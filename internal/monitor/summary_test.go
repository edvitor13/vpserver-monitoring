package monitor

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/i18n"
	"github.com/edvitor13/vpserver-monitoring/internal/store"
)

func TestDailySummary(t *testing.T) {
	m, now := summaryMonitor()
	got := m.Summary("daily", now, "pt-BR")
	for _, want := range []string{"Resumo de ontem · srv", "segunda, 05/10", "CPU: média", "pico 80% às 14:30",
		"Saída de dados: 2,0 GB", "(<0,1% dos 10 TB grátis)", "loja-api caiu 1×", "Agora:* ✅ tudo certo"} {
		if !strings.Contains(got, want) {
			t.Fatalf("faltou %q em:\n%s", want, got)
		}
	}
	if strings.Contains(got, "blog caiu") {
		t.Fatalf("parada pedida (deploy) não é queda:\n%s", got)
	}
	if p := m.PeriodOf("monthly", now); p.From.Format("2006-01-02") != "2026-09-01" || p.To.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("mês anterior errado: %v a %v", p.From, p.To)
	}
}

// O resumo em inglês: as datas saem no formato do idioma e o texto, traduzido
// no envio (i18n.Message), não deixa nada em português.
func TestSummaryInEnglish(t *testing.T) {
	m, now := summaryMonitor()
	pt := regexp.MustCompile(`(?i)[à-ÿ]|\b(de|do|da|dos|em|às|média|pico|saída|entrada|grátis|caiu|agora|tudo|certo|servidor|aplicações|ocorrências|resumo|mais)\b`)
	for kind, wants := range map[string][]string{
		"daily": {"📊 *Yesterday's summary · srv*", "_Monday, 10/05_", "• CPU: average 10", "peak 80% at 14:30", "• Outbound data: 2.0 GB",
			"(<0.1% of the free 10 TB)", "*Server*", "• loja-api went down 1×", "*Now:* ✅ all good"},
		"monthly": {"📊 *Month-end summary · srv*", "_October 2026_", "• Outbound data: 2.0 GB (<0.1% of the free 10 TB) · inbound 1.0 GB"},
		"weekly":  {"📊 *Weekly summary · srv*", "_09/29 – 10/05_", "peak 80% on 10/05 14:30"},
	} {
		got := i18n.Message("en", m.Summary(kind, now.AddDate(0, map[string]int{"monthly": 1}[kind], 0), "en"))
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s: faltou %q em:\n%s", kind, want, got)
			}
		}
		for _, ln := range strings.Split(got, "\n") {
			if w := pt.FindString(ln); w != "" {
				t.Errorf("%s: sobrou português (%q) em %q:\n%s", kind, w, ln, got)
			}
		}
	}
}

