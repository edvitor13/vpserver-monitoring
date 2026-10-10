package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Verificação em duas etapas por usuário: o segredo TOTP, os hashes dos
// códigos de recuperação e o último intervalo aceito ficam no users.json.

type TOTPState struct {
	Secret   string   `json:"secret"`   // base32
	Since    int64    `json:"since"`    // quando foi ligado
	Recovery []string `json:"recovery"` // hashes dos códigos de recuperação que sobram
	Last     int64    `json:"last"`     // último intervalo aceito (o mesmo código não vale duas vezes)
}

// TwoFAInfo é o que a tela vê do 2FA de alguém.
type TwoFAInfo struct {
	Enabled      bool  `json:"enabled"`
	Since        int64 `json:"since,omitempty"`
	RecoveryLeft int   `json:"recoveryLeft"`
	Asked        bool  `json:"asked"` // já recomendamos (não insiste)
}

func (u User) TwoFA() TwoFAInfo {
	i := TwoFAInfo{Asked: u.Asked2FA}
	if u.TOTP != nil {
		i.Enabled, i.Since, i.RecoveryLeft = true, u.TOTP.Since, len(u.TOTP.Recovery)
	}
	return i
}

var (
	ErrBadCode   = errors.New("código inválido ou vencido")
	ErrNoPending = errors.New("o QR code venceu: gere de novo")
	ErrNo2FA     = errors.New("a verificação em duas etapas não está ligada")
)

const (
	pendingTTL  = 15 * time.Minute // tempo para escanear o QR e confirmar
	ticketTTL   = 5 * time.Minute  // tempo para digitar o código depois da senha
	trustCookie = "vpmon_trust"
	trustTTL    = 30 * 24 * time.Hour // "lembrar este aparelho"
)

type pendingTOTP struct {
	secret string
	exp    time.Time
}

// StartTOTP gera um segredo novo para alguém ativar (vale 15 min até confirmar).
func (a *Auth) StartTOTP(name string) (string, error) {
	if a.dir == "" {
		return "", ErrNoStore
	}
	if _, ok := a.Get(name); !ok {
		return "", ErrNoUser
	}
	secret := newTOTPSecret()
	a.pendMu.Lock()
	a.pending[name] = pendingTOTP{secret: secret, exp: a.now().Add(pendingTTL)}
	a.pendMu.Unlock()
	return secret, nil
}

// EnableTOTP confirma o segredo pendente com um código do app e liga o 2FA.
// Devolve os códigos de recuperação (só desta vez). As outras sessões da pessoa caem.
func (a *Auth) EnableTOTP(name, code string) ([]string, error) {
	a.pendMu.Lock()
	p, ok := a.pending[name]
	a.pendMu.Unlock()
	if !ok || a.now().After(p.exp) {
		return nil, ErrNoPending
	}
	step := matchTOTP(p.secret, code, a.now(), 0)
	if step < 0 {
		return nil, ErrBadCode
	}
	plain, hashes := newRecoveryCodes()
	err := a.mutate(func(users []User) ([]User, error) {
		i := findUser(users, name)
		if i < 0 {
			return nil, ErrNoUser
		}
		users[i].TOTP = &TOTPState{Secret: p.secret, Since: a.now().Unix(), Recovery: hashes, Last: step}
		users[i].Asked2FA, users[i].Epoch = true, randomEpoch()
		return users, nil
	})
	if err != nil {
		return nil, err
	}
	a.pendMu.Lock()
	delete(a.pending, name)
	a.pendMu.Unlock()
	return plain, nil
}

// VerifySecond confere o segundo fator: um código do app ou, se não for, um
// código de recuperação (que deixa de valer). Devolve "totp" ou "recovery" e
// quantos códigos de recuperação sobraram.
func (a *Auth) VerifySecond(name, code string) (string, int, error) {
	used, left := "", 0
	err := a.mutate(func(users []User) ([]User, error) {
		i := findUser(users, name)
		if i < 0 || users[i].TOTP == nil {
			return nil, ErrNo2FA
		}
		t := *users[i].TOTP
		if step := matchTOTP(t.Secret, code, a.now(), t.Last); step >= 0 {
			t.Last, used = step, "totp"
		} else if rest, ok := useRecovery(t.Recovery, code); ok {
			t.Recovery, used = rest, "recovery"
		} else {
			return nil, ErrBadCode
		}
		users[i].TOTP, left = &t, len(t.Recovery)
		return users, nil
	})
	return used, left, err
}

// VerifyTOTP confere só um código do app (os de recuperação não valem aqui):
// é o que abre a sessão de SSH. O mesmo código não vale duas vezes.
func (a *Auth) VerifyTOTP(name, code string) error {
	return a.mutate(func(users []User) ([]User, error) {
		i := findUser(users, name)
		if i < 0 || users[i].TOTP == nil {
			return nil, ErrNo2FA
		}
		t := *users[i].TOTP
		step := matchTOTP(t.Secret, code, a.now(), t.Last)
		if step < 0 {
			return nil, ErrBadCode
		}
		t.Last = step
		users[i].TOTP = &t
		return users, nil
	})
}

