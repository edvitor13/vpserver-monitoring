package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// Verificação em duas etapas por TOTP (RFC 6238), o padrão dos apps
// autenticadores (Google/Microsoft Authenticator, Authy, 1Password, Aegis):
// HMAC-SHA1 sobre o número do intervalo de 30 s, 6 dígitos. Tudo local, sem
// API externa: o segredo fica no users.json e o celular calcula o mesmo código.

const (
	totpDigits = 6
	totpPeriod = 30
	totpSkew   = 1 // aceita um intervalo antes e um depois (relógios até 30 s fora)
	recoveryN  = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret gera um segredo de 160 bits (o tamanho do HMAC-SHA1).
func newTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return b32.EncodeToString(b)
}

// totpCode é o código de um intervalo.
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchTOTP devolve o intervalo aceito ou -1. Não aceita intervalo igual ou
// anterior a last: o mesmo código não serve duas vezes.
func matchTOTP(secretB32, code string, now time.Time, last int64) int64 {
	code = digits(code)
	secret, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secretB32)))
	if len(code) != totpDigits || err != nil {
		return -1
	}
	step := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		s := step + d
		if s > last && subtle.ConstantTimeCompare([]byte(totpCode(secret, s)), []byte(code)) == 1 {
			return s
		}
	}
	return -1
}

// otpauthURI é o conteúdo do QR code (e do link que abre o app no celular).
func otpauthURI(issuer, account, secretB32 string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secretB32}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// groupSecret mostra o segredo em blocos de 4 (para digitar no app).
func groupSecret(s string) string {
	var parts []string
	for i := 0; i < len(s); i += 4 {
		parts = append(parts, s[i:min(i+4, len(s))])
	}
	return strings.Join(parts, " ")
}

// --- códigos de recuperação -------------------------------------------------------------

// newRecoveryCodes gera os códigos (xxxx-xxxx, ~40 bits cada) e os hashes guardados.
func newRecoveryCodes() (plain, hashes []string) {
	const chars = "abcdefghijkmnpqrstuvwxyz23456789"
	for i := 0; i < recoveryN; i++ {
		var b strings.Builder
		for j := 0; j < 8; j++ {
			if j == 4 {
				b.WriteByte('-')
			}
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
			b.WriteByte(chars[n.Int64()])
		}
		plain = append(plain, b.String())
		hashes = append(hashes, recoveryHash(b.String()))
	}
	return plain, hashes
}

func normRecovery(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

func recoveryHash(code string) string {
	sum := sha256.Sum256([]byte("vpmon-recovery\x00" + normRecovery(code)))
	return hex.EncodeToString(sum[:])
}

// useRecovery procura o código entre os hashes; devolve a lista sem ele (uso único).
func useRecovery(hashes []string, code string) ([]string, bool) {
	if len(normRecovery(code)) != 8 {
		return hashes, false
	}
	h := recoveryHash(code)
	for i, x := range hashes {
		if subtle.ConstantTimeCompare([]byte(x), []byte(h)) == 1 {
			return append(append([]string(nil), hashes[:i]...), hashes[i+1:]...), true
		}
	}
	return hashes, false
}
