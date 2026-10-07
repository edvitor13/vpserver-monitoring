package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newCentral(t *testing.T) (*Central, *clock, string) {
	dir := t.TempDir()
	c := NewCentral(dir)
	ck := &clock{time.Unix(1_800_000_000, 0)}
	c.now = ck.now
	return c, ck, dir
}

func TestTokensCreateAuthRevoke(t *testing.T) {
	c, _, dir := newCentral(t)
	if _, _, err := c.Create("  ", "chefe", false); !errors.Is(err, ErrBadName) {
		t.Fatalf("nome vazio: %v", err)
	}
	v, plain, err := c.Create("loja", "chefe", true)
	if err != nil || !strings.HasPrefix(plain, "vps_") || len(plain) < 40 || v.Name != "loja" || !v.WhatsApp || v.Online {
		t.Fatalf("criar: %+v %q %v", v, plain, err)
	}
	if got, ok := c.Auth("Bearer " + plain); !ok || got.ID != v.ID {
		t.Fatal("o token certo deveria autenticar")
	}
	for _, h := range []string{"", plain, "Bearer vps_errado", "Basic " + plain, "Bearer " + plain + "x"} {
		if _, ok := c.Auth(h); ok {
			t.Fatalf("não deveria autenticar: %q", h)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "fleet-central.json"))
	if strings.Contains(string(b), plain) || !strings.Contains(string(b), v.ID) {
		t.Fatal("o arquivo guarda só o hash do token")
	}
	if again := NewCentral(dir); len(again.List()) != 1 {
		t.Fatal("o token sobrevive ao reinício")
	}
	if err := c.Revoke(v.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Auth("Bearer " + plain); ok {
		t.Fatal("token revogado não autentica")
	}
}

func TestReportsOnlineAndOfflineAlert(t *testing.T) {
	c, ck, _ := newCentral(t)
	v, _, _ := c.Create("loja", "chefe", false)
	if len(c.Alerts()) != 0 {
		t.Fatal("servidor que nunca conectou não é queda")
	}
	r := Report{Name: "loja-srv", PanelURL: "javascript:alert(1)", Top: []string{"a", "b", "c", "d"}, WhatsApp: "qualquer"}
	if err := c.Accept(v.ID, r, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	got := c.List()[0]
	if !got.Online || got.LastIP != "203.0.113.7" || got.Report.PanelURL != "" || len(got.Report.Top) != 3 || got.Report.WhatsApp != "off" {
		t.Fatalf("resumo limpo e online: %+v %+v", got, got.Report)
	}
	if err := c.Accept(v.ID, r, ""); !errors.Is(err, ErrTooSoon) {
		t.Fatalf("resumo em rajada: %v", err)
	}
	ck.add(2 * time.Minute)
	if len(c.Alerts()) != 0 {
		t.Fatal("2 min ainda não é silêncio")
	}
	ck.add(2 * time.Minute)
	a := c.Alerts()
	if len(a) != 1 || a[0].Key != "fleet.offline:"+v.ID || a[0].Level != "crit" || !strings.Contains(a[0].Title, "loja") {
		t.Fatalf("alerta de silêncio: %+v", a)
	}
	if c.List()[0].Online {
		t.Fatal("deveria estar sem notícias")
	}
	if err := c.Accept(v.ID, r, ""); err != nil || len(c.Alerts()) != 0 {
		t.Fatal("voltou a mandar: some o alerta")
	}
	if err := c.Bye(v.ID); err != nil {
		t.Fatal(err)
	}
	ck.add(10 * time.Minute)
	if got := c.List()[0]; len(c.Alerts()) != 0 || got.Online || got.Report != nil || got.LastSeen != 0 {
		t.Fatalf("desconectou de propósito: sem alerta e sem card velho (%+v)", got)
	}
}

func TestRelayPermissionAndRate(t *testing.T) {
	c, ck, _ := newCentral(t)
	no, _, _ := c.Create("sem-whatsapp", "chefe", false)
	yes, _, _ := c.Create("com-whatsapp", "chefe", true)
	if err := c.AllowRelay(no.ID); !errors.Is(err, ErrNoRelay) {
		t.Fatalf("token sem WhatsApp: %v", err)
	}
	for i := 0; i < relayPerHour; i++ {
		if err := c.AllowRelay(yes.ID); err != nil {
			t.Fatalf("aviso %d: %v", i, err)
		}
	}
	if err := c.AllowRelay(yes.ID); !errors.Is(err, ErrRelayRate) {
		t.Fatalf("limite por hora: %v", err)
	}
	ck.add(61 * time.Minute)
	if err := c.AllowRelay(yes.ID); err != nil {
		t.Fatalf("depois de uma hora libera de novo: %v", err)
	}
}
