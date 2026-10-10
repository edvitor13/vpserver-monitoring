package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

func testCatalog() {
	Use("xx", File{
		Exact: map[string]string{"Dados inválidos.": "Invalid data.", "Kernel e outros": "Kernel and other"},
		Formats: map[string]string{
			"Disco %.0f%% cheio":                  "Disk %.0f%% full",
			"%s está unhealthy":                   "%s is unhealthy",
			"%s caiu %d vezes em 24 h":            "%s went down %d times in 24 h",
			"há %d dias":                          "%d days ago",
			"Sem notícias de %s há %s":            "No reports from %[1]s for %[2]s",
			"O bucket usa %s dos %s grátis":       "The bucket uses %s of the free %s",
			"Backup de %s: %s":                    "Backup of %[1]s: %[2]s",
			"%s: %s":                              "%s - %s", // pouco texto fixo: ignorado
			"Não consegui ler os logs: %s":        "Couldn't read the logs: %s",
			"%d dias":                             "%d days",
			"ligado (%s)":                         "on (%s)",
			"🔐 *Segurança*\n*%s* entrou (IP %s).": "🔐 *Security*\n*%s* signed in (IP %s).",
		},
	})
}

func TestTr(t *testing.T) {
	testCatalog()
	cases := map[string]string{
		"Dados inválidos.":                                       "Invalid data.",
		"Disco 92% cheio":                                        "Disk 92% full",
		"loja-api-1 está unhealthy":                              "loja-api-1 is unhealthy",
		"loja-api-1 caiu 3 vezes em 24 h":                        "loja-api-1 went down 3 times in 24 h",
		"há 4 dias":                                              "4 days ago",
		"Sem notícias de portal há 3 dias":                       "No reports from portal for 3 days", // o valor também é traduzido
		"O bucket usa 2,5 GB dos 10 GB grátis":                   "The bucket uses 2.5 GB of the free 10 GB",
		"Backup de loja/db: ligado (1h)":                         "Backup of loja/db: on (1h)",
		"Não consegui ler os logs: dial tcp: connection refused": "Couldn't read the logs: dial tcp: connection refused",
		"🔐 *Segurança*\n*maria* entrou (IP 203.0.113.7).":        "🔐 *Security*\n*maria* signed in (IP 203.0.113.7).",
		"3 dias · Kernel e outros":                               "3 days · Kernel and other", // lista com " · "
		"blog: erro":                                             "blog: erro",                // "%s: %s" tem pouco texto fixo: não vale
		"loja-api-1":                                             "loja-api-1",
		"":                                                       "",
	}
	for in, want := range cases {
		if got := Tr("xx", in); got != want {
			t.Errorf("%q: %q, esperava %q", in, got, want)
		}
	}
	if got := Tr(Default, "Dados inválidos."); got != "Dados inválidos." {
		t.Fatalf("o português passa direto: %q", got)
	}
	if got := Tr("zz", "Dados inválidos."); got != "Dados inválidos." {
		t.Fatalf("idioma sem catálogo passa direto: %q", got)
	}
}

func TestJSON(t *testing.T) {
	testCatalog()
	in := `{"error":{"code":"bad_request","message":"Dados inválidos."},"n":1.5,"alerts":[{"title":"Disco 92% cheio","area":"disco"}],` +
		`"lines":[{"msg":"Dados inválidos."}],"ok":true,"big":12345678901234567890}`
	out := string(JSON("xx", []byte(in), map[string]bool{"lines": true}))
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"message":"Invalid data."`, `"title":"Disk 92% full"`, `"code":"bad_request"`, `"area":"disco"`,
		`"lines":[{"msg":"Dados inválidos."}]`, `"n":1.5`, `"big":12345678901234567890`} {
		if !strings.Contains(out, want) {
			t.Errorf("faltou %s em %s", want, out)
		}
	}
	if got := JSON("xx", []byte("não é json"), nil); string(got) != "não é json" {
		t.Fatalf("o que não é JSON volta igual: %q", got)
	}
}

// O catálogo de verdade carrega e reconhece os próprios formatos.
func TestRealCatalogLoads(t *testing.T) {
	f := readCatalog(t)
	c := build(f)
	if len(f.Formats) > 0 && len(c.pats) < len(f.Formats)/2 {
		t.Fatalf("poucos formatos viraram padrão: %d de %d", len(c.pats), len(f.Formats))
	}
	if Has("en") && Tr("en", "Dados inválidos.") == "Dados inválidos." && len(f.Exact) > 0 {
		t.Fatal("o inglês traduz as mensagens fixas")
	}
}