// Infos em inglês (a tela traduz na saída, como i18n.Tr): títulos, ações e
// itens montados no servidor saem inteiros, e o português sai como antes.
func TestInfosInEnglish(t *testing.T) {
	m, now := summaryMonitor()
	m.cfg.SelfProject = "vpserver-monitoring"
	gb := float64(1 << 30)
	m.stor.view.BuildCache = uint64(17.6 * gb)
	m.hostNow.FSUsed = m.stor.view.BuildCache * 100 / 21
	unit := func(name string, logs uint64) UnitView {
		return UnitView{Name: name, Container: &docker.Container{Name: name, State: "running"}, Disk: &Disk{Logs: logs}}
	}
	m.apps = []AppView{
		{Key: "loja", Name: "Loja", Kind: "compose", Units: []UnitView{unit("loja-api-1", uint64(1.4*gb))}},
		{Key: "portal", Name: "Portal", Kind: "compose", Units: []UnitView{unit("portal-app-1", 563<<20)}},
		{Key: "vpserver-monitoring", Name: m.appName("vpserver-monitoring"), Kind: "compose", Self: true,
			Units: []UnitView{unit("vpserver-cleaner", 0), unit("vpserver-dockerproxy", 0)}},
	}
	infos := map[string]Alert{}
	for _, a := range m.infos(now, LimitsView{}) {
		infos[a.Key] = a
	}
	monitorItem := "Monitor (this panel): vpserver-cleaner, vpserver-dockerproxy"
	for _, c := range []struct{ key, field, pt, en string }{
		{"info.buildcache", "Title", "Cache de build do Docker: 17,6 GB (21% do disco usado)", "Docker build cache: 17.6 GB (21% of the disk used)"},
		{"info.logs:Loja", "Title", "Logs do Loja: 1,4 GB e crescendo", "Loja logs: 1.4 GB and growing"},
		{"info.logs:Portal", "Title", "Logs do Portal: 563 MB e crescendo", "Portal logs: 563 MB and growing"},
		{"info.logs:Loja", "Action", `no compose da app: logging: {driver: json-file, options: {max-size: "10m", max-file: "3"}}`,
			`in the app's compose file: logging: {driver: json-file, options: {max-size: "10m", max-file: "3"}}`},
		{"info.nolimit", "Action", "no compose: mem_limit (ex.: mem_limit: 512m)", "in the compose file: mem_limit (e.g. mem_limit: 512m)"},
		{"info.nohealth", "Action", "no compose: healthcheck: {test: [...], interval: 30s}", "in the compose file: healthcheck: {test: [...], interval: 30s}"},
		{"info.nolimit", "Items", "Monitor (este painel): vpserver-cleaner, vpserver-dockerproxy", monitorItem},
		{"info.nohealth", "Items", "Monitor (este painel): vpserver-cleaner, vpserver-dockerproxy", monitorItem},
	} {
		a, ok := infos[c.key]
		if !ok {
			t.Fatalf("faltou a Info %s em %+v", c.key, infos)
		}
		got := map[string]string{"Title": a.Title, "Action": a.Action}[c.field]
		if c.field == "Items" {
			for _, it := range a.Items {
				if strings.HasPrefix(it, "Monitor (") {
					got = it
				}
			}
		}
		if got != c.pt {
			t.Errorf("%s %s: %q, esperava %q", c.key, c.field, got, c.pt)
		}
		if en := i18n.Tr("en", got); en != c.en {
			t.Errorf("%s %s em inglês: %q, esperava %q", c.key, c.field, en, c.en)
		}
	}
	// nada de português em nenhuma Info montada aqui
	pt := regexp.MustCompile(`(?i)[à-ÿ]|\b(de|do|da|dos|das|em|na|ex|crescendo|usado|disco|servidor|painel|este|sem|limite)\b`)
	for _, a := range infos {
		for _, s := range append([]string{a.Title, a.Detail, a.Action}, a.Items...) {
			if en := i18n.Tr("en", s); pt.MatchString(en) {
				t.Errorf("%s: sobrou português (%q) em %q", a.Key, pt.FindString(en), en)
			}
		}
	}
	// sem o disco lido (logo depois de subir), só o tamanho
	m.hostNow.FSUsed = 0
	for _, a := range m.infos(now, LimitsView{}) {
		if a.Key == "info.buildcache" && (a.Title != "Cache de build do Docker: 17,6 GB" || i18n.Tr("en", a.Title) != "Docker build cache: 17.6 GB") {
			t.Errorf("cache de build sem o disco: %q / %q", a.Title, i18n.Tr("en", a.Title))
		}
	}
}

func summaryMonitor() (*Monitor, time.Time) {
	loc := time.FixedZone("BRT", -3*3600)
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, loc)
	yesterday := time.Date(2026, 10, 5, 0, 0, 0, 0, loc)
	m := &Monitor{cfg: Config{Loc: loc, ServerName: "srv", Limits: Limits{EgressTB: 10}},
		st: store.NewState(yesterday.Add(-48 * time.Hour).Unix()), units: map[string]*UnitView{}, containers: map[string]docker.Container{}}
	m.st.Host = store.NewSeries(hostFields, hostTiers)
	m.dockerOK = true
	cpuIdx := 0
	for i, f := range hostFields {
		if f == "cpu" {
			cpuIdx = i
		}
	}
	for ts := yesterday; ts.Before(yesterday.Add(24 * time.Hour)); ts = ts.Add(5 * time.Minute) {
		v := make([]float64, len(hostFields))
		v[cpuIdx] = 10
		if ts.Hour() == 14 && ts.Minute() == 30 {
			v[cpuIdx] = 80
		}
		m.st.Host.Add(ts.Unix(), v)
	}
	m.st.Traffic.Add("host", "2026-10-05", 1e9, 2e9)
	m.st.Events = []docker.Event{
		{T: yesterday.Add(10 * time.Hour).Unix(), Action: "die", Container: "loja-api", ExitCode: "1"},
		{T: yesterday.Add(11 * time.Hour).Unix(), Action: "kill", Container: "blog"}, // deploy: não é queda
		{T: yesterday.Add(11 * time.Hour).Unix(), Action: "die", Container: "blog", ExitCode: "137"},
	}
	return m, now
}

func TestMaskURLPassword(t *testing.T) {
	in := "Database URL: postgresql://evolution:s3cr3t@vpserver-whatsapp-db:5432/evolution?schema=public"
	if out := MaskURLPassword(in); strings.Contains(out, "s3cr3t") || !strings.Contains(out, "evolution:[oculto]@vpserver-whatsapp-db") {
		t.Fatalf("senha da URL: %s", out)
	}
}
