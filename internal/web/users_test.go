package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
)

func admin(a *Auth, name string) User {
	u, _ := a.Get(name)
	return u
}

// O auth.json de antes dos usuários vira o primeiro administrador, e a sessão
// aberta antes do deploy continua valendo.
func TestMigratesOldLoginAndKeepsSession(t *testing.T) {
	dir := t.TempDir()
	hash := HashPassword("senha-trocada-1")
	b, _ := json.Marshal(storedAuth{User: "ana", Hash: hash, Changed: 100})
	os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600)

	// cookie no formato antigo (vencimento.nonce), assinado com a chave antiga
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	payload := exp + "." + base64.RawURLEncoding.EncodeToString([]byte("nonce-velho!"))
	mac := hmac.New(sha256.New, deriveKey("s", "ana", hash))
	mac.Write([]byte(payload))
	old := &http.Cookie{Name: cookieName, Value: payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}

	a := NewAuth("env-user", "senha-do-env", "s", true, dir, false)
	u, ok := a.Login("ana", "senha-trocada-1")
	if !ok || !u.Admin || canLogin(a, "env-user", "senha-do-env") {
		t.Fatalf("o login migrado deveria virar o administrador: %+v", u)
	}
	if _, err := os.Stat(filepath.Join(dir, "users.json")); err != nil {
		t.Fatal("deveria gravar o users.json")
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); err == nil {
		t.Fatal("o auth.json antigo deveria ser guardado como .migrado")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(old)
	if s, ok := a.Session(req); !ok || s.Name != "ana" {
		t.Fatal("a sessão aberta antes do deploy deveria continuar valendo")
	}
}

// O admin que entra com a senha do .env (sem users.json) também não perde a sessão.
func TestEnvAdminOldSessionStillValid(t *testing.T) {
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	payload := exp + ".bm9uY2U"
	mac := hmac.New(sha256.New, deriveKey("s", "ana", "senha-do-env-1"))
	mac.Write([]byte(payload))
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))})
	a := NewAuth("ana", "senha-do-env-1", "s", true, t.TempDir(), false)
	if u, ok := a.Session(req); !ok || !u.Admin {
		t.Fatal("sessão antiga do admin do .env deveria valer")
	}
	a.MarkLogin("ana") // grava o users.json; a sessão continua (o epoch do admin migrado é vazio)
	if _, ok := a.Session(req); !ok {
		t.Fatal("anotar o login não pode derrubar a sessão")
	}
}

