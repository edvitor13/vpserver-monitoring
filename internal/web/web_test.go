package web

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
)

func TestAuthCookieRoundTrip(t *testing.T) {
	a := NewAuth("ana", "senha-muito-boa", "segredo", true, "", false)
	if !a.Check("ana", "senha-muito-boa") || a.Check("ana", "errada") || a.Check("outro", "senha-muito-boa") {
		t.Fatal("Check errado")
	}
	rec := httptest.NewRecorder()
	a.Issue(rec)
	ck := rec.Result().Cookies()[0]
	if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie sem as proteções: %+v", ck)
	}
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(ck)
	if !a.Valid(req) {
		t.Fatal("cookie recém-emitido deveria valer")
	}
	// trocar a senha derruba as sessões
	if NewAuth("ana", "outra-senha-boa", "segredo", true, "", false).Valid(req) {
		t.Fatal("cookie deveria cair ao trocar a senha")
	}
	bad := httptest.NewRequest("GET", "/api/me", nil)
	bad.AddCookie(&http.Cookie{Name: cookieName, Value: ck.Value[:len(ck.Value)-2] + "xx"})
	if a.Valid(bad) {
		t.Fatal("cookie adulterado passou")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := NewAuth("u", "senha-muito-boa", "s", true, "", false)
	for i := 0; i < maxFailIP; i++ {
		if blocked, _ := a.Blocked("1.2.3.4"); blocked {
			t.Fatalf("bloqueou cedo demais (%d)", i)
		}
		a.Fail("1.2.3.4")
	}
	if blocked, _ := a.Blocked("1.2.3.4"); !blocked {
		t.Fatal("deveria bloquear depois de muitos erros")
	}
	if blocked, _ := a.Blocked("5.6.7.8"); blocked {
		t.Fatal("outro IP não deveria ser bloqueado")
	}
}

func TestPrivateRoutesNeedLoginAndHeaders(t *testing.T) {
	h := New(nil, NewAuth("u", "senha-muito-boa", "s", true, "", false), true, ai.Config{}, "", nil).Handler()
	for _, p := range []string{"/api/overview", "/api/system", "/api/logs?c=*", "/api/traffic", "/api/history/host"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s sem login devolveu %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "VPServer") {
		t.Fatalf("index: %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("cabeçalhos de segurança faltando: %v", rec.Header())
	}
	// login sem o cabeçalho X-Requested-With é recusado (CSRF)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"u","password":"senha-muito-boa"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login sem cabeçalho: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"u","password":"senha-muito-boa"}`))
	req.Header.Set("X-Requested-With", "vpmon")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("login certo falhou: %d %s", rec.Code, rec.Body)
	}
}

func TestPasswordHashAndChange(t *testing.T) {
	h := HashPassword("uma-senha-nova")
	if !VerifyPassword(h, "uma-senha-nova") || VerifyPassword(h, "outra") || VerifyPassword("lixo", "x") {
		t.Fatal("hash/verify errado")
	}
	store := filepath.Join(t.TempDir(), "auth.json")
	a := NewAuth("ana", "senha-do-env-1", "s", true, store, false)
	rec := httptest.NewRecorder()
	a.Issue(rec)
	old := httptest.NewRequest("GET", "/", nil)
	old.AddCookie(rec.Result().Cookies()[0])

	if err := a.ChangePassword("errada", "senha-nova-boa", ""); err != ErrBadCurrent {
		t.Fatalf("atual errada: %v", err)
	}
	if err := a.ChangePassword("senha-do-env-1", "curta", ""); err != ErrWeak {
		t.Fatalf("fraca: %v", err)
	}
	if err := a.ChangePassword("senha-do-env-1", "senha-nova-boa", ""); err != nil {
		t.Fatal(err)
	}
	if a.Check("ana", "senha-do-env-1") || !a.Check("ana", "senha-nova-boa") {
		t.Fatal("depois da troca só a nova deveria valer")
	}
	if a.Valid(old) {
		t.Fatal("sessões antigas deveriam cair")
	}
	// reiniciando, a senha trocada continua valendo (vale mais que o .env)
	b := NewAuth("ana", "senha-do-env-1", "s", true, store, false)
	if !b.Check("ana", "senha-nova-boa") || b.Check("ana", "senha-do-env-1") {
		t.Fatal("a senha gravada deveria sobreviver ao reinício")
	}
	if src, _ := b.PasswordInfo(); src != "panel" {
		t.Fatalf("origem: %s", src)
	}
}

func TestGzip(t *testing.T) {
	h := New(nil, NewAuth("u", "senha-muito-boa", "s", true, "", false), true, ai.Config{}, "", nil).Handler()
	req := httptest.NewRequest("GET", "/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("app.js sem gzip: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if !strings.Contains(string(b), "VPServer") || len(b) <= rec.Body.Len() {
		t.Fatalf("conteúdo descomprimido estranho (%d bytes)", len(b))
	}
	// sem Accept-Encoding: vai cru
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/app.js", nil))
	if rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), "VPServer") {
		t.Fatal("sem gzip deveria ir cru")
	}
}

