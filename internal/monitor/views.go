package monitor

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/host"
	"github.com/edvitor13/vpserver-monitoring/internal/procs"
	"github.com/edvitor13/vpserver-monitoring/internal/store"
)

const gib = 1 << 30

type Server struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	Docker   string `json:"docker"`
	Cores    int    `json:"cores"`
	Iface    string `json:"iface"`
	DiskSize uint64 `json:"diskSize"`
	Cloud    Cloud  `json:"cloud"`
}

type Alert struct {
	Key    string   `json:"key"`   // identidade estável (o título muda com os números): "host.cpu", "app.oom:<contêiner>"...
	Level  string   `json:"level"` // crit | warn | info
	Area   string   `json:"area"`  // host | app | limite | monitor | disco
	Title  string   `json:"title"`
	Detail string   `json:"detail,omitempty"`
	Target string   `json:"target,omitempty"` // chave da unidade/app para o link
	Items  []string `json:"items,omitempty"`  // lista do que está envolvido
	Action string   `json:"action,omitempty"` // o que fazer (comando ou ajuste)
}

type TrafficSummary struct {
	Iface        string     `json:"iface"`
	Today        store.RxTx `json:"today"`
	Yesterday    store.RxTx `json:"yesterday"`
	Month        store.RxTx `json:"month"`
	LastMonth    store.RxTx `json:"lastMonth"`
	Total        store.RxTx `json:"total"`
	SinceBoot    store.RxTx `json:"sinceBoot"`
	MonthElapsed float64    `json:"monthElapsed"` // fração do mês que já passou
	ProjectedTx  float64    `json:"projectedTx"`
	LimitBytes   float64    `json:"limitBytes"`
	Since        int64      `json:"since"` // desde quando o monitor conta
}

type LimitItem struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Used  float64 `json:"used"`
	Limit float64 `json:"limit"`
	Unit  string  `json:"unit"` // bytes | ocpu | gb
	Note  string  `json:"note"`
	Level string  `json:"level"` // ok | info | warn | crit
}

type Reclaim struct {
	Applies   string  `json:"applies"` // unknown | yes | no
	DataDays  float64 `json:"dataDays"`
	CPUP95    float64 `json:"cpuP95"`
	MemAvg    float64 `json:"memAvg"`
	NetAvg    float64 `json:"netAvg"`
	Threshold float64 `json:"threshold"`
	Idle      bool    `json:"idle"`  // os três abaixo do limite
	Ready     bool    `json:"ready"` // já há 7 dias de dados
}

type LimitsView struct {
	Kind    string      `json:"kind"` // a1 | micro | paid (shape fora do grátis) | "" (fora da Oracle)
	Shape   string      `json:"shape"`
	Region  string      `json:"region"`
	Items   []LimitItem `json:"items"`
	Reclaim Reclaim     `json:"reclaim"`
}

type Overview struct {
	Version  string         `json:"version"`
	Server   Server         `json:"server"`
	Host     host.Sample    `json:"host"`
	Apps     []AppView      `json:"apps"`
	Alerts   []Alert        `json:"alerts"`
	Traffic  TrafficSummary `json:"traffic"`
	Limits   LimitsView     `json:"limits"`
	Storage  StorageView    `json:"storage"`
	DockerOK bool           `json:"dockerOk"`
	Updated  int64          `json:"updated"`
	Started  int64          `json:"started"`
	Since    int64          `json:"since"` // início do histórico
}

func (m *Monitor) Overview() Overview {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now()
	o := Overview{
		Version: m.cfg.Version, Host: m.hostNow, DockerOK: m.dockerOK,
		Updated: m.hostNow.T, Started: m.started.Unix(), Since: m.st.Created,
		Apps: append([]AppView{}, m.apps...), // nunca null: a tela faz .filter/.map direto
	}
	o.Server = m.serverInfo()
	o.Storage = m.stor.view
	// "sistema e outros" com o disco de agora (a medição do Docker é de até 30 min atrás)
	if known := o.Storage.Apps + o.Storage.BuildCache + o.Storage.UnusedImages; o.Storage.Measured > 0 && m.hostNow.FSUsed > known {
		o.Storage.Other = m.hostNow.FSUsed - known
	}
	o.Traffic = m.trafficSummary(now)
	o.Limits = m.limits(now, o.Traffic)
	o.Alerts = m.alerts(now, o.Traffic, o.Limits)
	return o
}

func (m *Monitor) serverInfo() Server {
	var disk uint64
	for _, s := range m.disks {
		disk += s
	}
	name := m.cfg.ServerName
	if name == "" {
		name = m.cloud.Name // nome da instância no console da Oracle
	}
	if name == "" {
		name = m.info.Name
	}
	return Server{Name: name, Hostname: m.info.Name, OS: m.info.OperatingSystem, Kernel: m.info.KernelVersion,
		Arch: m.info.Architecture, Docker: m.info.ServerVersion, Cores: m.hostNow.Cores, Iface: m.iface, DiskSize: disk,
		Cloud: m.cloud}
}