func TestUserPermissionRules(t *testing.T) {
	a := NewAuth("chefe", "senha-do-env-1", "s", true, t.TempDir(), false)
	boss := admin(a, "chefe")

	// admin cria um gerente de usuários (sem ações) e uma pessoa com ações
	mgr, pass, err := a.CreateUser(boss, "gerente", Perms{Manage: true})
	if err != nil || len(pass) != 14 || !mgr.Manage || mgr.Actions || mgr.Admin || !mgr.MustChange {
		t.Fatalf("criar gerente: %+v %q %v", mgr, pass, err)
	}
	if u, ok := a.Login("gerente", pass); !ok || !u.MustChange {
		t.Fatal("a senha provisória vale e obriga a trocar")
	}
	if _, _, err := a.CreateUser(boss, "Gerente", Perms{}); !errors.Is(err, ErrUserExists) {
		t.Fatalf("nome repetido (sem diferenciar maiúscula): %v", err)
	}
	if _, _, err := a.CreateUser(boss, "x", Perms{}); !errors.Is(err, ErrBadUser) {
		t.Fatalf("nome inválido: %v", err)
	}
	g := admin(a, "gerente")

	// o gerente só concede o que tem e não mexe em administradores
	if _, _, err := a.CreateUser(g, "outro-admin", Perms{Admin: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("gerente criando admin: %v", err)
	}
	if _, _, err := a.CreateUser(g, "operador", Perms{Actions: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("gerente sem 'ações' concedendo ações: %v", err)
	}
	if _, _, err := a.CreateUser(g, "leitor", Perms{}); err != nil {
		t.Fatalf("gerente criando leitor: %v", err)
	}
	if err := a.DeleteUser(g, "chefe"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("gerente removendo admin: %v", err)
	}
	if _, err := a.ResetUser(g, "chefe"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("gerente trocando a senha do admin: %v", err)
	}
	leitor := admin(a, "leitor")
	if _, _, err := a.CreateUser(leitor, "mais-um", Perms{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("leitor criando usuário: %v", err)
	}

	// ninguém mexe em si mesmo por aqui; sempre sobra um administrador
	if err := a.DeleteUser(boss, "chefe"); !errors.Is(err, ErrSelf) {
		t.Fatalf("remover a si mesmo: %v", err)
	}
	if _, err := a.UpdateUser(boss, "chefe", Perms{}); !errors.Is(err, ErrSelf) {
		t.Fatalf("tirar o próprio admin: %v", err)
	}

	// mudar permissões e trocar a senha derrubam as sessões da pessoa
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(sessionCookie(a, "leitor"))
	if _, ok := a.Session(req); !ok {
		t.Fatal("sessão do leitor")
	}
	if v, err := a.UpdateUser(boss, "leitor", Perms{Actions: true}); err != nil || !v.Actions {
		t.Fatalf("dar ações ao leitor: %+v %v", v, err)
	}
	if _, ok := a.Session(req); ok {
		t.Fatal("mudar permissões deveria derrubar a sessão")
	}
	newPass, err := a.ResetUser(boss, "leitor")
	if err != nil || !canLogin(a, "leitor", newPass) {
		t.Fatalf("nova senha provisória: %v", err)
	}
	if err := a.DeleteUser(boss, "leitor"); err != nil || canLogin(a, "leitor", newPass) {
		t.Fatalf("remover: %v", err)
	}

	// com dois administradores, um pode rebaixar o outro, mas não o último
	two, _, _ := a.CreateUser(boss, "vice", Perms{Admin: true})
	if _, err := a.UpdateUser(admin(a, two.Name), "chefe", Perms{}); err != nil {
		t.Fatalf("rebaixar com outro admin sobrando: %v", err)
	}
	if err := a.DeleteUser(admin(a, "chefe"), "vice"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ex-admin sem permissão não remove ninguém: %v", err)
	}
	if got := len(a.Users()); got != 3 {
		t.Fatalf("usuários: %d", got)
	}
}

// vpmon reset-password: troca a senha com o painel rodando (ele relê o arquivo).
func TestResetPasswordOffline(t *testing.T) {
	dir := t.TempDir()
	a := NewAuth("ana", "senha-do-env-1", "s", true, dir, false)
	if _, err := ResetPasswordOffline(dir, "ana", "ninguem"); err == nil {
		t.Fatal("usuário inexistente deveria dar erro")
	}
	pass, err := ResetPasswordOffline(dir, "ana", "ana") // ainda sem users.json: parte do admin do .env
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if u, ok := a.Login("ana", pass); !ok || !u.MustChange || !u.Admin {
		t.Fatal("o painel rodando deveria aceitar a senha provisória nova")
	}
	if canLogin(a, "ana", "senha-do-env-1") {
		t.Fatal("a senha antiga não vale mais")
	}
}

// Cada rota respeita a permissão de quem está logado (a tela esconde, a API recusa).
func TestRoutePermissions(t *testing.T) {
	a := NewAuth("chefe", "senha-do-env-1", "s", true, t.TempDir(), false)
	boss := admin(a, "chefe")
	for _, c := range []struct {
		name string
		p    Perms
	}{{"leitor", Perms{}}, {"operador", Perms{Actions: true}}, {"gerente", Perms{Manage: true}}} {
		_, pass, err := a.CreateUser(boss, c.name, c.p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.ChangePassword(c.name, pass, "senha-propria-1", ""); err != nil { // sai da senha provisória
			t.Fatal(err)
		}
	}
	h := New(nil, a, true, ai.Config{}, "", nil).Handler()
	call := func(user, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Requested-With", "vpmon")
		req.AddCookie(sessionCookie(a, user))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	cases := []struct {
		user, method, path, body string
		want                     int
	}{
		{"leitor", "GET", "/api/me", "", 200},
		{"leitor", "POST", "/api/apps/pause", `{}`, 403},
		{"leitor", "GET", "/api/settings", "", 403},
		{"leitor", "GET", "/api/notify", "", 403},
		{"leitor", "GET", "/api/users", "", 403},
		{"operador", "POST", "/api/apps/pause", `{}`, 400}, // passou da permissão (faltou a app)
		{"operador", "GET", "/api/users", "", 403},
		{"gerente", "GET", "/api/users", "", 200},
		{"gerente", "POST", "/api/users", `{"name":"novato"}`, 200},
		{"gerente", "POST", "/api/users", `{"name":"chefao","admin":true}`, 403},
		{"gerente", "POST", "/api/users/delete", `{"name":"chefe"}`, 403},
		{"gerente", "GET", "/api/settings", "", 403},
		{"chefe", "GET", "/api/settings", "", 200},
		{"chefe", "POST", "/api/users/update", `{"name":"leitor","actions":true}`, 200},
		{"chefe", "POST", "/api/users/delete", `{"name":"chefe"}`, 400},
	}
	for _, c := range cases {
		if got := call(c.user, c.method, c.path, c.body); got != c.want {
			t.Errorf("%s %s %s: %d, queria %d", c.user, c.method, c.path, got, c.want)
		}
	}
	// /api/me devolve as permissões para a tela
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(sessionCookie(a, "operador"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var me struct {
		User    string `json:"user"`
		Admin   bool   `json:"admin"`
		Actions bool   `json:"actions"`
		Manage  bool   `json:"manage"`
	}
	json.NewDecoder(rec.Body).Decode(&me)
	if me.User != "operador" || me.Admin || !me.Actions || me.Manage {
		t.Fatalf("me: %+v", me)
	}
}