func TestDefaultLoginForcesChange(t *testing.T) {
	store := filepath.Join(t.TempDir(), "auth.json")
	a := NewAuth("", "", "s", true, store, false) // nada configurado: admin/admin
	if !a.Check("admin", "admin") || !a.MustChange() {
		t.Fatal("sem senha configurada, deveria valer admin/admin com troca obrigatória")
	}
	h := New(nil, a, true, ai.Config{}, "", nil).Handler()
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"admin","password":"admin"}`))
	req.Header.Set("X-Requested-With", "vpmon")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mustChange":true`) {
		t.Fatalf("login padrão: %d %s", rec.Code, rec.Body)
	}
	ck := rec.Result().Cookies()[0]
	// com a senha inicial, os dados ficam bloqueados
	r2 := httptest.NewRequest("GET", "/api/overview", nil)
	r2.AddCookie(ck)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r2)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "password_change_required") {
		t.Fatalf("deveria bloquear até trocar: %d %s", rec.Code, rec.Body)
	}
	// troca senha e usuário
	if err := a.ChangePassword("admin", "uma-senha-forte-1", "x"); err != ErrBadUser {
		t.Fatalf("usuário curto: %v", err)
	}
	if err := a.ChangePassword("admin", "uma-senha-forte-1", "maria"); err != nil {
		t.Fatal(err)
	}
	if a.MustChange() || a.Check("admin", "admin") || !a.Check("maria", "uma-senha-forte-1") {
		t.Fatal("depois da troca vale só o novo usuário e a nova senha")
	}
	// reinício: continua valendo o que foi gravado, mesmo com o .env vazio
	b := NewAuth("", "", "s", true, store, false)
	if b.MustChange() || !b.Check("maria", "uma-senha-forte-1") || b.Check("admin", "admin") {
		t.Fatal("o login trocado deveria sobreviver ao reinício")
	}
}

func TestEnvPasswordNoForcedChange(t *testing.T) {
	a := NewAuth("ana", "senha-do-env-ok", "s", true, filepath.Join(t.TempDir(), "auth.json"), false)
	if a.MustChange() || !a.Check("ana", "senha-do-env-ok") {
		t.Fatal("com senha no .env (como no servidor do autor) não há troca obrigatória")
	}
	f := NewAuth("admin", "inicial-gerada", "s", true, filepath.Join(t.TempDir(), "auth.json"), true)
	if !f.MustChange() {
		t.Fatal("com VPMON_FORCE_PASSWORD_CHANGE=true a senha do instalador deve ser trocada")
	}
}

// IA pela tela: testa a chave antes de salvar, mascara na resposta e vale mais que o .env.
func TestAISettings(t *testing.T) {
	good := "sk-chave-boa-1234567890"
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	defer ds.Close()
	env := ai.Config{Host: ds.URL, Endpoint: "/v1/chat/completions", Model: "deepseek-chat"}
	path := filepath.Join(t.TempDir(), "settings.json")
	auth := NewAuth("u", "senha-muito-boa", "s", true, "", false)
	h := New(nil, auth, true, env, path, nil).Handler()
	rec := httptest.NewRecorder()
	auth.Issue(rec)
	ck := rec.Result().Cookies()[0]
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/settings/ai", strings.NewReader(body))
		req.Header.Set("X-Requested-With", "vpmon")
		req.AddCookie(ck)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if r := post(`{"action":"save","key":"sk-errada-000000000"}`); r.Code != 400 || !strings.Contains(r.Body.String(), "recusou a chave") {
		t.Fatalf("chave errada deveria ser recusada: %d %s", r.Code, r.Body)
	}
	r := post(`{"action":"save","key":"` + good + `","model":"deepseek-reasoner"}`)
	if r.Code != 200 || strings.Contains(r.Body.String(), good) {
		t.Fatalf("salvar: %d %s (a chave não pode voltar inteira)", r.Code, r.Body)
	}
	var resp struct {
		AI aiView `json:"ai"`
	}
	json.Unmarshal(r.Body.Bytes(), &resp)
	if !resp.AI.Enabled || resp.AI.Source != "panel" || resp.AI.Model != "deepseek-reasoner" || resp.AI.Key != "sk-…7890" {
		t.Fatalf("visão: %+v", resp.AI)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), good) {
		t.Fatal("a chave deveria estar no settings.json do servidor")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Fatalf("settings.json aberto demais: %v", fi.Mode())
	}
	if r := post(`{"action":"remove"}`); r.Code != 200 || strings.Contains(r.Body.String(), `"enabled":true`) {
		t.Fatalf("remover (sem chave no .env, a IA desliga): %d %s", r.Code, r.Body)
	}
}
