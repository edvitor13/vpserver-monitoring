package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	ok := map[string]string{
		"painel.exemplo.com":                     "https://painel.exemplo.com",
		" https://painel.exemplo.com/#/servers ": "https://painel.exemplo.com",
		"http://203.0.113.5:8080/qualquer":       "http://203.0.113.5:8080",
		"localhost:8080":                         "https://localhost:8080",
	}
	for in, want := range ok {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("%q → %q, %v (esperava %q)", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ftp://painel.exemplo.com", "https://user:senha@painel.exemplo.com", "painel", "javascript:alert(1)"} {
		if got, err := NormalizeURL(in); err == nil {
			t.Errorf("%q deveria ser recusado (veio %q)", in, got)
		}
	}
}

func TestClientWithoutCentral(t *testing.T) {
	dir := t.TempDir()
	c := NewClient(dir, "dev", func() Report { return Report{} })
	if c.Status().Connected || c.Active() {
		t.Fatal("sem central configurado")
	}
	c.Report(context.Background()) // não faz nada
	if _, err := c.SetUseWhatsApp(true); err == nil {
		t.Fatal("sem central não liga o WhatsApp emprestado")
	}
	if _, err := c.Connect(context.Background(), "painel.exemplo.com", "sem-prefixo"); err == nil || !strings.Contains(err.Error(), "vps_") {
		t.Fatalf("token sem o prefixo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fleet-remote.json")); !os.IsNotExist(err) {
		t.Fatal("nada é gravado sem conectar")
	}
}