func (m *Monitor) trafficSummary(now time.Time) TrafficSummary {
	t := m.st.Traffic
	loc := now.In(m.cfg.Loc)
	first := time.Date(loc.Year(), loc.Month(), 1, 0, 0, 0, 0, m.cfg.Loc)
	next := first.AddDate(0, 1, 0)
	s := TrafficSummary{
		Iface: m.iface, Today: t.Day("host", m.day(now)), Yesterday: t.Day("host", m.day(now.AddDate(0, 0, -1))),
		Month: t.Month("host", m.month(now)), LastMonth: t.Month("host", m.month(first.AddDate(0, 0, -1))),
		Total: t.Total("host"), SinceBoot: store.RxTx{Rx: m.hostNow.NetRxCounter, Tx: m.hostNow.NetTxCounter},
		LimitBytes: m.cfg.Limits.EgressTB * 1e12, Since: m.st.Created,
	}
	s.MonthElapsed = now.Sub(first).Seconds() / next.Sub(first).Seconds()
	// projeção: o mês corrente no ritmo dos dias medidos (contando só desde que o monitor começou)
	start := first
	if c := time.Unix(m.st.Created, 0); c.After(start) {
		start = c
	}
	if el := now.Sub(start).Hours(); el >= 6 {
		s.ProjectedTx = float64(s.Month.Tx) / el * next.Sub(first).Hours()
	}
	return s
}

func pctLevel(p, info, warn, crit float64) string {
	switch {
	case p >= crit:
		return "crit"
	case p >= warn:
		return "warn"
	case p >= info:
		return "info"
	}
	return "ok"
}

func (m *Monitor) limits(now time.Time, tr TrafficSummary) LimitsView {
	l := m.cfg.Limits
	h := m.hostNow
	var disk uint64
	for _, s := range m.disks {
		disk += s
	}
	c := m.cloud
	kind := c.FreeKind()
	ocpu := c.OCPUs
	if ocpu == 0 {
		ocpu = float64(h.Cores)
	}
	memGB := c.MemGB
	if memGB == 0 {
		memGB = math.Round(float64(h.MemTotal) / gib)
	}
	egressNote := "Limite de saída configurado em VPMON_EGRESS_TB."
	if c.Provider == "oracle" {
		egressNote = "A Oracle não cobra os primeiros 10 TB de saída por mês. Entrada é sempre grátis."
	}
	items := []LimitItem{
		{Key: "egress", Label: "Saída de dados (internet) no mês", Used: float64(tr.Month.Tx), Limit: tr.LimitBytes, Unit: "bytes",
			Note: egressNote, Level: pctLevel(float64(tr.Month.Tx)/tr.LimitBytes*100, 50, 75, 90)},
	}
	switch kind {
	case "a1":
		items = append(items,
			LimitItem{Key: "ocpu", Label: "OCPUs Ampere A1 (esta VM)", Used: ocpu, Limit: l.FreeOCPU, Unit: "ocpu",
				Note:  "O Always Free dá 4 OCPUs A1 no total da conta (somando todas as VMs).",
				Level: pctLevel(ocpu/l.FreeOCPU*100, 101, 101, 101)},
			LimitItem{Key: "ram", Label: "Memória Ampere A1 (esta VM)", Used: memGB, Limit: l.FreeMemGB, Unit: "gb",
				Note:  "O Always Free dá 24 GB de RAM A1 no total da conta.",
				Level: pctLevel(memGB/l.FreeMemGB*100, 101, 101, 101)})
	case "micro":
		items = append(items, LimitItem{Key: "micro", Label: "VMs AMD Micro (esta conta)", Used: 1, Limit: 2, Unit: "vm",
			Note: "O Always Free dá 2 VMs VM.Standard.E2.1.Micro (1/8 de núcleo e 1 GB cada).", Level: "ok"})
	}
	if c.Provider == "oracle" {
		items = append(items, LimitItem{Key: "block", Label: "Armazenamento em bloco (disco de boot)", Used: math.Round(float64(disk) / gib), Limit: l.FreeDiskGB, Unit: "gb",
			Note:  "200 GB grátis somando discos de boot e volumes de todas as VMs da conta.",
			Level: pctLevel(float64(disk)/gib/l.FreeDiskGB*100, 101, 101, 101)})
	}
	items = append(items, LimitItem{Key: "fs", Label: "Espaço usado no disco", Used: float64(h.FSUsed), Limit: float64(h.FSTotal), Unit: "bytes",
		Note:  "Não é limite de cobrança: é o disco encher. Com ele cheio, banco e Docker param.",
		Level: pctLevel(float64(h.FSUsed)/math.Max(1, float64(h.FSTotal))*100, 70, 80, 90)})
	return LimitsView{Kind: kind, Shape: c.Shape, Region: c.RegionName, Items: items, Reclaim: m.reclaim(now)}
}