// DisableTOTP desliga o próprio 2FA (pede a senha e um código).
func (a *Auth) DisableTOTP(name, pass, code string) error {
	u, ok := a.Get(name)
	if !ok {
		return ErrNoUser
	}
	if u.TOTP == nil {
		return ErrNo2FA
	}
	if !a.checkPass(u, pass) {
		return ErrBadCurrent
	}
	if _, _, err := a.VerifySecond(name, code); err != nil {
		return err
	}
	return a.mutate(func(users []User) ([]User, error) {
		if i := findUser(users, name); i >= 0 {
			users[i].TOTP, users[i].Epoch = nil, randomEpoch()
		}
		return users, nil
	})
}

// NewRecoveryCodes troca os códigos de recuperação (pede um código do app).
func (a *Auth) NewRecoveryCodes(name, code string) ([]string, error) {
	var plain []string
	err := a.mutate(func(users []User) ([]User, error) {
		i := findUser(users, name)
		if i < 0 || users[i].TOTP == nil {
			return nil, ErrNo2FA
		}
		t := *users[i].TOTP
		step := matchTOTP(t.Secret, code, a.now(), t.Last)
		if step < 0 {
			return nil, ErrBadCode
		}
		var hashes []string
		plain, hashes = newRecoveryCodes()
		t.Last, t.Recovery = step, hashes
		users[i].TOTP = &t
		return users, nil
	})
	return plain, err
}

// DisableTOTPFor desliga o 2FA de outra pessoa (celular perdido). Mesmas
// regras de quem pode mexer em quem dos outros ajustes de usuário.
func (a *Auth) DisableTOTPFor(actor User, name string) error {
	return a.mutate(func(users []User) ([]User, error) {
		i, err := target(users, actor, name)
		if err != nil {
			return nil, err
		}
		if users[i].TOTP == nil {
			return nil, ErrNo2FA
		}
		users[i].TOTP, users[i].Epoch = nil, randomEpoch()
		return users, nil
	})
}

// MarkAsked anota que a recomendação do 2FA já foi feita para alguém.
func (a *Auth) MarkAsked(name string) error {
	return a.mutate(func(users []User) ([]User, error) {
		if i := findUser(users, name); i >= 0 {
			users[i].Asked2FA = true
		}
		return users, nil
	})
}

// --- segundo passo do login ---------------------------------------------------------------

// Ticket liga a senha certa ao código que vem em seguida (vale 5 min).
func (a *Auth) Ticket(name string) string {
	u, _ := a.Get(name)
	nonce := make([]byte, 9)
	rand.Read(nonce)
	payload := "2fa." + base64.RawURLEncoding.EncodeToString([]byte(u.Name)) + "." +
		strconv.FormatInt(time.Now().Add(ticketTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	return payload + "." + sign(a.keyFor(u), "ticket\x00"+payload)
}

// CheckTicket devolve o usuário de um bilhete válido e não vencido.
func (a *Auth) CheckTicket(t string) (User, bool) {
	i := strings.LastIndexByte(t, '.')
	if i < 0 {
		return User{}, false
	}
	payload, sig := t[:i], t[i+1:]
	parts := strings.Split(payload, ".")
	if len(parts) != 4 || parts[0] != "2fa" {
		return User{}, false
	}
	name, err := base64.RawURLEncoding.DecodeString(parts[1])
	exp, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || err2 != nil || time.Now().Unix() >= exp {
		return User{}, false
	}
	u, ok := a.Get(string(name))
	if !ok || subtle.ConstantTimeCompare([]byte(sig), []byte(sign(a.keyFor(u), "ticket\x00"+payload))) != 1 {
		return User{}, false
	}
	return u, true
}

// trustKey assina o "lembrar este aparelho": muda com a senha, o epoch e o segredo do 2FA.
func (a *Auth) trustKey(u User) []byte {
	mac := hmac.New(sha256.New, a.keyFor(u))
	mac.Write([]byte("trust\x00" + u.TOTP.Secret))
	return mac.Sum(nil)
}

// IssueTrust grava o "lembrar este aparelho por 30 dias".
func (a *Auth) IssueTrust(w http.ResponseWriter, name string) {
	u, ok := a.Get(name)
	if !ok || u.TOTP == nil {
		return
	}
	nonce := make([]byte, 9)
	rand.Read(nonce)
	payload := base64.RawURLEncoding.EncodeToString([]byte(u.Name)) + "." +
		strconv.FormatInt(time.Now().Add(trustTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	http.SetCookie(w, &http.Cookie{Name: trustCookie, Value: payload + "." + sign(a.trustKey(u), payload), Path: "/",
		MaxAge: int(trustTTL.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}

// Trusted diz se este navegador foi lembrado para esse usuário.
func (a *Auth) Trusted(r *http.Request, u User) bool {
	c, err := r.Cookie(trustCookie)
	if err != nil || u.TOTP == nil {
		return false
	}
	i := strings.LastIndexByte(c.Value, '.')
	if i < 0 {
		return false
	}
	payload, sig := c.Value[:i], c.Value[i+1:]
	parts := strings.Split(payload, ".")
	if len(parts) != 3 {
		return false
	}
	name, err := base64.RawURLEncoding.DecodeString(parts[0])
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || err2 != nil || string(name) != u.Name || time.Now().Unix() >= exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sig), []byte(sign(a.trustKey(u), payload))) == 1
}
