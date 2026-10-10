// Package age cifra no formato age v1 (https://age-encryption.org/v1), só com
// destinatário X25519: o bastante para os backups, que abrem com a ferramenta
// oficial (`age -d -i chave.txt arquivo.age`).
//
// Só biblioteca padrão (X25519, HMAC, SHA-256) e o ChaCha20-Poly1305 genérico
// copiado de golang.org/x/crypto (internal/xcrypto).
package age

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/chacha20poly1305"
)

const (
	intro      = "age-encryption.org/v1\n"
	x25519Info = "age-encryption.org/v1/X25519"
	chunkSize  = 64 << 10
	fileKeyLen = 16
	nonceLen   = 16
	tagLen     = chacha20poly1305.Overhead
)

var (
	b64          = base64.RawStdEncoding
	ErrBadKey    = errors.New("chave age inválida")
	ErrNoMatch   = errors.New("a chave privada não abre este arquivo")
	ErrCorrupted = errors.New("arquivo age corrompido ou alterado")
)

// --- chaves --------------------------------------------------------------------------------

// Recipient é a chave pública (quem pode abrir).
type Recipient struct{ pub *ecdh.PublicKey }

// Identity é a chave privada.
type Identity struct{ priv *ecdh.PrivateKey }

// GenerateIdentity cria um par novo.
func GenerateIdentity() (*Identity, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: k}, nil
}

// String é a chave privada em texto ("AGE-SECRET-KEY-1...").
func (i *Identity) String() string {
	s, _ := bech32Encode("AGE-SECRET-KEY-", i.priv.Bytes())
	return strings.ToUpper(s)
}

// Recipient é a chave pública do par.
func (i *Identity) Recipient() *Recipient { return &Recipient{pub: i.priv.PublicKey()} }

// String é a chave pública em texto ("age1...").
func (r *Recipient) String() string {
	s, _ := bech32Encode("age", r.pub.Bytes())
	return s
}

// ParseRecipient lê uma chave pública "age1...".
func ParseRecipient(s string) (*Recipient, error) {
	hrp, data, err := bech32Decode(strings.TrimSpace(s))
	if err != nil || hrp != "age" || len(data) != 32 {
		return nil, ErrBadKey
	}
	pub, err := ecdh.X25519().NewPublicKey(data)
	if err != nil {
		return nil, ErrBadKey
	}
	return &Recipient{pub: pub}, nil
}

// ParseIdentity lê uma chave privada "AGE-SECRET-KEY-1..." (aceita o arquivo
// do age-keygen, com comentários).
func ParseIdentity(s string) (*Identity, error) {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		hrp, data, err := bech32Decode(ln)
		if err != nil || hrp != "age-secret-key-" || len(data) != 32 {
			return nil, ErrBadKey
		}
		k, err := ecdh.X25519().NewPrivateKey(data)
		if err != nil {
			return nil, ErrBadKey
		}
		return &Identity{priv: k}, nil
	}
	return nil, ErrBadKey
}

// --- HKDF-SHA256 (RFC 5869) ----------------------------------------------------------------

func hkdf(ikm, salt []byte, info string, n int) []byte {
	if salt == nil {
		salt = make([]byte, sha256.Size)
	}
	ex := hmac.New(sha256.New, salt)
	ex.Write(ikm)
	prk := ex.Sum(nil)
	var out, prev []byte
	for c := byte(1); len(out) < n; c++ {
		m := hmac.New(sha256.New, prk)
		m.Write(prev)
		m.Write([]byte(info))
		m.Write([]byte{c})
		prev = m.Sum(nil)
		out = append(out, prev...)
	}
	return out[:n]
}

func aeadSeal(key, nonce, plain []byte) []byte {
	a, _ := chacha20poly1305.New(key)
	return a.Seal(nil, nonce, plain, nil)
}

func aeadOpen(key, nonce, sealed []byte) ([]byte, error) {
	a, _ := chacha20poly1305.New(key)
	return a.Open(nil, nonce, sealed, nil)
}

// --- cabeçalho -----------------------------------------------------------------------------

// wrapBody quebra o corpo de uma estrofe em linhas de 64 colunas (a última é
// sempre mais curta, mesmo que vazia).
func wrapBody(b []byte) string {
	s := b64.EncodeToString(b)
	var sb strings.Builder
	for len(s) >= 64 {
		sb.WriteString(s[:64] + "\n")
		s = s[64:]
	}
	sb.WriteString(s + "\n")
	return sb.String()
}

func headerMAC(fileKey []byte, header string) []byte {
	m := hmac.New(sha256.New, hkdf(fileKey, nil, "header", 32))
	m.Write([]byte(header))
	return m.Sum(nil)
}

// --- cifrar --------------------------------------------------------------------------------