// reclaim estima a regra de "instância ociosa" do Always Free: a Oracle pode
// recuperar a VM se, em 7 dias, CPU (percentil 95), rede e memória (A1)
// ficarem todos abaixo de 20%.
func (m *Monitor) reclaim(now time.Time) Reclaim {
	r := Reclaim{Applies: m.cfg.Limits.AlwaysFree, Threshold: 20}
	if k := m.cloud.FreeKind(); k == "" || k == "paid" {
		r.Applies = "no" // fora da Oracle, ou shape pago: a regra de ociosidade do Always Free não vale
	}
	from := now.Add(-7 * 24 * time.Hour).Unix()
	cpu := m.st.Host.Values(2, "cpu", from)
	mem := m.st.Host.Values(2, "mem", from)
	rx := m.st.Host.Values(2, "rx", from)
	tx := m.st.Host.Values(2, "tx", from)
	if len(cpu) == 0 || m.hostNow.MemTotal == 0 {
		return r
	}
	r.DataDays = float64(len(cpu)) * 300 / 86400
	r.Ready = r.DataDays >= 6.9
	r.CPUP95 = percentile(cpu, 95)
	r.MemAvg = mean(mem) / float64(m.hostNow.MemTotal) * 100
	bw := m.cfg.Limits.GbpsPerOCPU * float64(max(1, m.hostNow.Cores)) * 1e9 / 8 // bytes/s do shape
	if m.cloud.NetGbps > 0 {
		bw = m.cloud.NetGbps * 1e9 / 8 // banda informada pela própria Oracle
	}
	var net []float64
	for i := range rx {
		if i < len(tx) {
			net = append(net, rx[i]+tx[i])
		}
	}
	r.NetAvg = mean(net) / bw * 100
	r.Idle = r.CPUP95 < r.Threshold && r.MemAvg < r.Threshold && r.NetAvg < r.Threshold
	return r
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var t float64
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

var exitCode = regexp.MustCompile(`^Exited \((\d+)\)`)

func (m *Monitor) alerts(now time.Time, tr TrafficSummary, lim LimitsView) []Alert {
	out := []Alert{}
	add := func(key, level, area, title, detail, target string) {
		out = append(out, Alert{Key: key, Level: level, Area: area, Title: title, Detail: detail, Target: target})
	}
	h := m.hostNow
	recent := func(field string) float64 { return mean(m.st.Host.Values(0, field, now.Add(-5*time.Minute).Unix())) }

	if !m.dockerOK {
		add("monitor.docker", "warn", "monitor", "Sem acesso ao Docker", "O painel não está conseguindo falar com o proxy do Docker: "+m.dockerErr, "")
	}
	if c := recent("cpu"); c >= 90 {
		add("host.cpu", "crit", "host", fmt.Sprintf("CPU em %.0f%% nos últimos 5 min", c), "O servidor está no limite. Veja em Aplicações quem está consumindo.", "")
	} else if c >= 75 {
		add("host.cpu", "warn", "host", fmt.Sprintf("CPU alta: %.0f%% nos últimos 5 min", c), "Veja em Aplicações quem está consumindo.", "")
	}
	if s := recent("steal"); s >= 10 {
		add("host.steal", "warn", "host", fmt.Sprintf("CPU roubada pela Oracle: %.0f%%", s), "\"Steal\" é tempo em que o hipervisor deu a CPU para outra VM. Não é culpa das apps.", "")
	}
	if h.MemTotal > 0 {
		avail := float64(h.MemAvailable) / float64(h.MemTotal) * 100
		if avail < 5 {
			add("host.mem", "crit", "host", fmt.Sprintf("Memória quase esgotada: %.0f%% livre", avail), "O kernel pode começar a matar processos (OOM).", "")
		} else if avail < 12 {
			add("host.mem", "warn", "host", fmt.Sprintf("Memória apertada: %.0f%% livre", avail), "", "")
		}
	}
	if h.SwapTotal > 0 && float64(h.SwapUsed)/float64(h.SwapTotal) > 0.5 {
		add("host.swap", "warn", "host", "Swap acima de 50%", "A máquina está usando disco como memória — fica lenta.", "")
	}
	if h.PSI.MemSome60 >= 10 {
		add("host.psimem", "warn", "host", fmt.Sprintf("Pressão de memória: %.0f%% do tempo esperando RAM", h.PSI.MemSome60), "", "")
	}
	if h.PSI.IOFull10 >= 25 {
		add("host.psiio", "warn", "host", fmt.Sprintf("Disco travando: %.0f%% do tempo tudo parado esperando E/S", h.PSI.IOFull10), "", "")
	}
	if h.FSTotal > 0 {
		p := float64(h.FSUsed) / float64(h.FSTotal) * 100
		if p >= 90 {
			add("host.disk", "crit", "host", fmt.Sprintf("Disco %.0f%% cheio", p), "Libere espaço (imagens velhas, logs) antes que banco e Docker parem.", "")
		} else if p >= 80 {
			add("host.disk", "warn", "host", fmt.Sprintf("Disco %.0f%% cheio", p), "", "")
		}
		if days := m.daysUntilFull(now); days > 0 && days < 30 {
			add("host.diskdays", "warn", "host", fmt.Sprintf("No ritmo da última semana, o disco enche em ~%.0f dias", days), "", "")
		}
	}
	for _, e := range m.st.Events {
		if e.Action == "oom_kill_host" && e.T > now.Add(-24*time.Hour).Unix() {
			add("host.oomkill", "crit", "host", "O kernel matou um processo por falta de memória", "Aconteceu "+ago(now, e.T)+". Veja os eventos em Sistema.", "")
			break
		}
	}

	// contêineres (parada pedida — deploy, docker stop — não conta como queda)
	restarts := map[string]int{}
	ooms := map[string]bool{}
	for _, e := range docker.MarkRequested(m.st.Events) {
		if e.T < now.Add(-24*time.Hour).Unix() {
			continue
		}
		if e.Action == "die" && e.ExitCode != "0" && !e.Requested {
			restarts[e.Container]++
		}
		if e.Action == "oom" {
			ooms[e.Container] = true
		}
	}
	for _, a := range m.apps {
		for _, u := range a.Units {
			c := u.Container
			if c == nil || c.State == "paused" {
				continue // pausado de propósito (botão Pausar): não é problema
			}
			switch {
			case c.Health == "unhealthy":
				add("app.unhealthy:"+c.Name, "crit", "app", c.Name+" está unhealthy", "O healthcheck do contêiner está falhando. Veja os logs.", u.Key)
			case c.State == "restarting":
				add("app.restarting:"+c.Name, "crit", "app", c.Name+" está reiniciando sem parar", "", u.Key)
			case c.State == "exited" || c.State == "dead":
				if mm := exitCode.FindStringSubmatch(c.Status); mm != nil && mm[1] != "0" {
					add("app.exited:"+c.Name, "warn", "app", c.Name+" parou com erro (código "+mm[1]+")", c.Status, u.Key)
				}
			}
			if n := restarts[c.Name]; n >= 2 {
				add("app.crashes:"+c.Name, "warn", "app", fmt.Sprintf("%s caiu %d vezes em 24 h", c.Name, n), "", u.Key)
			}
			if ooms[c.Name] {
				add("app.oom:"+c.Name, "crit", "app", c.Name+" foi morto por falta de memória (OOM)", "Bateu no limite de memória do contêiner.", u.Key)
			}
			if u.MemLimit > 0 && float64(u.Mem)/float64(u.MemLimit) > 0.9 {
				add("app.memlimit:"+c.Name, "warn", "app", fmt.Sprintf("%s usando %.0f%% do limite de memória", c.Name, float64(u.Mem)/float64(u.MemLimit)*100), "", u.Key)
			}
			if u.CPULimit > 0 && u.throttledAvg >= 40 {
				add("app.cpulimit:"+c.Name, "info", "app", fmt.Sprintf("%s batendo no limite de CPU (%s núcleo)", c.Name, dec1(u.CPULimit)),
					fmt.Sprintf("Em %.0f%% dos últimos intervalos o kernel segurou o contêiner. Ele fica mais lento, mas não quebra.", u.throttledAvg), u.Key)
			}
			if u.LogErrors >= 10 {
				add("app.logerrors:"+c.Name, "info", "app", fmt.Sprintf("%s: %d linhas de erro no log na última hora", c.Name, u.LogErrors), "", u.Key)
			}
		}
	}

	out = append(out, m.infos(now, lim)...)
	if m.extra != nil {
		out = append(out, m.extra()...) // de fora do monitor (ex.: WhatsApp das notificações desconectado)
	}

	// limites do plano gratuito
	for _, it := range lim.Items {
		if it.Key == "egress" && (it.Level == "warn" || it.Level == "crit") {
			add("limit.egress", it.Level, "limite", fmt.Sprintf("Saída do mês em %.0f%% do grátis (%s TB)", it.Used/it.Limit*100, dec(m.cfg.Limits.EgressTB, 0)), "Passando de 10 TB, a Oracle cobra o excedente.", "")
		}
	}
	if tr.ProjectedTx > tr.LimitBytes {
		add("limit.egressproj", "warn", "limite", "No ritmo atual, a saída do mês passa de "+dec(m.cfg.Limits.EgressTB, 0)+" TB", "", "")
	}
	if lim.Kind == "paid" {
		add("limit.paid", "info", "limite", "Esta VM ("+lim.Shape+") não é Always Free",
			"Só VM.Standard.A1.Flex e VM.Standard.E2.1.Micro são grátis. Se não for de propósito, confira o Cost Analysis da Oracle.", "")
	}
	if r := lim.Reclaim; r.Ready && r.Idle && r.Applies != "no" {
		add("limit.idle", "warn", "limite", "VM parece ociosa pela regra do Always Free",
			"CPU (p95), memória e rede ficaram abaixo de 20% em 7 dias. Em conta Always Free a Oracle pode recuperar a VM. Veja Limites.", "")
	}

	rank := map[string]int{"crit": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Level] < rank[out[j].Level] })
	return out
}

