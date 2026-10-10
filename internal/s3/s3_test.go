package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exemplos da documentação da AWS (Signature Version 4, "Examples: Signature
// Calculations"): mesmas credenciais, data e assinatura esperada.
func TestSignatureMatchesAWSExamples(t *testing.T) {
	const ak, sk = "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

	get, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	get.Header.Set("Range", "bytes=0-9")
	get.Header.Set("x-amz-content-sha256", emptyHash)
	sign(get, ak, sk, "us-east-1", at)
	if !strings.HasSuffix(get.Header.Get("Authorization"), "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41") {
		t.Fatalf("GET: %s", get.Header.Get("Authorization"))
	}

	put, _ := http.NewRequest("PUT", "https://examplebucket.s3.amazonaws.com/test$file.text", strings.NewReader("Welcome to Amazon S3."))
	put.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	put.Header.Set("x-amz-storage-class", "REDUCED_REDUNDANCY")
	put.Header.Set("x-amz-content-sha256", sha256Hex([]byte("Welcome to Amazon S3.")))
	sign(put, ak, sk, "us-east-1", at)
	if !strings.HasSuffix(put.Header.Get("Authorization"), "Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd") {
		t.Fatalf("PUT: %s", put.Header.Get("Authorization"))
	}

	list, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	list.Header.Set("x-amz-content-sha256", emptyHash)
	sign(list, ak, sk, "us-east-1", at)
	if !strings.HasSuffix(list.Header.Get("Authorization"), "Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7") {
		t.Fatalf("listagem: %s", list.Header.Get("Authorization"))
	}
}

func TestConfigValidation(t *testing.T) {
	ok := Config{Endpoint: "https://abc123.r2.cloudflarestorage.com", Bucket: "meus-backups", AccessKey: "a", SecretKey: "b"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Config{
		{Endpoint: "http://abc.r2.cloudflarestorage.com", Bucket: "meus-backups", AccessKey: "a", SecretKey: "b"},
		{Endpoint: "https://abc.r2.cloudflarestorage.com/caminho", Bucket: "meus-backups", AccessKey: "a", SecretKey: "b"},
		{Endpoint: "https://user:x@abc.example.com", Bucket: "meus-backups", AccessKey: "a", SecretKey: "b"},
		{Endpoint: "https://abc.example.com", Bucket: "Maiusculo", AccessKey: "a", SecretKey: "b"},
		{Endpoint: "https://abc.example.com", Bucket: "meus-backups", AccessKey: "", SecretKey: "b"},
		{Endpoint: "https://abc.example.com", Bucket: "meus-backups", AccessKey: "a b", SecretKey: "b"},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("caso %d deveria ser recusado", i)
		}
	}
	if err := (Config{Endpoint: "http://127.0.0.1:9000", Bucket: "teste", AccessKey: "a", SecretKey: "b"}).Validate(); err != nil {
		t.Fatalf("http só em endereço local (testes): %v", err)
	}
}

// fakeS3 guarda em memória e confere a assinatura de cada pedido.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	calls   []string
	secret  string
	fail    int // falhas 503 a dar antes de aceitar
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	// confere a assinatura refazendo o cálculo com o segredo certo
	auth := r.Header.Get("Authorization")
	check, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	for k, v := range r.Header {
		if k != "Authorization" {
			check.Header[k] = v
		}
	}
	at, _ := time.Parse("20060102T150405Z", r.Header.Get("x-amz-date"))
	sign(check, "AK", f.secret, "auto", at)
	if check.Header.Get("Authorization") != auth {
		w.WriteHeader(403)
		fmt.Fprint(w, "<Error><Code>SignatureDoesNotMatch</Code></Error>")
		return
	}
	if f.fail > 0 {
		f.fail--
		w.WriteHeader(503)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("x-amz-content-sha256") != sha256Hex(body) {
		w.WriteHeader(400)
		fmt.Fprint(w, "<Error><Code>XAmzContentSHA256Mismatch</Code></Error>")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if parts[0] != "bucket" {
		w.WriteHeader(404)
		fmt.Fprint(w, "<Error><Code>NoSuchBucket</Code></Error>")
		return
	}
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	q := r.URL.Query()
	switch {
	case r.Method == "GET" && key == "":
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, q.Get("prefix")) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		start := 0
		if tok := q.Get("continuation-token"); tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		end := min(start+2, len(keys)) // páginas de 2 para testar a paginação
		fmt.Fprint(w, "<ListBucketResult>")
		for _, k := range keys[start:end] {
			fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-09T10:00:00.000Z</LastModified></Contents>", k, len(f.objects[k]))
		}
		if end < len(keys) {
			fmt.Fprintf(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", end)
		}
		fmt.Fprint(w, "</ListBucketResult>")
	case r.Method == "POST" && q.Has("uploads"):
		id := fmt.Sprintf("up%d", len(f.uploads)+1)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == "PUT" && q.Has("partNumber"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		f.uploads[q.Get("uploadId")][n] = body
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, n))
	case r.Method == "POST" && q.Has("uploadId"):
		var done struct {
			Parts []struct {
				PartNumber int `xml:"PartNumber"`
			} `xml:"Part"`
		}
		xml.Unmarshal(body, &done)
		var all []byte
		for _, p := range done.Parts {
			all = append(all, f.uploads[q.Get("uploadId")][p.PartNumber]...)
		}
		f.objects[key] = all
		delete(f.uploads, q.Get("uploadId"))
		fmt.Fprint(w, "<CompleteMultipartUploadResult/>")
	case r.Method == "DELETE" && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(204)
	case r.Method == "PUT":
		f.objects[key] = body
	case r.Method == "HEAD" || r.Method == "GET":
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(404)
			if r.Method == "GET" {
				fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			}
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == "GET" {
			w.Write(b)
		}
	case r.Method == "DELETE":
		delete(f.objects, key)
		w.WriteHeader(204)
	}
}

func newFake(t *testing.T) (*Client, *fakeS3) {
	ps, pt, rw := partSize, partThreshold, retryWait
	partSize, partThreshold, retryWait = 1<<20, 3<<20, 10*time.Millisecond
	t.Cleanup(func() { partSize, partThreshold, retryWait = ps, pt, rw })
	f := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}, secret: "segredo"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "AK", SecretKey: "segredo"})
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}

