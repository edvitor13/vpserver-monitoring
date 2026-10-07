package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
	"github.com/edvitor13/vpserver-monitoring/internal/notify"
)

type srcStub struct{ name string }

func (s srcStub) Overview() monitor.Overview {
	var o monitor.Overview
	o.Server.Name = s.name
	return o
}
func (srcStub) Summary(string, time.Time) string          { return "" }
func (srcStub) PeriodOf(string, time.Time) monitor.Period { return monitor.Period{} }
func (srcStub) AIContext() string                         { return "" }
func (srcStub) AI() ai.Executor                           { return nil }

type waStub struct {
	mu        sync.Mutex
	installed bool
	sent      []string
}

func (w *waStub) Configured() bool { return w.installed }
func (w *waStub) Status(context.Context) notify.Status {
	return notify.Status{Service: true, Exists: true, State: "open"}
}
func (w *waStub) Connect(context.Context, string) (notify.QR, error) { return notify.QR{}, nil }
func (w *waStub) Logout(context.Context) error                       { return nil }
func (w *waStub) Groups(context.Context) ([]notify.Group, error)     { return nil, nil }
func (w *waStub) Send(_ context.Context, _, text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, text)
	return nil
}

// Um painel central de verdade (handlers e notify) e um servidor conectado a ele.
func TestFleetCentralAndConnectedServer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	wa := &waStub{installed: true}
	nt := notify.New(srcStub{"central-srv"}, wa, time.UTC, dir, "self")
	if _, err := nt.SaveConfig(notify.Config{Enabled: true, Recipients: []notify.Recipient{{ID: "5511900000000", Name: "eu"}},
		Events: notify.DefaultEvents(), DailyAt: "08:00", QuietFrom: "22:00", QuietTo: "07:00"}); err != nil {
		t.Fatal(err)
	}
	nt.Tick(ctx) // lê o estado do WhatsApp
	fl := &fleet.Fleet{Central: fleet.NewCentral(dir), Client: fleet.NewClient(dir, "dev", func() fleet.Report { return fleet.Report{} })}
	auth := NewAuth("chefe", "senha-muito-boa", "s", true, t.TempDir(), false)
	central := httptest.NewServer(New(nil, auth, false, ai.Config{}, "", nt, fl).Handler())
	defer central.Close()

	// sem token/sessão: nada
	for _, c := range []struct{ method, path string }{{"POST", "/api/fleet/report"}, {"POST", "/api/fleet/notify"}, {"GET", "/api/fleet/servers"}, {"POST", "/api/fleet/tokens"}} {
		req, _ := http.NewRequest(c.method, central.URL+c.path, strings.NewReader("{}"))
		req.Header.Set("X-Requested-With", "vpmon")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s sem credencial: %d", c.method, c.path, resp.StatusCode)
		}
	}

	remoteDir := t.TempDir()
	remote := fleet.NewClient(remoteDir, "dev", func() fleet.Report {
		return fleet.Report{Name: "loja-srv", PanelURL: "https://loja.exemplo.com", CPU: 12.5, AppsUp: 3, AppsTotal: 4}
	})
	if _, err := remote.Connect(ctx, central.URL, "vps_errado"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("token errado: %v", err)
	}

	// token sem WhatsApp: conecta e manda o resumo, mas não usa o WhatsApp do central
	_, plain, _ := fl.Central.Create("loja", "chefe", false)
	st, err := remote.Connect(ctx, central.URL, plain)
	if err != nil || !st.OK || st.Central != "VPServer" || st.CanWhatsApp {
		t.Fatalf("conectar: %+v %v", st, err)
	}
	got := fl.Central.List()[0]
	if !got.Online || got.Report == nil || got.Report.Name != "loja-srv" || got.Report.AppsUp != 3 {
		t.Fatalf("o central guarda o resumo: %+v", got)
	}
	if _, err := remote.SetUseWhatsApp(true); err == nil {
		t.Fatal("token sem WhatsApp não pode ligar o emprestado")
	}
	if err := remote.Send(ctx, "oi"); err == nil || !strings.Contains(err.Error(), "não pode usar") {
		t.Fatalf("o central recusa aviso de token sem WhatsApp: %v", err)
	}

	// token com WhatsApp: os avisos do servidor conectado saem pelo central
	v2, plain2, _ := fl.Central.Create("blog", "chefe", true)
	st, err = remote.Connect(ctx, central.URL, plain2)
	if err != nil || !st.CanWhatsApp || !st.CentralWhatsApp {
		t.Fatalf("token com WhatsApp: %+v %v", st, err)
	}
	if _, err := remote.SetUseWhatsApp(true); err != nil || !remote.Active() {
		t.Fatalf("ligar o WhatsApp do central: %v", err)
	}
	local := notify.New(srcStub{"blog-srv"}, &waStub{}, time.UTC, remoteDir, "self")
	local.SetRelay(remote)
	if _, err := local.SendNow(ctx, "test"); err != nil {
		t.Fatalf("teste pelo central: %v", err)
	}
	if len(wa.sent) != 1 || !strings.Contains(wa.sent[0], "blog-srv") || !strings.Contains(wa.sent[0], "Servidor conectado: blog") {
		t.Fatalf("o WhatsApp do central mandou o aviso do blog: %v", wa.sent)
	}

	// desconectar de propósito: o central não acusa silêncio
	if err := remote.Disconnect(ctx); err != nil || remote.Status().Connected || remote.Active() {
		t.Fatal("desconectar esquece o central")
	}
	for _, tv := range fl.Central.List() {
		if tv.ID == v2.ID && (tv.LastSeen != 0 || tv.Report != nil) {
			t.Fatalf("o central soube da desconexão: %+v", tv)
		}
	}
	if _, err := remote.Connect(ctx, central.URL, plain2); err != nil {
		t.Fatal(err)
	}

	// token revogado: o servidor conectado vê o erro
	fl.Central.Revoke(v2.ID)
	remote.Report(ctx)
	if st := remote.Status(); st.OK || !strings.Contains(st.Error, "token") {
		t.Fatalf("depois de revogar: %+v", st)
	}
}
