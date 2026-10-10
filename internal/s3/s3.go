// Package s3 fala com armazenamento compatível com S3 (Cloudflare R2, AWS S3,
// Backblaze B2, MinIO...) só com a biblioteca padrão: assinatura AWS v4,
// endereçamento por caminho (https://endpoint/bucket/chave), envio simples ou
// em partes, conferência, listagem, download e remoção.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	emptyHash   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	maxAttempts = 3
)

// Variáveis só para os testes encurtarem.
var (
	partSize      int64 = 64 << 20  // envio em partes: 64 MB cada
	partThreshold int64 = 256 << 20 // acima disso, em partes
	retryWait           = 2 * time.Second
)

// Config é o armazenamento (o segredo nunca volta para a tela).
type Config struct {
	Endpoint  string `json:"endpoint"` // https://<conta>.r2.cloudflarestorage.com
	Region    string `json:"region"`   // "auto" no R2
	Bucket    string `json:"bucket"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Validate confere o formato (não fala com o armazenamento).
func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	switch {
	case err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil:
		return errors.New("endpoint inválido (ex.: https://<id-da-conta>.r2.cloudflarestorage.com)")
	case u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname())):
		return errors.New("o endpoint precisa ser https")
	case !bucketName.MatchString(c.Bucket):
		return errors.New("nome de bucket inválido (3 a 63 caracteres: letras minúsculas, números, ponto e hífen)")
	case strings.TrimSpace(c.AccessKey) == "" || strings.TrimSpace(c.SecretKey) == "":
		return errors.New("informe a Access Key ID e a Secret Access Key")
	case len(c.AccessKey) > 128 || len(c.SecretKey) > 256 || strings.ContainsAny(c.AccessKey+c.SecretKey, " \n\t"):
		return errors.New("chaves de acesso em formato inválido")
	}
	return nil
}

func isLocal(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

type Client struct {
	cfg Config
	hc  *http.Client
	now func() time.Time
}

func New(cfg Config) (*Client, error) {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.AccessKey, cfg.SecretKey = strings.TrimSpace(cfg.AccessKey), strings.TrimSpace(cfg.SecretKey)
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, hc: &http.Client{}, now: time.Now}, nil
}

// --- assinatura v4 ---------------------------------------------------------------------------

func hmacSHA(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// uriEncode segue a regra da AWS: só A-Z a-z 0-9 - _ . ~ ficam (e "/" no caminho).
func uriEncode(s string, keepSlash bool) string {
	var sb strings.Builder
	for _, b := range []byte(s) {
		switch {
		case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9', b == '-', b == '_', b == '.', b == '~':
			sb.WriteByte(b)
		case b == '/' && keepSlash:
			sb.WriteByte(b)
		default:
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// signable são os cabeçalhos assinados (além de host e x-amz-*).
var signable = map[string]bool{"content-type": true, "content-md5": true, "range": true, "date": true}

// sign assina o pedido (cabeçalhos x-amz-date e x-amz-content-sha256 já postos).
func sign(req *http.Request, accessKey, secretKey, region string, t time.Time) {
	amzDate := t.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	hdrs := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || signable[lk] {
			hdrs[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	names := make([]string, 0, len(hdrs))
	for k := range hdrs {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + hdrs[k] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if u, err := url.PathUnescape(path); err == nil {
		path = uriEncode(u, true)
	}
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, path, canonicalQuery(req.URL.Query()), ch.String(), signed,
		req.Header.Get("x-amz-content-sha256")}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA(hmacSHA(hmacSHA(hmacSHA([]byte("AWS4"+secretKey), day), region), "s3"), "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSHA(key, toSign)))
}

// --- pedidos ---------------------------------------------------------------------------------

// Error é a resposta de erro do armazenamento, com uma mensagem para a tela.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	switch e.Code {
	case "SignatureDoesNotMatch", "InvalidAccessKeyId", "InvalidToken":
		return "o armazenamento recusou as credenciais (Access Key ID ou Secret Access Key erradas)"
	case "AccessDenied":
		return "sem permissão no bucket (o token precisa de leitura e escrita de objetos neste bucket)"
	case "NoSuchBucket":
		return "o bucket não existe (crie no painel do provedor antes)"
	case "NoSuchKey":
		return "arquivo não encontrado no bucket"
	}
	if e.Code != "" {
		return fmt.Sprintf("o armazenamento respondeu %d: %s", e.Status, e.Code)
	}
	return fmt.Sprintf("o armazenamento respondeu %d", e.Status)
}

func (c *Client) objectURL(key string, q url.Values) string {
	u := c.cfg.Endpoint + "/" + c.cfg.Bucket
	if key != "" {
		u += "/" + uriEncode(key, true)
	}
	if len(q) > 0 {
		u += "?" + canonicalQuery(q)
	}
	return u
}

// do monta, assina e manda; body pode ser nil. Devolve a resposta só com 2xx.
func (c *Client) do(ctx context.Context, method, key string, q url.Values, body io.ReadSeeker, size int64, payloadHash string, hdr http.Header) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var rdr io.Reader
		if body != nil {
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			rdr = io.LimitReader(body, size)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.objectURL(key, q), rdr)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.ContentLength = size
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		if payloadHash == "" {
			payloadHash = emptyHash
		}
		req.Header.Set("x-amz-content-sha256", payloadHash)
		sign(req, c.cfg.AccessKey, c.cfg.SecretKey, c.cfg.Region, c.now())
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("o armazenamento não respondeu: %w", err)
		} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		} else {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			var e struct {
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			}
			xml.Unmarshal(raw, &e)
			lastErr = &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return nil, lastErr // erro de pedido: não adianta repetir
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * retryWait):
		}
	}
	return nil, lastErr
}

// hashSection lê o trecho e devolve o SHA-256 (o pedido vai assinado com o hash real).
func hashSection(f io.ReadSeeker, off, n int64) (string, error) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type section struct {
	f        *os.File
	off, n   int64
	position int64
}

func (s *section) Read(p []byte) (int, error) {
	if s.position >= s.n {
		return 0, io.EOF
	}
	if int64(len(p)) > s.n-s.position {
		p = p[:s.n-s.position]
	}
	n, err := s.f.ReadAt(p, s.off+s.position)
	s.position += int64(n)
	if err == io.EOF && s.position < s.n {
		return n, io.ErrUnexpectedEOF
	}
	if s.position >= s.n {
		err = nil
	}
	return n, err
}

func (s *section) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekStart || off < 0 || off > s.n {
		return 0, errors.New("seek inválido")
	}
	s.position = off
	return off, nil
}

// PutFile envia o arquivo inteiro para key (em partes, se for grande).
func (c *Client) PutFile(ctx context.Context, key string, f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size > partThreshold {
		return c.putMultipart(ctx, key, f, size)
	}
	hash, err := hashSection(f, 0, size)
	if err != nil {
		return err
	}
	hdr := http.Header{"Content-Type": {"application/octet-stream"}}
	resp, err := c.do(ctx, http.MethodPut, key, nil, &section{f: f, n: size}, size, hash, hdr)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

func (c *Client) putMultipart(ctx context.Context, key string, f *os.File, size int64) error {
	resp, err := c.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, 0, "", nil)
	if err != nil {
		return err
	}
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	err = xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&init)
	resp.Body.Close()
	if err != nil || init.UploadID == "" {
		return errors.New("o armazenamento não abriu o envio em partes")
	}
	abort := func() {
		ac, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if r, err := c.do(ac, http.MethodDelete, key, url.Values{"uploadId": {init.UploadID}}, nil, 0, "", nil); err == nil {
			r.Body.Close()
		}
	}
	type part struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	var parts []part
	for n, off := 1, int64(0); off < size; n, off = n+1, off+partSize {
		ln := min(partSize, size-off)
		hash, err := hashSection(f, off, ln)
		if err != nil {
			abort()
			return err
		}
		q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {init.UploadID}}
		r, err := c.do(ctx, http.MethodPut, key, q, &section{f: f, off: off, n: ln}, ln, hash, nil)
		if err != nil {
			abort()
			return fmt.Errorf("parte %d: %w", n, err)
		}
		etag := r.Header.Get("ETag")
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if etag == "" {
			abort()
			return fmt.Errorf("parte %d: o armazenamento não devolveu o ETag", n)
		}
		parts = append(parts, part{PartNumber: n, ETag: etag})
	}
	body, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{Parts: parts})
	r, err := c.do(ctx, http.MethodPost, key, url.Values{"uploadId": {init.UploadID}}, bytes.NewReader(body), int64(len(body)),
		sha256Hex(body), http.Header{"Content-Type": {"application/xml"}})
	if err != nil {
		abort()
		return err
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	r.Body.Close()
	if bytes.Contains(raw, []byte("<Error>")) { // o S3 pode responder 200 com erro no corpo
		abort()
		return errors.New("o armazenamento não fechou o envio em partes")
	}
	return nil
}

// Head devolve o tamanho do arquivo no bucket.
func (c *Client) Head(ctx context.Context, key string) (int64, error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, nil, 0, "", nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.ContentLength, nil
}

// Get abre o arquivo do bucket para leitura (quem chama fecha).
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil, 0, "", nil)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, nil, 0, "", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Object é um arquivo listado.
type Object struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// List devolve os arquivos com o prefixo (todas as páginas, até max).
func (c *Client) List(ctx context.Context, prefix string, max int) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, http.MethodGet, "", q, nil, 0, "", nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
			IsTruncated bool   `xml:"IsTruncated"`
			Next        string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, errors.New("resposta estranha do armazenamento na listagem")
		}
		for _, o := range page.Contents {
			t, _ := time.Parse(time.RFC3339, o.LastModified)
			out = append(out, Object{Key: o.Key, Size: o.Size, Modified: t})
		}
		if !page.IsTruncated || page.Next == "" || len(out) >= max {
			return out, nil
		}
		token = page.Next
	}
}