func tempFile(t *testing.T, data []byte) *os.File {
	p := filepath.Join(t.TempDir(), "arquivo")
	os.WriteFile(p, data, 0o600)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestPutHeadGetListDelete(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	data := []byte("conteúdo do backup")
	for _, key := range []string{"srv/banco/hourly/a b+c.age", "srv/banco/daily/x.age", "srv/outro/hourly/y.age"} {
		if err := c.PutFile(ctx, key, tempFile(t, data)); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if n, err := c.Head(ctx, "srv/banco/hourly/a b+c.age"); err != nil || n != int64(len(data)) {
		t.Fatalf("head: %d %v", n, err)
	}
	rc, _, err := c.Get(ctx, "srv/banco/daily/x.age")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("download diferente")
	}
	objs, err := c.List(ctx, "srv/banco/", 100)
	if err != nil || len(objs) != 2 || objs[0].Size != int64(len(data)) || objs[0].Modified.IsZero() {
		t.Fatalf("listagem: %+v %v", objs, err)
	}
	all, _ := c.List(ctx, "srv/", 100)
	if len(all) != 3 {
		t.Fatalf("paginação: %d", len(all))
	}
	if err := c.Delete(ctx, "srv/banco/daily/x.age"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Head(ctx, "srv/banco/daily/x.age"); err == nil {
		t.Fatal("apagado continua lá")
	}
	if _, _, err := c.Get(ctx, "nao/existe"); err == nil || !strings.Contains(err.Error(), "não encontrado") {
		t.Fatalf("arquivo que não existe: %v", err)
	}
	_ = f
}

func TestErrorsAndRetries(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	f.fail = 2 // duas falhas 503 e depois aceita
	if err := c.PutFile(ctx, "k", tempFile(t, []byte("x"))); err != nil {
		t.Fatalf("deveria repetir até dar certo: %v", err)
	}
	wrong := *c
	wrong.cfg.SecretKey = "errado"
	if _, err := wrong.Head(ctx, "k"); err == nil {
		t.Fatal("segredo errado aceito")
	}
	if err := wrong.PutFile(ctx, "k2", tempFile(t, []byte("x"))); err == nil || !strings.Contains(err.Error(), "credenciais") {
		t.Fatalf("mensagem de credencial: %v", err)
	}
	other := *c
	other.cfg.Bucket = "outro-bucket"
	if err := other.PutFile(ctx, "k", tempFile(t, []byte("x"))); err == nil || !strings.Contains(err.Error(), "bucket não existe") {
		t.Fatalf("bucket que não existe: %v", err)
	}
}

func TestMultipartUpload(t *testing.T) {
	c, f := newFake(t)
	data := make([]byte, partThreshold+partSize+partSize/2+123) // 4 partes e meia
	rand.Read(data)
	if err := c.PutFile(context.Background(), "grande.age", tempFile(t, data)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.objects["grande.age"], data) {
		t.Fatal("arquivo remontado diferente")
	}
	n := 0
	for _, call := range f.calls {
		if strings.Contains(call, "partNumber=") {
			n++
		}
	}
	if n != 5 || len(f.uploads) != 0 {
		t.Fatalf("partes: %d (abertos: %d)", n, len(f.uploads))
	}
}