// infos são as observações da aba Infos: nada quebrado, mas vale saber.
func (m *Monitor) infos(now time.Time, lim LimitsView) []Alert {
	var out []Alert
	st := m.stor.view

	// cache de build do Docker
	if st.BuildCache >= 1<<30 {
		title := "Cache de build do Docker: " + size(st.BuildCache)
		if m.hostNow.FSUsed > 0 { // logo depois de subir, o disco ainda não foi lido
			title += fmt.Sprintf(" (%.0f%% do disco usado)", float64(st.BuildCache)/float64(m.hostNow.FSUsed)*100)
		}
		out = append(out, Alert{Key: "info.buildcache", Level: "info", Area: "disco",
			Title:  title,
			Detail: "Sobra dos builds dos deploys de todas as apps do servidor. Dá para liberar sem afetar nenhuma app no ar — o próximo build de cada uma só fica mais lento.",
			Action: "no servidor: docker builder prune -a"})
	}

	// imagens e volumes sem uso
	if st.UnusedImages >= 100<<20 {
		var items []string
		for _, im := range m.df.Images {
			if im.Containers == 0 && len(items) < 8 {
				items = append(items, fmt.Sprintf("%s — %s", strings.Join(im.Tags, ", "), size(uint64(max(0, im.Size)))))
			}
		}
		out = append(out, Alert{Key: "info.images", Level: "info", Area: "disco", Title: "Imagens sem nenhum contêiner: " + size(st.UnusedImages),
			Detail: "Versões antigas ou imagens que ninguém usa mais.", Items: items, Action: "no servidor: docker image prune -a"})
	}
	var unusedVol []string
	var unusedVolSize int64
	for _, v := range m.df.Volumes {
		if v.Refs == 0 {
			unusedVol = append(unusedVol, fmt.Sprintf("%s — %s", v.Name, size(uint64(v.Size))))
			unusedVolSize += v.Size
		}
	}
	if len(unusedVol) > 0 {
		out = append(out, Alert{Key: "info.volumes", Level: "info", Area: "disco",
			Title:  fmt.Sprintf("%d volume(s) sem contêiner: %s", len(unusedVol), size(uint64(unusedVolSize))),
			Detail: "Pode ser dado antigo de uma app removida — confira antes de apagar.", Items: unusedVol})
	}

	// logs grandes (sem rotação o arquivo só cresce)
	type logItem struct {
		name string
		size uint64
	}
	logsByApp := map[string][]logItem{}
	for _, a := range m.apps {
		for _, u := range a.Units {
			if u.Disk != nil && u.Disk.Logs >= 100<<20 {
				logsByApp[a.Name] = append(logsByApp[a.Name], logItem{u.Name, u.Disk.Logs})
			}
		}
	}
	for app, list := range logsByApp {
		sort.Slice(list, func(i, j int) bool { return list[i].size > list[j].size })
		var total uint64
		var items []string
		for _, l := range list {
			total += l.size
			items = append(items, fmt.Sprintf("%s — %s", l.name, size(l.size)))
		}
		lvl := "info"
		if total >= 2<<30 {
			lvl = "warn"
		}
		out = append(out, Alert{Key: "info.logs:" + app, Level: lvl, Area: "disco", Title: fmt.Sprintf("Logs do %s: %s e crescendo", app, size(total)),
			Detail: "Esses contêineres não têm rotação de log, então o arquivo só cresce até o disco acabar.",
			Items:  items, Action: "no compose da app: logging: {driver: json-file, options: {max-size: \"10m\", max-file: \"3\"}}"})
	}

	// contêineres sem limite de memória / sem healthcheck
	noLimit, noHealth := map[string][]string{}, map[string][]string{}
	var nNoLimit, nNoHealth int
	for _, a := range m.apps {
		if a.Kind == "temp" {
			continue
		}
		for _, u := range a.Units {
			c := u.Container
			if c == nil || c.State != "running" {
				continue
			}
			if u.MemLimit == 0 {
				noLimit[a.Name] = append(noLimit[a.Name], c.Name)
				nNoLimit++
			}
			if c.Health == "" {
				noHealth[a.Name] = append(noHealth[a.Name], c.Name)
				nNoHealth++
			}
		}
	}
	group := func(m map[string][]string) []string {
		var items []string
		for app, list := range m {
			sort.Strings(list)
			items = append(items, app+": "+strings.Join(list, ", "))
		}
		sort.Strings(items)
		return items
	}
	if nNoLimit > 0 {
		out = append(out, Alert{Key: "info.nolimit", Level: "info", Area: "app", Title: fmt.Sprintf("%d contêineres sem limite de memória", nNoLimit),
			Detail: "Se um deles vazar memória, pode ocupar a RAM do servidor inteiro e o kernel começa a matar processos de qualquer app.",
			Items:  group(noLimit), Action: "no compose: mem_limit (ex.: mem_limit: 512m)"})
	}
	if nNoHealth > 0 {
		out = append(out, Alert{Key: "info.nohealth", Level: "info", Area: "app", Title: fmt.Sprintf("%d contêineres sem healthcheck", nNoHealth),
			Detail: "Sem healthcheck, o Docker (e o painel) só sabem que o processo está de pé, não se ele responde.",
			Items:  group(noHealth), Action: "no compose: healthcheck: {test: [...], interval: 30s}"})
	}

	// disco crescendo
	if g := m.diskGrowth(now); g >= 200<<20 {
		days := float64(m.hostNow.FSAvail) / g
		out = append(out, Alert{Key: "info.diskgrowth", Level: "info", Area: "disco", Title: fmt.Sprintf("O disco cresce ~%s por dia", size(uint64(g))),
			Detail: fmt.Sprintf("Média da última semana. Nesse ritmo, os %s livres duram ~%.0f dias.", size(m.hostNow.FSAvail), days)})
	}

	// perto da regra de ociosidade da Oracle (só avisa quando está perto)
	if r := lim.Reclaim; r.Applies != "no" && r.DataDays >= 1 && !r.Idle && r.CPUP95 < 20 && r.NetAvg < 20 && r.MemAvg < 25 {
		out = append(out, Alert{Key: "info.idlenear", Level: "info", Area: "limite", Title: fmt.Sprintf("Perto da regra de VM ociosa da Oracle (memória em %.0f%%)", r.MemAvg),
			Detail: fmt.Sprintf("CPU p95 %.0f%% e rede %s%% já estão abaixo de 20%%; só a memória (%.0f%%) segura a VM fora da regra. Só importa se a conta for Always Free.", r.CPUP95, dec1(r.NetAvg), r.MemAvg),
			Action: "se a conta for Pay As You Go, ponha VPMON_ALWAYS_FREE=no no .env do painel"})
	}

	// contêineres parados há tempos
	var stopped []string
	for _, a := range m.apps {
		for _, u := range a.Units {
			if c := u.Container; c != nil && (c.State == "exited" || c.State == "created") && a.Kind != "temp" {
				if mm := exitCode.FindStringSubmatch(c.Status); mm == nil || mm[1] == "0" {
					stopped = append(stopped, fmt.Sprintf("%s — %s", c.Name, c.Status))
				}
			}
		}
	}
	if len(stopped) > 0 {
		sort.Strings(stopped)
		out = append(out, Alert{Key: "info.stopped", Level: "info", Area: "app", Title: fmt.Sprintf("%d contêiner(es) parado(s)", len(stopped)),
			Detail: "Pararam sem erro. Se não forem mais usados, dá para removê-los.", Items: stopped})
	}
	return out
}