// Encrypt devolve um escritor que cifra para r e grava em dst. Feche (Close)
// no fim: só então o último bloco é gravado.
func Encrypt(dst io.Writer, r *Recipient) (io.WriteCloser, error) {
	fileKey := make([]byte, fileKeyLen)
	if _, err := rand.Read(fileKey); err != nil {
		return nil, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(r.pub)
	if err != nil {
		return nil, err
	}
	share := eph.PublicKey().Bytes()
	salt := append(append([]byte{}, share...), r.pub.Bytes()...)
	wrapKey := hkdf(shared, salt, x25519Info, 32)
	body := aeadSeal(wrapKey, make([]byte, 12), fileKey)

	header := intro + "-> X25519 " + b64.EncodeToString(share) + "\n" + wrapBody(body) + "---"
	mac := headerMAC(fileKey, header)
	if _, err := io.WriteString(dst, header+" "+b64.EncodeToString(mac)+"\n"); err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	if _, err := dst.Write(nonce); err != nil {
		return nil, err
	}
	return &writer{dst: dst, key: hkdf(fileKey, nonce, "payload", 32), buf: make([]byte, 0, chunkSize)}, nil
}

type writer struct {
	dst    io.Writer
	key    []byte
	buf    []byte
	n      uint64 // blocos já gravados
	closed bool
	err    error
}

func chunkNonce(n uint64, last bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[3:11], n)
	if last {
		nonce[11] = 1
	}
	return nonce
}

func (w *writer) flush(last bool) error {
	_, err := w.dst.Write(aeadSeal(w.key, chunkNonce(w.n, last), w.buf))
	w.n++
	w.buf = w.buf[:0]
	return err
}

func (w *writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("age: escrita depois do Close")
	}
	if w.err != nil {
		return 0, w.err
	}
	total := len(p)
	for len(p) > 0 {
		if len(w.buf) == chunkSize { // bloco cheio e ainda tem dado: não é o último
			if w.err = w.flush(false); w.err != nil {
				return total - len(p), w.err
			}
		}
		n := copy(w.buf[len(w.buf):chunkSize], p)
		w.buf = w.buf[:len(w.buf)+n]
		p = p[n:]
	}
	return total, nil
}

func (w *writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	return w.flush(true)
}

// --- abrir (para os testes e a conferência) -------------------------------------------------

// Decrypt devolve um leitor do conteúdo original (confere tudo: cabeçalho e blocos).
func Decrypt(src io.Reader, id *Identity) (io.Reader, error) {
	br := bufio.NewReader(src)
	line := func() (string, error) {
		s, err := br.ReadString('\n')
		if err != nil {
			return "", ErrCorrupted
		}
		return s, nil
	}
	var header strings.Builder
	first, err := line()
	if err != nil || first != intro {
		return nil, ErrCorrupted
	}
	header.WriteString(first)
	var fileKey []byte
	for {
		ln, err := line()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(ln, "--- ") {
			header.WriteString("---")
			mac, err := b64.DecodeString(strings.TrimSuffix(ln[4:], "\n"))
			if err != nil || fileKey == nil {
				if fileKey == nil {
					return nil, ErrNoMatch
				}
				return nil, ErrCorrupted
			}
			if !hmac.Equal(mac, headerMAC(fileKey, header.String())) {
				return nil, ErrCorrupted
			}
			break
		}
		if !strings.HasPrefix(ln, "-> ") {
			return nil, ErrCorrupted
		}
		header.WriteString(ln)
		args := strings.Fields(strings.TrimSuffix(ln[3:], "\n"))
		var body []byte
		for { // corpo: linhas de 64 até a mais curta
			bl, err := line()
			if err != nil {
				return nil, err
			}
			header.WriteString(bl)
			part, err := b64.DecodeString(strings.TrimSuffix(bl, "\n"))
			if err != nil {
				return nil, ErrCorrupted
			}
			body = append(body, part...)
			if len(bl)-1 < 64 {
				break
			}
		}
		if fileKey != nil || len(args) != 2 || args[0] != "X25519" {
			continue
		}
		share, err := b64.DecodeString(args[1])
		if err != nil || len(share) != 32 || len(body) != fileKeyLen+tagLen {
			return nil, ErrCorrupted
		}
		ephPub, err := ecdh.X25519().NewPublicKey(share)
		if err != nil {
			return nil, ErrCorrupted
		}
		shared, err := id.priv.ECDH(ephPub)
		if err != nil {
			return nil, ErrCorrupted
		}
		salt := append(append([]byte{}, share...), id.priv.PublicKey().Bytes()...)
		if fk, err := aeadOpen(hkdf(shared, salt, x25519Info, 32), make([]byte, 12), body); err == nil {
			fileKey = fk
		}
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(br, nonce); err != nil {
		return nil, ErrCorrupted
	}
	return &reader{src: br, key: hkdf(fileKey, nonce, "payload", 32)}, nil
}

type reader struct {
	src  *bufio.Reader
	key  []byte
	n    uint64
	out  []byte
	done bool
	err  error
}

func (r *reader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		buf := make([]byte, chunkSize+tagLen)
		n, err := io.ReadFull(r.src, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			if err == io.EOF {
				r.err = ErrCorrupted // acabou sem o bloco final
			} else {
				r.err = err
			}
			continue
		}
		buf = buf[:n]
		// é o último se não há mais nada depois
		_, peekErr := r.src.Peek(1)
		last := peekErr == io.EOF
		plain, oerr := aeadOpen(r.key, chunkNonce(r.n, last), buf)
		if oerr != nil {
			r.err = ErrCorrupted
			continue
		}
		if !last && len(plain) != chunkSize {
			r.err = ErrCorrupted
			continue
		}
		r.n++
		r.out, r.done = plain, last
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// EncryptBytes e DecryptBytes: atalhos (testes e textos pequenos).
func EncryptBytes(plain []byte, r *Recipient) ([]byte, error) {
	var buf bytes.Buffer
	w, err := Encrypt(&buf, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecryptBytes(enc []byte, id *Identity) ([]byte, error) {
	r, err := Decrypt(bytes.NewReader(enc), id)
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return out, nil
}
