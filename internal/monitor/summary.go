package monitor

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/store"
)

// Resumos das notificações (WhatsApp). Os períodos são fechados, no fuso do
// painel: "daily" = ontem, "weekly" = os 7 dias até ontem, "monthly" = o mês
// anterior. O texto já sai no formato do WhatsApp (*negrito*, "•") e com os
// mesmos números da tela.

var (
	weekdayNames = [...]string{"domingo", "segunda", "terça", "quarta", "quinta", "sexta", "sábado"}
	monthNames   = [...]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto",
		"setembro", "outubro", "novembro", "dezembro"}
)

// Period é o intervalo [From, To) de um resumo.
type Period struct {
	Kind     string
	From, To time.Time
	Title    string // "Resumo de ontem"
	Label    string // "segunda, 05/10"
}

// PeriodOf devolve o período fechado de um resumo (daily, weekly ou monthly).
func (m *Monitor) PeriodOf(kind string, now time.Time) Period {
	loc := m.cfg.Loc
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	p := Period{Kind: kind}
	switch kind {
	case "weekly":
		p.From, p.To = today.AddDate(0, 0, -7), today
		p.Title = "Resumo da semana"
		p.Label = p.From.Format("02/01") + " a " + p.To.AddDate(0, 0, -1).Format("02/01")
	case "monthly":
		first := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
		p.From, p.To = first.AddDate(0, -1, 0), first
		p.Title = "Fechamento de " + monthNames[p.From.Month()-1]
		p.Label = fmt.Sprintf("%s de %d", monthNames[p.From.Month()-1], p.From.Year())
	default:
		p.Kind = "daily"
		p.From, p.To = today.AddDate(0, 0, -1), today
		p.Title = "Resumo de ontem"
		p.Label = weekdayNames[p.From.Weekday()] + ", " + p.From.Format("02/01")
	}
	return p
}

// prev é o período de mesmo tamanho logo antes (para comparar).
func (p Period) prev() Period {
	q := p
	if p.Kind == "monthly" {
		q.From, q.To = p.From.AddDate(0, -1, 0), p.From
	} else {
		q.From, q.To = p.From.Add(-p.To.Sub(p.From)), p.From
	}
	return q
}

