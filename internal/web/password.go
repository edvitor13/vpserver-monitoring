package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Senhas guardadas só como hash PBKDF2-SHA256 (users.json). O auth.json era o
// formato de antes dos usuários (um login só): só é lido para migrar.

const pbkdf2Iter = 310000 // recomendação OWASP para PBKDF2-HMAC-SHA256

func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	n := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, n*hLen)
	buf := make([]byte, 4)
	for block := 1; block <= n; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Write(buf)
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// HashPassword devolve "pbkdf2-sha256$<iter>$<sal>$<hash>".
func HashPassword(pass string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	dk := pbkdf2([]byte(pass), salt, pbkdf2Iter, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter, enc.EncodeToString(salt), enc.EncodeToString(dk))
}

// VerifyPassword confere a senha contra o hash, em tempo constante.
func VerifyPassword(hash, pass string) bool {
	p := strings.Split(hash, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(p[1])
	if err != nil || iter < 1000 || iter > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(p[2])
	want, err2 := enc.DecodeString(p[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(pbkdf2([]byte(pass), salt, iter, len(want)), want) == 1
}

// storedAuth é o auth.json antigo.
type storedAuth struct {
	User    string `json:"user,omitempty"`
	Hash    string `json:"hash"`
	Changed int64  `json:"changed"`
}

func loadStored(path string) (storedAuth, bool) {
	var s storedAuth
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Hash == "" {
		return s, false
	}
	return s, true
}
