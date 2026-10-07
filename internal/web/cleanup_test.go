package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/cleanup"
	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

type pruneStub struct{}

func (pruneStub) PruneBuildCache(context.Context) (docker.Pruned, error) {
	return docker.Pruned{Freed: 10}, nil
}
func (pruneStub) PruneDanglingImages(context.Context) (docker.Pruned, error) {
	return docker.Pruned{}, nil
}

type diskStub struct{}

func (diskStub) DiskSnapshot() monitor.DiskSnapshot {
	return monitor.DiskSnapshot{FSUsed: 50, FSTotal: 100, BuildCache: 10, Logs: []monitor.LogFile{}}
}
func (diskStub) RefreshDisk(context.Context) {}
func (diskStub) RefreshStorage()             {}

func TestCleanupPermissions(t *testing.T) {
	a := NewAuth("chefe", "senha-muito-boa", "s", true, t.TempDir(), false)
	boss, _ := a.Get("chefe")
	for _, c := range []struct {
		name string
		p    Perms
	}{{"leitor", Perms{}}, {"faxina", Perms{Clean: true}}, {"gerente", Perms{Manage: true}}} {
		_, pass, err := a.CreateUser(boss, c.name, c.p)
		if err != nil {
			t.Fatal(err)
		}
		a.ChangePassword(c.name, pass, "senha-propria-1", "")
	}
	gerente, _ := a.Get("gerente")
	if _, _, err := a.CreateUser(gerente, "outro", Perms{Clean: true}); err == nil {
		t.Fatal("quem não pode limpar não concede a permissão de limpar")
	}
	cl := cleanup.New(pruneStub{}, diskStub{}, t.TempDir(), "")
	h := New(nil, a, true, ai.Config{}, "", nil, nil).WithCleanup(cl).Handler()
	call := func(user, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Requested-With", "vpmon")
		req.AddCookie(sessionCookie(a, user))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	run := `{"buildCache":true,"confirm":true}`
	auto := `{"enabled":true,"threshold":85,"buildCache":true,"logsOverMb":100}`
	cases := []struct {
		user, method, path, body string
		want                     int
	}{
		{"leitor", "GET", "/api/cleanup", "", 200},
		{"leitor", "POST", "/api/cleanup/run", run, 403},
		{"leitor", "POST", "/api/cleanup/auto", auto, 403},
		{"faxina", "POST", "/api/cleanup/run", `{"buildCache":true}`, 400}, // sem a confirmação
		{"faxina", "POST", "/api/cleanup/run", `{"confirm":true}`, 400},    // nada escolhido
		{"faxina", "POST", "/api/cleanup/auto", `{"enabled":true,"threshold":20,"buildCache":true,"logsOverMb":100}`, 400},
		{"faxina", "POST", "/api/cleanup/auto", auto, 200},
		{"faxina", "POST", "/api/cleanup/run", run, 202},
	}
	for _, c := range cases {
		if got, body := call(c.user, c.method, c.path, c.body); got != c.want {
			t.Errorf("%s %s %s: %d, queria %d (%s)", c.user, c.method, c.path, got, c.want, body)
		}
	}
	for _, u := range []struct {
		name string
		can  bool
	}{{"leitor", false}, {"faxina", true}, {"chefe", true}} {
		_, body := call(u.name, "GET", "/api/cleanup", "")
		var v struct {
			CanClean bool `json:"canClean"`
		}
		json.Unmarshal([]byte(body), &v)
		if v.CanClean != u.can {
			t.Errorf("%s: canClean=%v", u.name, v.CanClean)
		}
	}
}
