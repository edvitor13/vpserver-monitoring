package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/cleanup"
	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
)

// Um central e um servidor conectado de verdade: ver à distância, o que não
// dá para ver, e o controle total com as permissões de quem está no central.
func TestRemoteViewAndControl(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// central
	cdir := t.TempDir()
	cauth := NewAuth("chefe", "senha-muito-boa", "s", true, t.TempDir(), false)
	boss, _ := cauth.Get("chefe")
	_, pass, _ := cauth.CreateUser(boss, "leitor", Perms{})
	cauth.ChangePassword("leitor", pass, "senha-propria-1", "")
	cfl := &fleet.Fleet{Central: fleet.NewCentral(cdir), Client: fleet.NewClient(cdir, "dev", func() fleet.Report { return fleet.Report{} })}
	central := httptest.NewServer(New(nil, cauth, false, ai.Config{}, "", nil, cfl).Handler())
	defer func() { central.CloseClientConnections(); central.Close() }()

	// servidor conectado (a tela dele responde de verdade, com a limpeza de mentira)
	rdir := t.TempDir()
	rauth := NewAuth("dono", "senha-muito-boa", "s2", true, t.TempDir(), false)
	rfl := &fleet.Fleet{Central: fleet.NewCentral(rdir)}
	rfl.Client = fleet.NewClient(rdir, "dev", func() fleet.Report { return fleet.Report{Name: "loja-srv"} })
	remote := New(nil, rauth, false, ai.Config{}, "", nil, rfl).WithCleanup(cleanup.New(pruneStub{}, diskStub{}, t.TempDir(), ""))
	rfl.Client.SetLocal(remote.LocalView)
	go rfl.Client.RunViews(ctx)

	v, plain, _ := cfl.Central.Create("loja", "chefe", false)
	if _, err := rfl.Client.Connect(ctx, central.URL, plain); err != nil {
		t.Fatal(err)
	}
	call := func(user, method, path, body string, headers ...string) (int, string) {
		req, _ := http.NewRequest(method, central.URL+path, strings.NewReader(body))
		if len(headers) == 0 {
			req.Header.Set("X-Requested-With", "vpmon")
		}
		req.AddCookie(sessionCookie(cauth, user))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	waitFor := func(what string, ok func(fleet.TokenView) bool) {
		t.Helper()
		for i := 0; i < 100; i++ {
			for _, tv := range cfl.Central.List() {
				if tv.ID == v.ID && ok(tv) {
					return
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("o central não ficou sabendo: %s", what)
	}
	view := "/api/fleet/view/" + v.ID

	if code, _ := call("chefe", "GET", view+"/api/cleanup", ""); code != http.StatusForbidden {
		t.Fatalf("sem compartilhar, nada: %d", code)
	}

	// só ver
	rfl.Client.SetShare(fleet.Share{View: true})
	waitFor("ver", func(tv fleet.TokenView) bool { return tv.Viewable && !tv.Control })
	code, body := call("leitor", "GET", view+"/api/cleanup", "")
	if code != 200 || !strings.Contains(body, `"canClean":false`) || !strings.Contains(body, `"disk"`) {
		t.Fatalf("ver a limpeza do outro: %d %s", code, body)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/api/logs"}, {"GET", "/api/users"}, {"GET", "/api/settings"},
		{"POST", "/api/cleanup/run"}, {"POST", "/api/apps/pause"}, {"POST", "/api/users"}} {
		if code, _ := call("chefe", c.method, view+c.path, `{"buildCache":true,"confirm":true}`); code != http.StatusForbidden {
			t.Errorf("%s %s com só ver: %d", c.method, c.path, code)
		}
	}

	// controle total: valem as permissões de quem está no central
	rfl.Client.SetShare(fleet.Share{View: true, Control: true})
	waitFor("controle total", func(tv fleet.TokenView) bool { return tv.Viewable && tv.Control })
	if code, body := call("chefe", "GET", view+"/api/cleanup", ""); code != 200 || !strings.Contains(body, `"canClean":true`) {
		t.Fatalf("o administrador do central pode limpar lá: %d %s", code, body)
	}
	if code, body := call("leitor", "POST", view+"/api/cleanup/run", `{"buildCache":true,"confirm":true}`); code != http.StatusForbidden || !strings.Contains(body, "painel central") {
		t.Fatalf("leitor do central não limpa lá: %d %s", code, body)
	}
	if code, _ := call("chefe", "POST", view+"/api/cleanup/run", `{"buildCache":true,"confirm":true}`, "sem-cabecalho"); code != http.StatusForbidden {
		t.Fatalf("POST sem a origem conferida: %d", code)
	}
	if code, body := call("chefe", "POST", view+"/api/cleanup/run", `{"buildCache":true,"confirm":true}`); code != http.StatusAccepted {
		t.Fatalf("administrador do central limpa lá: %d %s", code, body)
	}
	if code, _ := call("chefe", "POST", view+"/api/users", `{"name":"intruso","admin":true}`); code != http.StatusForbidden {
		t.Fatalf("usuários nunca à distância: %d", code)
	}
	if code, _ := call("chefe", "GET", view+"/api/logs/targets", ""); code == http.StatusForbidden {
		t.Fatal("controle total inclui ler os logs")
	}

	// desligar vale na hora
	rfl.Client.SetShare(fleet.Share{})
	waitFor("desligado", func(tv fleet.TokenView) bool { return !tv.Viewable })
	if code, _ := call("chefe", "GET", view+"/api/cleanup", ""); code != http.StatusForbidden {
		t.Fatalf("desligado: %d", code)
	}
}