// diskGrowth devolve quantos bytes por dia o disco cresceu na última semana (0 = sem dados).
func (m *Monitor) diskGrowth(now time.Time) float64 {
	v := m.st.Host.Values(2, "fs", now.Add(-7*24*time.Hour).Unix())
	if len(v) < 288 { // pelo menos 1 dia de dados
		return 0
	}
	days := float64(len(v)) * 300 / 86400
	return (v[len(v)-1] - v[0]) / days
}

// size formata bytes como na tela (base 1024, vírgula decimal).
func size(b uint64) string {
	f := float64(b)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 || f >= 100 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return dec1(f) + " " + units[i]
}

// daysUntilFull estima pela inclinação do uso de disco nos últimos 7 dias.
func (m *Monitor) daysUntilFull(now time.Time) float64 {
	growth := m.diskGrowth(now)
	if growth <= 0 {
		return 0
	}
	return float64(m.hostNow.FSAvail) / growth
}

// dec1 formata com uma casa e vírgula decimal (19,5).
func dec1(v float64) string { return strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1) }

func ago(now time.Time, t int64) string {
	d := now.Sub(time.Unix(t, 0))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	}
	return fmt.Sprintf("há %d dias", int(d.Hours()/24))
}

// --- histórico -------------------------------------------------------------------------------

