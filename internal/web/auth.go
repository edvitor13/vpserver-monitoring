package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cookieName = "vpmon_session"
	sessionTTL = 30 * 24 * time.Hour
	failWindow = 15 * time.Minute
	maxFailIP  = 8  // erros por IP na janela
	maxFailAll = 40 // erros no total na janela (freio contra ataque distribuído)
)

// DefaultUser e DefaultPassword valem quando ninguém definiu VPMON_PASSWORD:
// entra-se com admin/admin e o painel obriga a trocar antes de mostrar qualquer dado.
const (
	DefaultUser     = "admin"
	DefaultPassword = "admin"
)

// Auth é o login único do painel. A chave que assina o cookie mistura o
// segredo com o usuário e a senha (ou o hash dela): trocar a senha derruba
// todas as sessões.
type Auth struct {
	envUser   string
	envPass   string // VPMON_PASSWORD (vale enquanto não houver senha trocada pela tela)
	secret    string
	secure    bool
	storePath string // <data>/auth.json

	mu         sync.RWMutex
	user       string
	hash       string // senha trocada pela tela (PBKDF2); vazio = usa envPass
	changed    int64
	key        []byte
	mustChange bool // senha inicial: só deixa trocar a senha até trocar

	failMu sync.Mutex
	fails  map[string][]time.Time
}

func deriveKey(secret, user, material string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("vpmon-session\x00" + user + "\x00" + material))
	return mac.Sum(nil)
}

// NewAuth monta o login. storePath vazio desliga a troca de senha pela tela.
// Sem senha no ambiente, vale admin/admin com troca obrigatória; forceChange
// obriga a trocar também a senha inicial do .env (o instalador liga isso).
func NewAuth(user, pass, secret string, secure bool, storePath string, forceChange bool) *Auth {
	if user == "" {
		user = DefaultUser
	}
	if pass == "" {
		pass, forceChange = DefaultPassword, true
	}
	a := &Auth{envUser: user, envPass: pass, user: user, secret: secret, secure: secure, storePath: storePath,
		fails: map[string][]time.Time{}}
	if s, ok := loadStored(storePath); ok && storePath != "" {
		// já trocou pela tela: vale o que foi gravado (usuário e senha)
		a.hash, a.changed = s.Hash, s.Changed
		if s.User != "" {
			a.user = s.User
		}
		a.key = deriveKey(secret, a.user, s.Hash)
	} else {
		a.key = deriveKey(secret, user, pass)
		a.mustChange = forceChange
	}
	return a
}

// MustChange diz se o login atual ainda é o inicial (só pode trocar a senha).
func (a *Auth) MustChange() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mustChange
}

func (a *Auth) sign(payload string) string {
	a.mu.RLock()
	mac := hmac.New(sha256.New, a.key)
	a.mu.RUnlock()
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Auth) checkPass(pass string) bool {
	a.mu.RLock()
	hash := a.hash
	a.mu.RUnlock()
	if hash != "" {
		return VerifyPassword(hash, pass)
	}
	hp, wp := sha256.Sum256([]byte(pass)), sha256.Sum256([]byte(a.envPass))
	return subtle.ConstantTimeCompare(hp[:], wp[:]) == 1
}

// Check confere usuário e senha em tempo constante.
func (a *Auth) Check(user, pass string) bool {
	a.mu.RLock()
	cur := a.user
	a.mu.RUnlock()
	hu, wu := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(cur))
	okU := subtle.ConstantTimeCompare(hu[:], wu[:]) == 1
	okP := a.checkPass(pass)
	return okU && okP
}

// Issue grava o cookie de sessão.
func (a *Auth) Issue(w http.ResponseWriter) {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	exp := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: payload + "." + a.sign(payload), Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode,
	})
}

func (a *Auth) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}

// Valid diz se a requisição tem uma sessão válida e não vencida.
func (a *Auth) Valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(c.Value, '.')
	if i < 0 {
		return false
	}
	payload, sig := c.Value[:i], c.Value[i+1:]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign(payload))) != 1 {
		return false
	}
	expS, _, _ := strings.Cut(payload, ".")
	exp, err := strconv.ParseInt(expS, 10, 64)
	return err == nil && time.Now().Unix() < exp
}

func (a *Auth) User() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.user
}

// PasswordInfo diz de onde vem a senha: "env" (.env) ou "panel" (trocada pela tela, com a data).
func (a *Auth) PasswordInfo() (source string, changed int64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.hash != "" {
		return "panel", a.changed
	}
	return "env", 0
}

// --- freio de tentativas ------------------------------------------------------------

func (a *Auth) prune(now time.Time) int {
	total := 0
	for ip, ts := range a.fails {
		k := 0
		for _, t := range ts {
			if now.Sub(t) < failWindow {
				ts[k] = t
				k++
			}
		}
		if k == 0 {
			delete(a.fails, ip)
			continue
		}
		a.fails[ip] = ts[:k]
		total += k
	}
	return total
}

// Blocked diz se o IP (ou o painel inteiro) está bloqueado e por quanto tempo.
func (a *Auth) Blocked(ip string) (bool, time.Duration) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	now := time.Now()
	total := a.prune(now)
	ts := a.fails[ip]
	if len(ts) >= maxFailIP {
		return true, failWindow - now.Sub(ts[0])
	}
	if total >= maxFailAll {
		return true, time.Minute
	}
	return false, 0
}

func (a *Auth) Fail(ip string) {
	a.failMu.Lock()
	a.fails[ip] = append(a.fails[ip], time.Now())
	a.failMu.Unlock()
}

func (a *Auth) Reset(ip string) {
	a.failMu.Lock()
	delete(a.fails, ip)
	a.failMu.Unlock()
}

// clientIP: atrás do túnel da Cloudflare o IP real vem no CF-Connecting-IP
// (ninguém chega ao painel sem passar pelo túnel — não há porta publicada).
func clientIP(r *http.Request, trustCF bool) string {
	if trustCF {
		if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