func (p Period) days() []string {
	var out []string
	for d := p.From; d.Before(p.To); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

type usage struct {
	avg, peak, first, last float64
	peakT                  int64
	n                      int
}

func usageOf(t []int64, v store.Nums) usage {
	u := usage{peak: math.Inf(-1)}
	var sum float64
	for i, x := range v {
		if math.IsNaN(x) {
			continue
		}
		if u.n == 0 {
			u.first = x
		}
		u.last = x
		sum += x
		u.n++
		if x > u.peak {
			u.peak, u.peakT = x, t[i]
		}
	}
	if u.n > 0 {
		u.avg = sum / float64(u.n)
	}
	return u
}

// tiny é a porcentagem que pode ser minúscula (banda contra 10 TB).
func tiny(v float64) string {
	if v > 0 && v < 0.1 {
		return "<0,1%"
	}
	return pctS(v)
}

// Summary monta o resumo de um período (daily, weekly ou monthly).
func (m *Monitor) Summary(kind string, now time.Time) string {
	o := m.Overview() // antes de travar: o Overview trava sozinho
	p := m.PeriodOf(kind, now)
	m.mu.RLock()
	defer m.mu.RUnlock()

	from, to := p.From.Unix(), p.To.Unix()
	at := func(t int64) string {
		if p.Kind == "daily" {
			return "às " + time.Unix(t, 0).In(m.cfg.Loc).Format("15:04")
		}
		return "em " + time.Unix(t, 0).In(m.cfg.Loc).Format("02/01 15:04")
	}
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	line("📊 *%s · %s*", p.Title, o.Server.Name)
	line("_%s_", p.Label)
	if m.st.Created > from {
		line("_(o painel coleta desde %s)_", m.when(m.st.Created))
	}

	// servidor
	r := m.st.Host.Query(from, to, 2000, "cpu", "mem", "fs")
	cpu, mem, fs := usageOf(r.T, r.Cols["cpu"]), usageOf(r.T, r.Cols["mem"]), usageOf(r.T, r.Cols["fs"])
	line("")
	line("*Servidor*")
	if cpu.n == 0 {
		line("• Sem dados de CPU e memória neste período.")
	} else {
		cmp := ""
		if p.Kind != "daily" {
			q := p.prev()
			if pr := m.st.Host.Query(q.From.Unix(), q.To.Unix(), 2000, "cpu"); m.st.Created <= q.From.Unix() {
				if pc := usageOf(pr.T, pr.Cols["cpu"]); pc.n > 0 {
					cmp = " (antes: " + pctS(pc.avg) + ")"
				}
			}
		}
		line("• CPU: média %s%s · pico %s %s", pctS(cpu.avg), cmp, pctS(cpu.peak), at(cpu.peakT))
		total := ""
		if o.Host.MemTotal > 0 {
			total = " de " + size(o.Host.MemTotal)
		}
		line("• Memória: média %s · pico %s%s", size(uint64(mem.avg)), size(uint64(mem.peak)), total)
	}
	if o.Host.FSTotal > 0 {
		grow := ""
		if fs.n > 1 {
			if d := fs.last - fs.first; d >= 0 {
				grow = " · +" + size(uint64(d)) + " no período"
			} else {
				grow = " · −" + size(uint64(-d)) + " no período"
			}
		}
		line("• Disco: %s usado (%s de %s)%s", pctS(float64(o.Host.FSUsed)/float64(o.Host.FSTotal)*100),
			size(o.Host.FSUsed), size(o.Host.FSTotal), grow)
	}
	var tx, rx uint64
	for _, d := range p.days() {
		v := m.st.Traffic.Day("host", d)
		tx, rx = tx+v.Tx, rx+v.Rx
	}
	limit := m.cfg.Limits.EgressTB * 1e12
	switch p.Kind {
	case "monthly":
		line("• Saída de dados: %s (%s dos %s TB grátis) · entrada %s", dataSize(tx), tiny(float64(tx)/limit*100), dec(m.cfg.Limits.EgressTB, 0), dataSize(rx))
	case "weekly":
		line("• Saída de dados: %s · entrada %s", dataSize(tx), dataSize(rx))
	default:
		mo := m.st.Traffic.Month("host", p.To.Add(-time.Second).Format("2006-01"))
		line("• Saída de dados: %s · no mês: %s (%s dos %s TB grátis)", dataSize(tx), dataSize(mo.Tx), tiny(float64(mo.Tx)/limit*100), dec(m.cfg.Limits.EgressTB, 0))
	}

	// aplicações
	type appUse struct {
		name    string
		cpu     float64
		mem, tx float64
	}
	var apps []appUse
	for k, s := range m.st.Units {
		key, ok := strings.CutPrefix(k, "app:")
		if !ok || key == AppKernel || key == AppTemp || s.Last < from {
			continue
		}
		q := s.Query(from, to, 2000, "cpu", "mem")
		a := appUse{name: m.appName(key), cpu: usageOf(q.T, q.Cols["cpu"]).avg, mem: usageOf(q.T, q.Cols["mem"]).avg}
		for _, d := range p.days() {
			a.tx += float64(m.st.Traffic.Day(k, d).Tx)
		}
		apps = append(apps, a)
	}
	up, total := 0, 0
	for _, a := range o.Apps {
		if a.Kind == "compose" || a.Kind == "standalone" {
			total++
			if a.Running > 0 {
				up++
			}
		}
	}
	line("")
	line("*Aplicações* · %d de %d no ar", up, total)
	top := func(label string, val func(appUse) float64, f func(float64) string) {
		sort.Slice(apps, func(i, j int) bool { return val(apps[i]) > val(apps[j]) })
		var parts []string
		for _, a := range apps {
			if len(parts) == 3 || val(a) <= 0 {
				break
			}
			parts = append(parts, a.name+" "+f(val(a)))
		}
		if len(parts) > 0 {
			line("• %s: %s", label, strings.Join(parts, " · "))
		}
	}
	top("Mais CPU", func(a appUse) float64 { return a.cpu }, pctS)
	top("Mais memória", func(a appUse) float64 { return a.mem }, func(v float64) string { return size(uint64(v)) })
	top("Mais saída", func(a appUse) float64 { return a.tx }, func(v float64) string { return dataSize(uint64(v)) })

	// ocorrências
	crashes, ooms := map[string]int{}, map[string]int{}
	var hostOOM int
	for _, e := range docker.MarkRequested(m.st.Events) {
		if e.T < from || e.T >= to {
			continue
		}
		switch {
		case e.Action == "die" && e.ExitCode != "0" && !e.Requested:
			crashes[e.Container]++
		case e.Action == "oom":
			ooms[e.Container]++
		case e.Action == "oom_kill_host":
			hostOOM++
		}
	}
	line("")
	line("*Ocorrências*")
	if len(crashes)+len(ooms)+hostOOM == 0 {
		line("• Nenhuma queda de contêiner nem falta de memória.")
	}
	list := func(mp map[string]int, f string) {
		keys := make([]string, 0, len(mp))
		for k := range mp {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return mp[keys[i]] > mp[keys[j]] })
		for i, k := range keys {
			if i == 5 {
				line("• … e mais %d", len(keys)-5)
				break
			}
			line(f, k, mp[k])
		}
	}
	list(crashes, "• %s caiu %d×")
	list(ooms, "• %s ficou sem memória (OOM) %d×")
	if hostOOM > 0 {
		line("• O kernel matou processo por falta de memória %d×", hostOOM)
	}

	// agora
	crit, warn := 0, 0
	for _, a := range o.Alerts {
		switch a.Level {
		case "crit":
			crit++
		case "warn":
			warn++
		}
	}
	line("")
	switch {
	case crit+warn == 0:
		line("*Agora:* ✅ tudo certo")
	default:
		var parts []string
		if crit > 0 {
			parts = append(parts, fmt.Sprintf("🔴 %d urgente(s)", crit))
		}
		if warn > 0 {
			parts = append(parts, fmt.Sprintf("🟡 %d alerta(s)", warn))
		}
		line("*Agora:* %s", strings.Join(parts, " · "))
	}
	return strings.TrimRight(b.String(), "\n")
}
