package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/backup"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

type backupSrcStub struct{}

func (backupSrcStub) Containers() []monitor.ContainerInfo { return nil }
func (backupSrcStub) ServerName() string                  { return "srv" }

func TestBackupRoutesAreAdminOnlyAndConfirmed(t *testing.T) {
	a := NewAuth("chefe", "senha-muito-boa", "s", true, t.TempDir(), false)
	boss, _ := a.Get("chefe")
	_, pass, _ := a.CreateUser(boss, "quase", Perms{Actions: true, Manage: true, Clean: true})
	a.ChangePassword("quase", pass, "senha-propria-1", "")
	bk := backup.New(backupSrcStub{}, nil, t.TempDir())
	h := New(nil, a, true, ai.Config{}, "", nil, nil).WithBackup(bk).Handler()
	call := func(user, method, path, body string) (int, string, http.Header) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Requested-With", "vpmon")
		req.AddCookie(sessionCookie(a, user))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), rec.Header()
	}
	// todas as permissões menos administrador: nada
	for _, c := range []struct{ m, p string }{{"GET", "/api/backup"}, {"POST", "/api/backup/key"}, {"POST", "/api/backup/storage"},
		{"POST", "/api/backup/target"}, {"POST", "/api/backup/run"}, {"GET", "/api/backup/objects?id=x"}, {"GET", "/api/backup/download?key=x"}} {
		if code, _, _ := call("quase", c.m, c.p, `{"generate":true,"confirm":true}`); code != http.StatusForbidden {
			t.Errorf("%s %s sem ser administrador: %d", c.m, c.p, code)
		}
	}
	if code, body, _ := call("chefe", "GET", "/api/backup", ""); code != 200 || !strings.Contains(body, `"configured":false`) {
		t.Fatalf("administrador vê: %d %s", code, body)
	}
	code, body, hdr := call("chefe", "POST", "/api/backup/key", `{"generate":true}`)
	if code != 200 || !strings.Contains(body, `"privateKey":"AGE-SECRET-KEY-1`) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("gerar a chave: %d %s", code, body)
	}
	if strings.Contains(func() string { _, b, _ := call("chefe", "GET", "/api/backup", ""); return b }(), "AGE-SECRET-KEY") {
		t.Fatal("a chave privada não volta depois")
	}
	if code, body, _ := call("chefe", "POST", "/api/backup/key", `{"generate":true}`); code != 400 || !strings.Contains(body, "confirm_required") {
		t.Fatalf("trocar a chave sem confirmar: %d %s", code, body)
	}
	if code, _, _ := call("chefe", "POST", "/api/backup/key", `{"generate":true,"confirm":true}`); code != 200 {
		t.Fatalf("trocar confirmando: %d", code)
	}
	if code, body, _ := call("chefe", "POST", "/api/backup/target", `{"id":"loja/db","enabled":true,"every":"1h"}`); code != 400 || !strings.Contains(body, "confirm_required") {
		t.Fatalf("ligar sem confirmar: %d %s", code, body)
	}
	if code, body, _ := call("chefe", "POST", "/api/backup/run", `{"id":"loja/db"}`); code != 400 || !strings.Contains(body, "confirm_required") {
		t.Fatalf("fazer agora sem confirmar: %d %s", code, body)
	}
	if code, body, _ := call("chefe", "GET", "/api/backup/download?key=outro/x.age", ""); code != 400 || !strings.Contains(body, "fora dos backups") {
		t.Fatalf("download fora do prefixo: %d %s", code, body)
	}
	if code, _, _ := call("chefe", "POST", "/api/backup/storage", `{"endpoint":"http://exemplo.com","bucket":"b","accessKey":"a","secretKey":"s"}`); code != 400 {
		t.Fatalf("armazenamento inválido: %d", code)
	}
}
