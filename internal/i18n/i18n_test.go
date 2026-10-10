package i18n

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

// Mensagem do WhatsApp: montada em pedaços, traduzida linha por linha, com o
// enfeite (emoji, "• ", *negrito*) de fora; cada linha dos formatos de várias
// linhas também vale sozinha.
func TestMessage(t *testing.T) {
	Use("xx", File{
		Exact: map[string]string{"Urgente": "Urgent", "Resumo de ontem": "Yesterday's summary", "*Servidor*": "*Server*"},
		Formats: map[string]string{
			"Disco %.0f%% cheio":                  "Disk %.0f%% full",
			"*%s* _(piorou)_":                     "*%s* _(got worse)_",
			"%d dias":                             "%d days",
			"%s _(durou %s)_":                     "%s _(lasted %s)_",
			"✅ *Resolvido · %s*\n%s _(durou %s)_": "✅ *Resolved · %s*\n%s _(lasted %s)_",
			"🔐 *Segurança*\n*%s* entrou (IP %s).": "🔐 *Security*\n*%s* signed in (IP %s).",
			"Backup de %s:\n%[2]s":                "%[2]s\nbackup of %[1]s", // índice: não vale por linha
			"• Saída: %s · entrada %s":            "• Outbound: %s · inbound %s",
		},
	})
	cases := map[string]string{
		"🔴 *Urgente · srv*\n*Disco 92% cheio*\nVeja 🔗 https://painel.exemplo.com/#/infos": "🔴 *Urgent · srv*\n*Disk 92% full*\nVeja 🔗 https://painel.exemplo.com/#/infos",
		"🔴 *Disco 92% cheio* _(piorou)_":                        "🔴 *Disk 92% full* _(got worse)_",
		"✅ *Resolvido · srv*\nDisco 95% cheio _(durou 3 dias)_": "✅ *Resolved · srv*\nDisk 95% full _(lasted 3 days)_",
		// um formato de várias linhas no meio de outra mensagem (o resumo do silêncio)
		"🌙 *Silêncio*\n\n_23:10_\n🔐 *Segurança*\n*ana* entrou (IP 203.0.113.7).":    "🌙 *Silêncio*\n\n_23:10_\n🔐 *Security*\n*ana* signed in (IP 203.0.113.7).",
		"📊 *Resumo de ontem · srv*\n\n*Servidor*\n• Saída: 2,5 GB · entrada 1,0 GB": "📊 *Yesterday's summary · srv*\n\n*Server*\n• Outbound: 2.5 GB · inbound 1.0 GB",
		"Texto da IA que já veio em inglês.\n• loja-api: ok":                        "Texto da IA que já veio em inglês.\n• loja-api: ok",
	}
	for in, want := range cases {
		if got := Message("xx", in); got != want {
			t.Errorf("%q:\n%q\nesperava\n%q", in, got, want)
		}
	}
	if got := Message("xx", "backup of loja"); got != "backup of loja" {
		t.Fatalf("linha de formato com índice não vira padrão: %q", got)
	}
	if got := Message(Default, "🔴 *Urgente · srv*"); got != "🔴 *Urgente · srv*" {
		t.Fatalf("o português passa direto: %q", got)
	}
}

// Linha que aparece em dois textos de várias linhas tem a mesma tradução nos dois.
func TestCatalogLinesAgree(t *testing.T) {
	if _, _, conflicts := lines(readCatalog(t)); len(conflicts) > 0 {
		t.Fatalf("linhas com duas traduções diferentes em en.json: %q", conflicts)
	}
	_, _, conflicts := lines(File{Exact: map[string]string{"a\nPor favor": "a\nPlease", "b\nPor favor": "b\nKindly"}})
	if len(conflicts) != 1 {
		t.Fatalf("conflito não achado: %q", conflicts)
	}
}

func TestDates(t *testing.T) {
	d := time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC) // segunda
	for _, c := range [][2]string{
		{Weekday(Default, d.Weekday()) + ", " + DayMonth(Default, d), "segunda, 05/10"},
		{Weekday("en", d.Weekday()) + ", " + DayMonth("en", d), "Monday, 10/05"},
		{MonthYear(Default, d), "outubro de 2026"},
		{MonthYear("en", d), "October 2026"},
		{Month(Default, time.March), "março"},
	} {
		if c[0] != c[1] {
			t.Errorf("%q, esperava %q", c[0], c[1])
		}
	}
	if !Valid(Default) || !Valid("en") || Valid("fr") || Valid("") {
		t.Fatal("Valid")
	}
}
