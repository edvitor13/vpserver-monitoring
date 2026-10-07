package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
)

// clock é o relógio fixo dos testes do 2FA (no meio de um intervalo de 30 s:
// a virada do intervalo nunca cai no meio do teste).
var clock = time.Unix(1_800_000_015, 0)

func newAuth2FA(t *testing.T, user string) (*Auth, string) {
	dir := t.TempDir()
	a := NewAuth(user, "senha-do-env-1", "s", true, dir, false)
	a.now = func() time.Time { return clock }
	return a, dir
}

// code gera o código do app para o intervalo do relógio dos testes (+offset).
func code(t *testing.T, secret string, offset int64) string {
	t.Helper()
	raw, err := b32.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totpCode(raw, clock.Unix()/totpPeriod+offset)
}

// enable2FA liga o 2FA de alguém e devolve o segredo e os códigos de recuperação.
func enable2FA(t *testing.T, a *Auth, name string) (string, []string) {
	t.Helper()
	sec, err := a.StartTOTP(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnableTOTP(name, "000000"); !errors.Is(err, ErrBadCode) && code(t, sec, 0) != "000000" {
		t.Fatalf("código errado deveria ser recusado: %v", err)
	}
	rec, err := a.EnableTOTP(name, code(t, sec, 0))
	if err != nil || len(rec) != recoveryN {
		t.Fatalf("ligar 2FA: %v", err)
	}
	return sec, rec
}

func postJSON(h http.Handler, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("X-Requested-With", "vpmon")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

func TestLoginWithTwoFactor(t *testing.T) {
	a, _ := newAuth2FA(t, "ana")
	old := httptest.NewRequest("GET", "/", nil)
	old.AddCookie(sessionCookie(a, "ana"))
	sec, recovery := enable2FA(t, a, "ana")
	if _, ok := a.Session(old); ok {
		t.Fatal("ligar o 2FA deveria derrubar as sessões abertas")
	}
	if u, _ := a.Get("ana"); !u.TwoFA().Enabled || !u.TwoFA().Asked || u.TwoFA().RecoveryLeft != recoveryN {
		t.Fatalf("estado do 2FA: %+v", u.TwoFA())
	}
	h := New(nil, a, true, ai.Config{}, "", nil).Handler()

	// a senha sozinha não entra: vem um bilhete para o código
	rec := postJSON(h, "/api/login", `{"user":"ana","password":"senha-do-env-1"}`)
	var step1 struct {
		Need2FA bool   `json:"need2fa"`
		Ticket  string `json:"ticket"`
	}
	json.NewDecoder(rec.Body).Decode(&step1)
	if rec.Code != 200 || !step1.Need2FA || step1.Ticket == "" || cookieNamed(rec, cookieName) != nil {
		t.Fatalf("login com 2FA deveria pedir o código sem dar sessão: %d %+v", rec.Code, step1)
	}
	if r := postJSON(h, "/api/login/2fa", `{"ticket":"`+step1.Ticket+`","code":"123"}`); r.Code != 401 {
		t.Fatalf("código errado: %d", r.Code)
	}
	if r := postJSON(h, "/api/login/2fa", `{"ticket":"`+step1.Ticket+`x","code":"`+code(t, sec, 1)+`"}`); r.Code != 401 ||
		!strings.Contains(r.Body.String(), "ticket_expired") {
		t.Fatalf("bilhete adulterado: %d %s", r.Code, r.Body)
	}
	// o código usado para ligar não vale de novo; o do próximo intervalo vale
	if r := postJSON(h, "/api/login/2fa", `{"ticket":"`+step1.Ticket+`","code":"`+code(t, sec, 0)+`"}`); r.Code != 401 {
		t.Fatalf("o mesmo código não pode valer duas vezes: %d", r.Code)
	}
	r := postJSON(h, "/api/login/2fa", `{"ticket":"`+step1.Ticket+`","code":"`+code(t, sec, 1)+`","remember":true}`)
	session, trust := cookieNamed(r, cookieName), cookieNamed(r, trustCookie)
	if r.Code != 200 || session == nil || trust == nil {
		t.Fatalf("código certo deveria entrar e lembrar o aparelho: %d %s", r.Code, r.Body)
	}

	// aparelho lembrado: a senha basta por 30 dias
	if r := postJSON(h, "/api/login", `{"user":"ana","password":"senha-do-env-1"}`, trust); cookieNamed(r, cookieName) == nil {
		t.Fatalf("aparelho lembrado deveria entrar direto: %s", r.Body)
	}

	// código de recuperação: entra uma vez e avisa quantos sobram
	rec = postJSON(h, "/api/login", `{"user":"ana","password":"senha-do-env-1"}`)
	json.NewDecoder(rec.Body).Decode(&step1)
	r = postJSON(h, "/api/login/2fa", `{"ticket":"`+step1.Ticket+`","code":"`+strings.ToUpper(recovery[0])+`"}`)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"recoveryUsed":true`) || !strings.Contains(r.Body.String(), `"recoveryLeft":9`) {
		t.Fatalf("código de recuperação: %d %s", r.Code, r.Body)
	}
	if r := postJSON(h, "/api/login/2fa", `{"ticket":"`+step1.Ticket+`","code":"`+recovery[0]+`"}`); r.Code != 401 {
		t.Fatal("código de recuperação é de uso único")
	}
}

func TestDisableTwoFactor(t *testing.T) {
	a, dir := newAuth2FA(t, "chefe")
	sec, _ := enable2FA(t, a, "chefe")
	if err := a.DisableTOTP("chefe", "errada", code(t, sec, 1)); !errors.Is(err, ErrBadCurrent) {
		t.Fatalf("desligar pede a senha: %v", err)
	}
	if err := a.DisableTOTP("chefe", "senha-do-env-1", "000000"); !errors.Is(err, ErrBadCode) && code(t, sec, 1) != "000000" {
		t.Fatalf("desligar pede um código válido: %v", err)
	}
	if err := a.DisableTOTP("chefe", "senha-do-env-1", code(t, sec, 1)); err != nil {
		t.Fatal(err)
	}
	if u, _ := a.Get("chefe"); u.TOTP != nil {
		t.Fatal("deveria estar desligado")
	}

	// o administrador desliga o 2FA de outra pessoa (celular perdido)
	boss, _ := a.Get("chefe")
	_, pass, _ := a.CreateUser(boss, "maria", Perms{})
	a.ChangePassword("maria", pass, "senha-da-maria-1", "")
	enable2FA(t, a, "maria")
	maria, _ := a.Get("maria")
	if err := a.DisableTOTPFor(maria, "chefe"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("só leitura não desliga o 2FA dos outros: %v", err)
	}
	if err := a.DisableTOTPFor(boss, "maria"); err != nil {
		t.Fatal(err)
	}
	if u, _ := a.Get("maria"); u.TOTP != nil {
		t.Fatal("o admin deveria ter desligado o 2FA da maria")
	}

	// reset-password também desliga (quem esqueceu a senha pode ter perdido o celular)
	enable2FA(t, a, "maria")
	if _, err := ResetPasswordOffline(dir, "chefe", "maria"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if u, _ := a.Get("maria"); u.TOTP != nil || !u.MustChange {
		t.Fatal("reset-password deveria desligar o 2FA e pedir senha nova")
	}
}

func TestNewRecoveryCodesNeedCode(t *testing.T) {
	a, _ := newAuth2FA(t, "ana")
	sec, old := enable2FA(t, a, "ana")
	if _, err := a.NewRecoveryCodes("ana", old[0]); !errors.Is(err, ErrBadCode) {
		t.Fatalf("trocar os códigos pede um código do app, não de recuperação: %v", err)
	}
	fresh, err := a.NewRecoveryCodes("ana", code(t, sec, 1))
	if err != nil || len(fresh) != recoveryN {
		t.Fatal(err)
	}
	if _, _, err := a.VerifySecond("ana", old[1]); !errors.Is(err, ErrBadCode) {
		t.Fatal("os códigos antigos deixam de valer")
	}
	if _, err := a.EnableTOTP("ana", code(t, sec, 1)); !errors.Is(err, ErrNoPending) {
		t.Fatalf("sem QR pendente: %v", err)
	}
}

// As rotas da tela: QR, ligar (cookie novo), dispensar a recomendação, desligar e o admin desligando o de outra pessoa.
func TestTwoFactorRoutes(t *testing.T) {
	a, _ := newAuth2FA(t, "chefe")
	h := New(nil, a, true, ai.Config{}, "", nil).Handler()
	ck := sessionCookie(a, "chefe")
	me := func(c *http.Cookie) TwoFAInfo {
		req := httptest.NewRequest("GET", "/api/me", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m struct {
			TwoFA TwoFAInfo `json:"twoFA"`
		}
		json.NewDecoder(rec.Body).Decode(&m)
		return m.TwoFA
	}
	if r := postJSON(h, "/api/2fa/dismiss", `{}`, ck); r.Code != 200 || !me(ck).Asked || me(ck).Enabled {
		t.Fatalf("dispensar a recomendação: %d %+v", r.Code, me(ck))
	}
	r := postJSON(h, "/api/2fa/setup", `{}`, ck)
	var setup struct{ Secret, URI string }
	json.NewDecoder(r.Body).Decode(&setup)
	if r.Code != 200 || !strings.HasPrefix(setup.URI, "otpauth://totp/VPServer:chefe?") || !strings.Contains(setup.Secret, " ") {
		t.Fatalf("setup: %d %+v", r.Code, setup)
	}
	sec := strings.ReplaceAll(setup.Secret, " ", "")
	if r := postJSON(h, "/api/2fa/enable", `{"code":"111111"}`, ck); r.Code != 400 && code(t, sec, 0) != "111111" {
		t.Fatalf("código errado ao ligar: %d", r.Code)
	}
	r = postJSON(h, "/api/2fa/enable", `{"code":"`+code(t, sec, 0)+`"}`, ck)
	fresh := cookieNamed(r, cookieName)
	if r.Code != 200 || fresh == nil || !strings.Contains(r.Body.String(), `"codes":[`) {
		t.Fatalf("ligar: %d %s", r.Code, r.Body)
	}
	if !me(fresh).Enabled || me(fresh).RecoveryLeft != recoveryN {
		t.Fatalf("depois de ligar, o cookie novo vale e o 2FA aparece ligado: %+v", me(fresh))
	}
	if r := postJSON(h, "/api/2fa/setup", `{}`, fresh); r.Code != 400 {
		t.Fatal("já ligado: não gera outro QR")
	}

	// o admin desliga o 2FA de outra pessoa
	boss, _ := a.Get("chefe")
	_, pass, _ := a.CreateUser(boss, "maria", Perms{})
	a.ChangePassword("maria", pass, "senha-da-maria-1", "")
	enable2FA(t, a, "maria")
	if r := postJSON(h, "/api/users/2fa-off", `{"name":"maria"}`, fresh); r.Code != 200 {
		t.Fatalf("admin desligando o 2FA da maria: %d %s", r.Code, r.Body)
	}
	if u, _ := a.Get("maria"); u.TOTP != nil {
		t.Fatal("deveria estar desligado")
	}

	// desligar o próprio: senha e código
	if r := postJSON(h, "/api/2fa/disable", `{"password":"errada","code":"`+code(t, sec, 1)+`"}`, fresh); r.Code != 400 {
		t.Fatalf("senha errada: %d", r.Code)
	}
	r = postJSON(h, "/api/2fa/disable", `{"password":"senha-do-env-1","code":"`+code(t, sec, 1)+`"}`, fresh)
	if r.Code != 200 || me(cookieNamed(r, cookieName)).Enabled {
		t.Fatalf("desligar: %d %s", r.Code, r.Body)
	}
}
