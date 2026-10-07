package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

// fakeRelay faz o papel do painel central visto por um servidor conectado.
type fakeRelay struct {
	mu     sync.Mutex
	active bool
	fail   error
	sent   []string
}

func (r *fakeRelay) Active() bool    { r.mu.Lock(); defer r.mu.Unlock(); return r.active }
func (r *fakeRelay) Central() string { return "central" }
func (r *fakeRelay) Send(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, text)
	return nil
}

// noWA: servidor conectado sem o WhatsApp próprio instalado.
type noWA struct{ fakeWA }

func (*noWA) Configured() bool { return false }

func TestConnectedServerSendsThroughCentral(t *testing.T) {
	s, src, _, c := setup(t, "2026-10-06 14:00")
	s.wa = &noWA{}
	s.cfg.Recipients = nil // os destinos são os do central
	r := &fakeRelay{active: true}
	s.SetRelay(r)
	ctx := context.Background()

	src.set([]monitor.Alert{down})
	s.Tick(ctx)
	c.add(3 * time.Minute)
	s.Tick(ctx)
	if len(r.sent) != 1 || !strings.Contains(r.sent[0], "api está unhealthy") {
		t.Fatalf("o alerta deveria sair pelo central: %v", r.sent)
	}
	if a := s.Alerts(); len(a) != 0 {
		t.Fatalf("sem WhatsApp próprio, mas com o do central: nada a avisar em Infos (%+v)", a)
	}
	if v := s.View(ctx); v.Relay != "central" {
		t.Fatalf("a tela deveria dizer que sai pelo central: %+v", v.Relay)
	}
	if msg, err := s.SendNow(ctx, "test"); err != nil || len(r.sent) != 2 || !strings.Contains(r.sent[1], "Teste") {
		t.Fatalf("teste pelo central: %q %v %v", msg, err, r.sent)
	}

	r.fail = errors.New("fora do ar")
	src.set([]monitor.Alert{down, {Key: "app.unhealthy:web", Level: "crit", Area: "app", Title: "web está unhealthy"}})
	s.Tick(ctx)
	c.add(3 * time.Minute)
	s.Tick(ctx)
	if n := len(s.st.Log); n == 0 || s.st.Log[n-1].Status != "failed" || !strings.Contains(s.st.Log[n-1].Error, "painel central") {
		t.Fatalf("falha no central fica no registro: %+v", s.st.Log)
	}

	r.active = false // central desligou o WhatsApp emprestado: volta ao daqui (que não existe)
	if got := s.blocked(false); got == "" {
		t.Fatal("sem central e sem WhatsApp daqui, não há como enviar")
	}
}

func TestCentralRelaysOnlyToItsRecipients(t *testing.T) {
	s, _, wa, _ := setup(t, "2026-10-06 14:00")
	ctx := context.Background()
	s.Tick(ctx) // lê o estado do WhatsApp
	if !s.RelayReady() {
		t.Fatal("conectado, ligado e com destino: pronto para emprestar")
	}
	if err := s.Relay(ctx, "loja", "🔴 *disco cheio · loja-srv*"); err != nil {
		t.Fatal(err)
	}
	got := wa.take()
	if len(got) != 1 || !strings.Contains(got[0], "disco cheio") || !strings.HasSuffix(got[0], "_Servidor conectado: loja_") {
		t.Fatalf("aviso emprestado com a origem no fim: %v", got)
	}
	if e := s.st.Log[len(s.st.Log)-1]; e.Kind != "relay" || e.Status != "sent" {
		t.Fatalf("registro do aviso emprestado: %+v", e)
	}
	if err := s.Relay(ctx, "loja", strings.Repeat("x", 5000)); err != nil || len([]rune(wa.take()[0])) > maxRelayText+60 {
		t.Fatal("texto enorme é cortado")
	}

	s.mu.Lock()
	for i := 0; i < maxPerHour; i++ {
		s.st.Sends = append(s.st.Sends, s.now().Unix())
	}
	s.mu.Unlock()
	if err := s.Relay(ctx, "loja", "oi"); err == nil {
		t.Fatal("o limite por hora do central vale para os avisos emprestados")
	}

	s.cfg.Enabled = false
	if s.RelayReady() || !errors.Is(s.Relay(ctx, "loja", "oi"), ErrRelayNotReady) {
		t.Fatal("notificações desligadas no central: não empresta")
	}
}
