package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
	"github.com/edvitor13/vpserver-monitoring/internal/sshchat"
	"github.com/edvitor13/vpserver-monitoring/internal/sshchat/sshtest"
	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/ssh"
)

func TestSSHNeedsAdmin2FAAndCode(t *testing.T) {
	a, _ := newAuth2FA(t, "chefe")
	now := clock
	a.now = func() time.Time { return now }
	boss, _ := a.Get("chefe")
	_, pass, _ := a.CreateUser(boss, "quase", Perms{Actions: true, Manage: true, Clean: true})
	a.ChangePassword("quase", pass, "senha-propria-1", "")

	srv := sshtest.New(t)
	sc, err := sshchat.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.SetTarget("tester", "127.0.0.1", srv.Port()); err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(sc.View().PublicKey))
	srv.Allow(pub)
	h := New(nil, a, true, ai.Config{}, "", nil, nil).WithSSH(sc).Handler()

	cookies := map[string]*http.Cookie{"chefe": sessionCookie(a, "chefe"), "quase": sessionCookie(a, "quase")}
	call := func(c *http.Cookie, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Requested-With", "vpmon")
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var j map[string]any
		json.Unmarshal(rec.Body.Bytes(), &j)
		return rec.Code, j
	}
	errCode := func(j map[string]any) string {
		e, _ := j["error"].(map[string]any)
		s, _ := e["code"].(string)
		return s
	}
	routes := []struct{ m, p string }{{"GET", "/api/ssh"}, {"GET", "/api/ssh/probe"}, {"POST", "/api/ssh/target"}, {"POST", "/api/ssh/newkey"},
		{"POST", "/api/ssh/enable"}, {"POST", "/api/ssh/disable"}, {"POST", "/api/ssh/unlock"}, {"POST", "/api/ssh/open"}, {"POST", "/api/ssh/lock"},
		{"POST", "/api/ssh/run"}, {"POST", "/api/ssh/input"}, {"GET", "/api/ssh/poll"}, {"GET", "/api/ssh/complete?w=x"}, {"GET", "/api/ssh/log"},
		{"POST", "/api/ssh/ai"}}

	// todas as permissões menos administrador: nada
	for _, r := range routes {
		if code, _ := call(cookies["quase"], r.m, r.p, `{"confirm":true,"cmd":"id"}`); code != http.StatusForbidden {
			t.Errorf("%s %s sem ser administrador: %d", r.m, r.p, code)
		}
	}
	// nada disso vai à distância, nem com controle total
	for _, r := range routes {
		if fleet.RemoteAllowed(r.m, strings.Split(r.p, "?")[0], fleet.Share{View: true, Logs: true, Control: true}) {
			t.Errorf("%s %s não pode ir pelo painel central", r.m, r.p)
		}
	}

	// administrador sem 2FA: vê a tela, mas não liga nem abre
	if code, j := call(cookies["chefe"], "GET", "/api/ssh", ""); code != 200 || j["twoFA"] != false || j["unlocked"] != nil {
		t.Fatalf("sem 2FA: %d %v", code, j)
	}
	for _, p := range []string{"/api/ssh/enable", "/api/ssh/unlock", "/api/ssh/run"} {
		if code, j := call(cookies["chefe"], "POST", p, `{"code":"123456"}`); code != 403 || errCode(j) != "ssh_2fa_required" {
			t.Fatalf("%s sem 2FA: %d %v", p, code, j)
		}
	}

	secret, recovery := enable2FA(t, a, "chefe")
	raw, _ := b32.DecodeString(secret)
	codeNow := func() string { return totpCode(raw, now.Unix()/totpPeriod) }
	cookies["chefe"] = sessionCookie(a, "chefe") // ligar o 2FA derruba as sessões antigas

	if code, j := call(cookies["chefe"], "POST", "/api/ssh/run", `{"cmd":"id"}`); code != 409 || errCode(j) != "ssh_disabled" {
		t.Fatalf("desligado: %d %v", code, j)
	}
	if code, j := call(cookies["chefe"], "GET", "/api/ssh/probe?login=1", ""); code != 200 || !strings.Contains(toJSON(j), `"authorized":true`) {
		t.Fatalf("verificação: %d %v", code, j)
	}
	// o código usado para ligar o 2FA não vale de novo; código de recuperação não abre SSH
	if code, j := call(cookies["chefe"], "POST", "/api/ssh/enable", `{"code":"`+codeNow()+`"}`); code != 400 || errCode(j) != "bad_code" {
		t.Fatalf("código repetido: %d %v", code, j)
	}
	if code, _ := call(cookies["chefe"], "POST", "/api/ssh/enable", `{"code":"`+recovery[0]+`"}`); code != 400 || sc.Enabled() {
		t.Fatalf("código de recuperação não liga o SSH: %d", code)
	}
	now = now.Add(30 * time.Second)
	code, j := call(cookies["chefe"], "POST", "/api/ssh/enable", `{"code":"`+codeNow()+`"}`)
	if code != 200 || !sc.Enabled() || !strings.Contains(toJSON(j), `"ready":true`) {
		t.Fatalf("ligar: %d %v", code, j)
	}
	// ligar já abre a sessão neste navegador
	if code, j := call(cookies["chefe"], "GET", "/api/ssh", ""); code != 200 || j["unlocked"] != true {
		t.Fatalf("sessão aberta: %d %v", code, j)
	}
	if code, j := call(cookies["chefe"], "POST", "/api/ssh/run", `{"cmd":"echo oi"}`); code != 200 || j["block"] != float64(1) {
		t.Fatalf("rodar: %d %v", code, j)
	}
	var out string
	for i := 0; i < 50 && !strings.Contains(out, "oi"); i++ {
		_, j := call(cookies["chefe"], "GET", "/api/ssh/poll?v=-1", "")
		out = toJSON(j)
	}
	if !strings.Contains(out, `"out":"oi\r\n"`) || !strings.Contains(out, `"cmd":"echo oi"`) {
		t.Fatalf("poll: %s", out)
	}
	if code, j := call(cookies["chefe"], "GET", "/api/ssh/complete?w=dock&m=cmd", ""); code != 200 || !strings.Contains(toJSON(j), "docker-compose") {
		t.Fatalf("completar: %d %v", code, j)
	}
	if code, j := call(cookies["chefe"], "GET", "/api/ssh/log", ""); code != 200 || !strings.Contains(toJSON(j), `"text":"echo oi"`) {
		t.Fatalf("registro: %d %v", code, j)
	}

	// outro navegador da mesma pessoa: precisa do código
	other := sessionCookie(a, "chefe")
	if code, j := call(other, "POST", "/api/ssh/run", `{"cmd":"echo x"}`); code != 403 || errCode(j) != "ssh_locked" {
		t.Fatalf("outro navegador sem o código: %d %v", code, j)
	}
	if code, _ := call(other, "POST", "/api/ssh/unlock", `{"code":"000000"}`); code != 400 {
		t.Fatalf("código errado: %d", code)
	}
	now = now.Add(30 * time.Second)
	if code, j := call(other, "POST", "/api/ssh/unlock", `{"code":"`+codeNow()+`"}`); code != 200 {
		t.Fatalf("abrir com o código: %d %v", code, j)
	}
	if code, _ := call(other, "POST", "/api/ssh/run", `{"cmd":"echo y"}`); code != 200 {
		t.Fatalf("rodar depois do código: %d", code)
	}

	// Encerrar fecha o shell e pede o código de novo neste navegador
	if code, _ := call(other, "POST", "/api/ssh/lock", `{}`); code != 200 || sc.Shell("chefe") != nil {
		t.Fatal("encerrar")
	}
	if code, j := call(other, "POST", "/api/ssh/run", `{"cmd":"echo z"}`); code != 403 || errCode(j) != "ssh_locked" {
		t.Fatalf("depois de encerrar: %d %v", code, j)
	}
	// no primeiro navegador a sessão segue liberada: reabre o shell sem código
	if code, _ := call(cookies["chefe"], "POST", "/api/ssh/run", `{"cmd":"echo z"}`); code != 409 {
		t.Fatalf("sem shell: %d", code)
	}
	if code, _ := call(cookies["chefe"], "POST", "/api/ssh/open", `{}`); code != 200 || sc.Shell("chefe") == nil {
		t.Fatal("reabrir sem código")
	}

	// ligado: não muda alvo nem chave; desligar pede confirmação e fecha tudo
	if code, _ := call(cookies["chefe"], "POST", "/api/ssh/newkey", `{"confirm":true}`); code != 409 {
		t.Fatalf("trocar a chave ligado: %d", code)
	}
	if code, j := call(cookies["chefe"], "POST", "/api/ssh/disable", `{}`); code != 400 || errCode(j) != "confirm_required" {
		t.Fatalf("desligar sem confirmar: %d %v", code, j)
	}
	if code, _ := call(cookies["chefe"], "POST", "/api/ssh/disable", `{"confirm":true}`); code != 200 || sc.Enabled() || sc.Shell("chefe") != nil {
		t.Fatal("desligar")
	}
	if code, j := call(cookies["chefe"], "GET", "/api/ssh", ""); code != 200 || j["unlocked"] != nil {
		t.Fatalf("desligado não fica liberado: %v", j)
	}
	// religar pede o código de novo
	if code, _ := call(cookies["chefe"], "POST", "/api/ssh/enable", `{"code":"`+codeNow()+`"}`); code != 400 {
		t.Fatalf("religar com código já usado: %d", code)
	}

	// pedido de outro site
	req := httptest.NewRequest("POST", "/api/ssh/run", strings.NewReader(`{"cmd":"id"}`))
	req.AddCookie(cookies["chefe"])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("sem X-Requested-With: %d", rec.Code)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
