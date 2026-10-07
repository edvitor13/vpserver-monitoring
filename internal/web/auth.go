package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

// Auth guarda os usuários (users.json) e as sessões. O cookie de sessão leva o
// nome do usuário e é assinado com uma chave dele, que mistura o segredo do
// painel com a senha (ou o hash dela) e o "epoch": trocar a senha ou as
// permissões de alguém derruba as sessões só dessa pessoa.
type Auth struct {
	envUser string
	envPass string // VPMON_PASSWORD: vale para o admin inicial enquanto ele não trocar pela tela
	secret  string
	secure  bool
	dir     string // pasta de dados ("" = nada é gravado; só o admin do .env)
	path    string // <dir>/users.json

	mu    sync.RWMutex
	users []User
	mtime time.Time // do users.json carregado (outro processo pode mudar: vpmon reset-password)

	pendMu  sync.Mutex
	pending map[string]pendingTOTP // 2FA esperando a confirmação (QR mostrado)
	now     func() time.Time       // relógio do 2FA (trocável nos testes)

	failMu sync.Mutex
	fails  map[string][]time.Time
}

func deriveKey(secret, user, material string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("vpmon-session\x00" + user + "\x00" + material))
	return mac.Sum(nil)
}

// NewAuth monta o login. dir é a pasta de dados (users.json); vazio desliga a
// gravação. Sem senha no ambiente, vale admin/admin com troca obrigatória;
// forceChange obriga a trocar também a senha inicial do .env (o instalador liga isso).
func NewAuth(user, pass, secret string, secure bool, dir string, forceChange bool) *Auth {
	if user == "" {
		user = DefaultUser
	}
	if pass == "" {
		pass, forceChange = DefaultPassword, true
	}
	a := &Auth{envUser: user, envPass: pass, secret: secret, secure: secure, dir: dir, fails: map[string][]time.Time{},
		pending: map[string]pendingTOTP{}, now: time.Now}
	if dir != "" {
		a.path = filepath.Join(dir, "users.json")
	}
	if users, mt, ok := readUsers(a.path); ok {
		a.users, a.mtime = users, mt
		return a
	}
	users, migrated := initialUsers(dir, user, forceChange)
	a.users = users
	if migrated { // auth.json de antes dos usuários: vira users.json
		if mt, err := writeUsers(a.path, users); err == nil {
			a.mtime = mt
			os.Rename(filepath.Join(dir, "auth.json"), filepath.Join(dir, "auth.json.migrado"))
		}
	}
	return a
}

// reload relê o users.json se ele mudou por fora (vpmon reset-password).
func (a *Auth) reload() {
	if a.path == "" {
		return
	}
	st, err := os.Stat(a.path)
	if err != nil {
		return
	}
	a.mu.RLock()
	same := st.ModTime().Equal(a.mtime)
	a.mu.RUnlock()
	if same {
		return
	}
	if users, mt, ok := readUsers(a.path); ok {
		a.mu.Lock()
		a.users, a.mtime = users, mt
		a.mu.Unlock()
	}
}

func (a *Auth) keyFor(u User) []byte {
	material := u.Hash
	if material == "" {
		material = a.envPass
	}
	if u.Epoch != "" {
		material += "\x00" + u.Epoch
	}
	return deriveKey(a.secret, u.Name, material)
}

func sign(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Auth) checkPass(u User, pass string) bool {
	if u.Hash != "" {
		return VerifyPassword(u.Hash, pass)
	}
	hp, wp := sha256.Sum256([]byte(pass)), sha256.Sum256([]byte(a.envPass))
	return subtle.ConstantTimeCompare(hp[:], wp[:]) == 1
}

// dummyHash é conferido quando o usuário não existe, para o tempo da
// resposta não revelar quais nomes existem (calculado só no primeiro uso:
// o healthcheck e o init não pagam o PBKDF2).
var dummyHash = sync.OnceValue(func() string { return HashPassword("vpmon-usuario-que-nao-existe") })

// Login confere usuário e senha.
func (a *Auth) Login(name, pass string) (User, bool) {
	u, ok := a.Get(name)
	if !ok {
		VerifyPassword(dummyHash(), pass)
		return User{}, false
	}
	return u, a.checkPass(u, pass)
}

// Issue grava o cookie de sessão de um usuário.
func (a *Auth) Issue(w http.ResponseWriter, name string) {
	u, ok := a.Get(name)
	if !ok {
		return
	}
	nonce := make([]byte, 12)
	rand.Read(nonce)
	exp := time.Now().Add(sessionTTL).Unix()
	payload := base64.RawURLEncoding.EncodeToString([]byte(u.Name)) + "." + strconv.FormatInt(exp, 10) + "." +
		base64.RawURLEncoding.EncodeToString(nonce)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: payload + "." + sign(a.keyFor(u), payload), Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode,
	})
}

func (a *Auth) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}

// Session devolve o usuário de uma sessão válida e não vencida.
func (a *Auth) Session(r *http.Request) (User, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return User{}, false
	}
	i := strings.LastIndexByte(c.Value, '.')
	if i < 0 {
		return User{}, false
	}
	payload, sig := c.Value[:i], c.Value[i+1:]
	parts := strings.Split(payload, ".")
	a.reload()
	a.mu.RLock()
	var cands []User
	var expS string
	switch len(parts) {
	case 3: // usuário.vencimento.nonce
		name, err := base64.RawURLEncoding.DecodeString(parts[0])
		if j := findUser(a.users, string(name)); err == nil && j >= 0 {
			cands = append(cands, a.users[j])
		}
		expS = parts[1]
	case 2: // sessão de antes dos usuários (um login só): vale para o admin migrado, até ele mudar
		for _, u := range a.users {
			if u.Admin && u.Epoch == "" {
				cands = append(cands, u)
			}
		}
		expS = parts[0]
	}
	a.mu.RUnlock()
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || time.Now().Unix() >= exp {
		return User{}, false
	}
	for _, u := range cands {
		if subtle.ConstantTimeCompare([]byte(sig), []byte(sign(a.keyFor(u), payload))) == 1 {
			return u, true
		}
	}
	return User{}, false
}

// PasswordInfo diz de onde vem a senha: "env" (.env) ou "panel" (trocada pela tela, com a data).
func (a *Auth) PasswordInfo(u User) (source string, changed int64) {
	if u.Hash != "" {
		return "panel", u.Changed
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