var ranges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "1y": 365 * 24 * time.Hour,
}

func RangeOK(r string) bool { _, ok := ranges[r]; return ok }

// HostHistory devolve campos do host no período.
func (m *Monitor) HostHistory(rng string, fields []string) store.Result {
	m.mu.RLock()
	defer m.mu.RUnlock()
	to := time.Now().Unix()
	return m.st.Host.Query(to-int64(ranges[rng].Seconds()), to, 1500, fields...)
}

// UnitHistory devolve a série de um contêiner, serviço ou app ("c:..", "s:..", "app:..").
func (m *Monitor) UnitHistory(key, rng string) (store.Result, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.st.Units[key]
	if s == nil {
		return store.Result{}, false
	}
	to := time.Now().Unix()
	return s.Query(to-int64(ranges[rng].Seconds()), to, 1500), true
}

type AppSeries struct {
	Key   string     `json:"key"`
	Name  string     `json:"name"`
	Color int        `json:"color"`
	V     store.Nums `json:"v"`
}

type AppsHistory struct {
	Step   int64       `json:"step"`
	T      []int64     `json:"t"`
	Series []AppSeries `json:"series"`
}

// AppsHistory devolve um campo (cpu, mem, rx...) de todas as apps, numa linha do tempo comum.
func (m *Monitor) AppsHistory(field, rng string) AppsHistory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	to := time.Now().Unix()
	from := to - int64(ranges[rng].Seconds())
	res := map[string]store.Result{}
	tset := map[int64]bool{}
	var step int64
	for k, s := range m.st.Units {
		if !strings.HasPrefix(k, "app:") || s.Last < from {
			continue
		}
		r := s.Query(from, to, 0, field)
		res[strings.TrimPrefix(k, "app:")] = r
		step = max(step, r.Step)
		for _, t := range r.T {
			tset[t] = true
		}
	}
	out := AppsHistory{Step: step, T: make([]int64, 0, len(tset)), Series: []AppSeries{}}
	for t := range tset {
		out.T = append(out.T, t)
	}
	sort.Slice(out.T, func(i, j int) bool { return out.T[i] < out.T[j] })
	pos := map[int64]int{}
	for i, t := range out.T {
		pos[t] = i
	}
	for k, r := range res {
		v := make(store.Nums, len(out.T))
		for i := range v {
			v[i] = math.NaN()
		}
		for i, t := range r.T {
			v[pos[t]] = r.Cols[field][i]
		}
		out.Series = append(out.Series, AppSeries{Key: k, Name: m.appName(k), Color: m.colorOf(k), V: v})
	}
	sort.Slice(out.Series, func(i, j int) bool {
		a := AppView{Key: out.Series[i].Key, Name: out.Series[i].Name, Self: out.Series[i].Key == m.cfg.SelfProject}
		b := AppView{Key: out.Series[j].Key, Name: out.Series[j].Name, Self: out.Series[j].Key == m.cfg.SelfProject}
		return appLess(a, b)
	})
	return out
}

// --- banda ---------------------------------------------------------------------------------

type DayTraffic struct {
	Day string `json:"d"`
	store.RxTx
}

type KeyTraffic struct {
	Key       string     `json:"key"`
	Name      string     `json:"name"`
	App       string     `json:"app,omitempty"`
	Color     int        `json:"color"`
	Today     store.RxTx `json:"today"`
	Month     store.RxTx `json:"month"`
	LastMonth store.RxTx `json:"lastMonth"`
	Total     store.RxTx `json:"total"`
}

type TrafficView struct {
	Summary    TrafficSummary `json:"summary"`
	Days       []DayTraffic   `json:"days"`
	Months     []DayTraffic   `json:"months"`
	Apps       []KeyTraffic   `json:"apps"`
	Containers []KeyTraffic   `json:"containers"`
}

func (m *Monitor) Traffic() TrafficView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now()
	t := m.st.Traffic
	v := TrafficView{Summary: m.trafficSummary(now), Apps: []KeyTraffic{}, Containers: []KeyTraffic{}}
	for i := 59; i >= 0; i-- {
		d := m.day(now.AddDate(0, 0, -i))
		v.Days = append(v.Days, DayTraffic{d, t.Day("host", d)})
	}
	loc := now.In(m.cfg.Loc)
	first := time.Date(loc.Year(), loc.Month(), 1, 12, 0, 0, 0, m.cfg.Loc)
	for i := 11; i >= 0; i-- {
		mo := first.AddDate(0, -i, 0).Format("2006-01")
		v.Months = append(v.Months, DayTraffic{mo, t.Month("host", mo)})
	}
	day, month, last := m.day(now), m.month(now), m.month(first.AddDate(0, 0, -1))
	kt := func(key, name, app string, color int) KeyTraffic {
		return KeyTraffic{Key: key, Name: name, App: app, Color: color, Today: t.Day(key, day),
			Month: t.Month(key, month), LastMonth: t.Month(key, last), Total: t.Total(key)}
	}
	for _, k := range t.Keys("app:") {
		a := strings.TrimPrefix(k, "app:")
		v.Apps = append(v.Apps, kt(k, m.appName(a), a, m.colorOf(a)))
	}
	appOf := map[string]string{}
	for _, c := range m.containers {
		appOf["c:"+c.Name] = m.appKey(c)
	}
	for _, k := range t.Keys("c:") {
		a := appOf[k]
		v.Containers = append(v.Containers, kt(k, strings.TrimPrefix(k, "c:"), a, m.colorOf(a)))
	}
	sort.Slice(v.Apps, func(i, j int) bool { return v.Apps[i].Month.Tx > v.Apps[j].Month.Tx })
	sort.Slice(v.Containers, func(i, j int) bool { return v.Containers[i].Month.Tx > v.Containers[j].Month.Tx })
	return v
}

// --- logs ----------------------------------------------------------------------------------

type LogLineView struct {
	docker.LogLine
	C     string `json:"c"`
	Error bool   `json:"e,omitempty"`
}

type LogTarget struct {
	Name       string           `json:"name"`
	App        string           `json:"app"`
	AppName    string           `json:"appName"`
	Color      int              `json:"color"`
	State      string           `json:"state"`
	Lines      int              `json:"lines1h"`
	Errors     int              `json:"errors1h"`
	Errors24   int              `json:"errors24h"`
	LastErrors []docker.LogLine `json:"lastErrors"`
}

// LogTargets lista os contêineres que têm log, com a atividade recente.
func (m *Monitor) LogTargets() []LogTarget {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now().Unix()
	out := []LogTarget{}
	for _, c := range m.containers {
		k := m.appKey(c)
		t := LogTarget{Name: c.Name, App: k, AppName: m.appName(k), Color: m.colorOf(k), State: c.State, LastErrors: []docker.LogLine{}}
		if ls := m.st.LogStats[c.Name]; ls != nil {
			t.Lines, t.Errors = logLastHour(ls, now)
			for _, ch := range ls.Checks {
				t.Errors24 += ch.Errors
			}
			t.LastErrors = append(t.LastErrors, ls.LastErrors...)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AppName != out[j].AppName {
			return out[i].AppName < out[j].AppName
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Logs lê as últimas linhas de um contêiner, ou de todos ("*") misturados por horário.
func (m *Monitor) Logs(ctx context.Context, name string, tail int, onlyErrors bool) ([]LogLineView, error) {
	m.mu.RLock()
	var targets []docker.Container
	for _, c := range m.containers {
		if (name == "*" && c.State == "running") || c.Name == name {
			targets = append(targets, c)
		}
	}
	m.mu.RUnlock()
	if len(targets) == 0 {
		return nil, fmt.Errorf("contêiner não encontrado")
	}
	per := tail
	if name == "*" {
		per = min(tail, 200)
	}
	if onlyErrors {
		per = min(per*10, 5000) // lê mais para sobrar o suficiente depois do filtro
	}
	var out []LogLineView
	for _, c := range targets {
		lines, err := m.dc.Logs(ctx, c.ID, per, 0)
		if err != nil {
			if name != "*" {
				return nil, err
			}
			continue
		}
		for _, l := range lines {
			isErr := errorLine.MatchString(l.Msg)
			if onlyErrors && !isErr {
				continue
			}
			if len(l.Msg) > 4000 {
				l.Msg = l.Msg[:4000] + "…"
			}
			l.Msg = MaskURLPassword(l.Msg) // ex.: a Evolution escreve a URL do banco com a senha ao subir
			out = append(out, LogLineView{LogLine: l, C: c.Name, Error: isErr})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	if len(out) > tail {
		out = out[len(out)-tail:]
	}
	return out, nil
}

// --- sistema -----------------------------------------------------------------------------

type ProcView struct {
	procs.Proc
	UnitName string `json:"unitName"`
}

type SystemView struct {
	Version  string           `json:"version"`
	Server   Server           `json:"server"`
	Info     docker.Info      `json:"info"`
	Host     host.Sample      `json:"host"`
	ByCPU    []ProcView       `json:"byCpu"`
	ByMem    []ProcView       `json:"byMem"`
	Services []UnitView       `json:"services"`
	Disk     docker.DiskUsage `json:"disk"`
	Events   []docker.Event   `json:"events"`
	Warming  bool             `json:"warming"` // processos ainda sem a 2ª leitura
}

// System devolve a aba Sistema e marca que alguém está olhando (liga a amostragem de processos).
func (m *Monitor) System() SystemView {
	m.mu.Lock()
	warming := time.Since(m.lastView) > time.Minute
	m.lastView = time.Now()
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	v := SystemView{Version: m.cfg.Version, Server: m.serverInfo(), Info: m.info, Host: m.hostNow, Disk: m.df,
		Warming: warming || len(m.procList) == 0, Services: []UnitView{}}
	unitName := map[string]string{}
	unitApp := map[string]string{}
	for key, u := range m.units {
		unitName[key], unitApp[key] = u.Name, u.App
		if u.Kind != "container" {
			v.Services = append(v.Services, *u)
		}
	}
	sort.Slice(v.Services, func(i, j int) bool {
		if v.Services[i].CPU != v.Services[j].CPU {
			return v.Services[i].CPU > v.Services[j].CPU
		}
		return v.Services[i].Mem > v.Services[j].Mem
	})
	byCPU, byMem := procs.Top(m.procList, 15)
	conv := func(ps []procs.Proc) []ProcView {
		out := make([]ProcView, 0, len(ps))
		for _, p := range ps {
			pv := ProcView{Proc: p, UnitName: unitName[p.Cgroup]}
			pv.App = unitApp[p.Cgroup]
			if pv.UnitName == "" {
				pv.UnitName = p.Cgroup
			}
			out = append(out, pv)
		}
		return out
	}
	v.ByCPU, v.ByMem = conv(byCPU), conv(byMem)
	ev := docker.MarkRequested(m.st.Events)
	if len(ev) > 150 {
		ev = ev[len(ev)-150:]
	}
	v.Events = make([]docker.Event, 0, len(ev))
	for i := len(ev) - 1; i >= 0; i-- {
		v.Events = append(v.Events, ev[i])
	}
	return v
}

// ContainerExists diz se o nome é de um contêiner conhecido (valida a entrada dos logs).
func (m *Monitor) ContainerExists(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.containers {
		if c.Name == name {
			return true
		}
	}
	return false
}
