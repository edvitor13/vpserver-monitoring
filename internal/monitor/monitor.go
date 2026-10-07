// Package monitor junta tudo: amostra o host e as unidades a cada intervalo,
// agrupa por aplicação, soma a banda, conta erros de log e calcula alertas.
//
// As aplicações são descobertas sozinhas: cada projeto do Docker Compose vira
// uma aplicação; contêiner solto vira a própria aplicação; serviços do host
// ficam em "Sistema". Nada precisa ser cadastrado quando surgir uma app nova.
package monitor

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/cgroups"
	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/host"
	"github.com/edvitor13/vpserver-monitoring/internal/procs"
	"github.com/edvitor13/vpserver-monitoring/internal/store"
)

const (
	AppSystem = "_system" // serviços do host
	AppKernel = "_kernel" // o que sobra: kernel, buffers, processos fora de cgroup
	AppTemp   = "_temp"   // contêineres avulsos de vida curta (docker run --rm, jobs de CI)
)

// autoName reconhece nome gerado pelo Docker ("beautiful_moser") ou um ID
// curto (contêiner que sumiu antes de aparecer na lista).
var autoName = regexp.MustCompile(`^([a-z]+_[a-z]+|[0-9a-f]{12})$`)

var (
	hostFields = []string{"cpu", "user", "system", "iowait", "steal", "load1", "mem", "cache", "swap",
		"rx", "tx", "rd", "wr", "util", "iops", "fs", "psicpu", "psimem", "psiio", "tcp"}
	hostTiers = []store.TierSpec{{Step: 5, Cap: 720}, {Step: 60, Cap: 1440}, {Step: 300, Cap: 8640}, {Step: 3600, Cap: 8784}}

	unitFields = []string{"cpu", "mem", "rx", "tx", "rd", "wr"}
	unitTiers  = []store.TierSpec{{Step: 5, Cap: 720}, {Step: 60, Cap: 1440}, {Step: 1800, Cap: 1440}}

	errorLine = regexp.MustCompile(`(?i)\b(error|errors|exception|traceback|fatal|panic|critical|crit|emerg)\b|"status":\s*5\d\d|" 5\d\d `)
)

type Limits struct {
	EgressTB    float64 // saída gratuita por mês (Oracle: 10 TB)
	FreeOCPU    float64 // Always Free Ampere A1: 4 OCPUs no total
	FreeMemGB   float64 // 24 GB no total
	FreeDiskGB  float64 // 200 GB de volumes de bloco no total
	GbpsPerOCPU float64 // banda do shape A1: 1 Gbps por OCPU
	AlwaysFree  string  // "unknown", "yes" ou "no" (conta Pay As You Go não sofre recuperação por ociosidade)
}

type Config struct {
	Interval    time.Duration
	Proc        string
	Sys         string
	Cgroup      string
	DataDir     string
	DockerAddr  string
	Loc         *time.Location
	AppNames    map[string]string
	SelfProject string
	ServerName  string
	Limits      Limits
	Version     string
	LogSizes    string // arquivo do vpserver-sizer com o tamanho dos logs do Docker
}

// UnitView é o estado atual de um contêiner ou serviço.
type UnitView struct {
	Key       string            `json:"key"` // c:<contêiner> | s:<unidade>
	Name      string            `json:"name"`
	Desc      string            `json:"desc,omitempty"`
	Kind      string            `json:"kind"`
	App       string            `json:"app"`
	CPU       float64           `json:"cpu"`      // % da máquina
	CPUCores  float64           `json:"cpuCores"` // núcleos em uso
	CPULimit  float64           `json:"cpuLimit"` // limite em núcleos (0 = sem)
	Throttled float64           `json:"throttled"`
	Mem       uint64            `json:"mem"`
	MemLimit  uint64            `json:"memLimit"`
	HasNet    bool              `json:"hasNet"`
	NetRx     float64           `json:"netRx"`
	NetTx     float64           `json:"netTx"`
	IORead    float64           `json:"ioRead"`
	IOWrite   float64           `json:"ioWrite"`
	Pids      uint64            `json:"pids"`
	Today     store.RxTx        `json:"today"`
	Month     store.RxTx        `json:"month"`
	LogLines  int               `json:"logLines1h"`
	LogErrors int               `json:"logErrors1h"`
	Container *docker.Container `json:"container,omitempty"`
	Disk      *Disk             `json:"disk,omitempty"`      // camada + volumes + logs (sem a imagem)
	ImageSize uint64            `json:"imageSize,omitempty"` // tamanho da imagem
	ImageUses int               `json:"imageUses,omitempty"` // quantos contêineres usam a mesma imagem

	throttledAvg float64
}

type AppView struct {
	Key        string      `json:"key"`
	Name       string      `json:"name"`
	Kind       string      `json:"kind"` // compose | standalone | system | kernel
	Color      int         `json:"color"`
	Self       bool        `json:"self,omitempty"`
	CPU        float64     `json:"cpu"`
	Mem        uint64      `json:"mem"`
	NetRx      float64     `json:"netRx"`
	NetTx      float64     `json:"netTx"`
	IORead     float64     `json:"ioRead"`
	IOWrite    float64     `json:"ioWrite"`
	Today      store.RxTx  `json:"today"`
	Month      store.RxTx  `json:"month"`
	Running    int         `json:"running"`
	Paused     int         `json:"paused"` // congelados pelo botão Pausar (docker pause)
	Total      int         `json:"total"`
	Unhealthy  int         `json:"unhealthy"`
	LogErrors  int         `json:"logErrors1h"`
	Status     string      `json:"status"` // ok | warn | crit | stopped | paused
	Disk       Disk        `json:"disk"`
	Components []Component `json:"components"`
	API        int         `json:"api"`        // instâncias da API (ex.: api1 + api2 = 2)
	APIRunning int         `json:"apiRunning"` // dessas, quantas no ar
	Units      []UnitView  `json:"units"`
}

type Monitor struct {
	cfg    Config
	hr     *host.Reader
	cr     *cgroups.Reader
	dc     *docker.Client
	ps     *procs.Sampler
	bootID string
	iface  string
	disks  map[string]uint64

	mu         sync.RWMutex
	st         *store.State
	prevHost   *host.Raw
	hostNow    host.Sample
	prevUnits  map[string]cgroups.Unit
	prevUnitsT time.Time
	units      map[string]*UnitView // por Key de unidade
	containers map[string]docker.Container
	apps       []AppView
	dockerOK   bool
	dockerErr  string
	info       docker.Info
	df         docker.DiskUsage
	cloud      Cloud // VM da Oracle (vazio fora dela)
	stor       storage
	procList   []procs.Proc
	lastView   time.Time // última vez que alguém abriu a aba Sistema
	started    time.Time
	lastSave   time.Time
	eventsTo   int64 // até onde os eventos já foram lidos (unix)
	oomBase    uint64
	busy       sync.Map         // tarefas lentas em andamento (logs, df)
	extra      func() []Alert   // alertas de fora do monitor (lidos com m.mu travado: não pode chamar o monitor)
	resumed    map[string]int64 // contêiner → quando foi retomado pelo botão (unix)
}

// SetExtraAlerts acrescenta alertas de fora do monitor (as notificações avisam
// quando o WhatsApp cai). Chame antes do Run. f roda com o monitor travado:
// tem de ser rápida e não pode chamar métodos do monitor.
func (m *Monitor) SetExtraAlerts(f func() []Alert) { m.extra = f }

func New(cfg Config) *Monitor {
	m := &Monitor{
		cfg:        cfg,
		hr:         &host.Reader{Proc: cfg.Proc, Sys: cfg.Sys, NetPID: "1", FSPath: cfg.DataDir},
		dc:         docker.New(cfg.DockerAddr),
		ps:         procs.NewSampler(cfg.Proc),
		prevUnits:  map[string]cgroups.Unit{},
		units:      map[string]*UnitView{},
		containers: map[string]docker.Container{},
		resumed:    map[string]int64{},
		started:    time.Now(),
	}
	m.iface = m.hr.DefaultIface()
	m.disks = m.hr.DiskSizes()
	m.cr = &cgroups.Reader{Root: cfg.Cgroup, Proc: cfg.Proc, HostIface: m.iface}
	if b, err := os.ReadFile(filepath.Join(cfg.Proc, "sys", "kernel", "random", "boot_id")); err == nil {
		m.bootID = strings.TrimSpace(string(b))
	}
	return m
}

func (m *Monitor) statePath() string { return filepath.Join(m.cfg.DataDir, "state.gob") }

func (m *Monitor) load() {
	st, err := store.Load(m.statePath())
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("estado salvo ilegível; começando do zero", "err", err)
		}
		st = store.NewState(time.Now().Unix())
	}
	if !st.Host.Compatible(hostFields, hostTiers) {
		st.Host = store.NewSeries(hostFields, hostTiers)
	}
	for k, s := range st.Units {
		if !s.Compatible(unitFields, unitTiers) {
			delete(st.Units, k)
		}
	}
	// eventos guardados antes da versão 2 não têm kill/stop: sem eles, todo
	// deploy pareceria queda. Descarta (o Docker devolve as últimas 24 h de novo).
	if st.EventsV < 2 {
		var keep []docker.Event
		for _, e := range st.Events {
			if e.Action == "oom_kill_host" {
				keep = append(keep, e)
			}
		}
		st.Events, st.EventsV = keep, 2
	}
	m.st = st
}

// Save grava o estado em disco.
// Version é a versão do painel (o commit do build).
func (m *Monitor) Version() string { return m.cfg.Version }

func (m *Monitor) Save() {
	m.mu.RLock()
	err := m.st.Save(m.statePath())
	m.mu.RUnlock()
	if err != nil {
		slog.Error("não consegui salvar o estado", "err", err)
	}
}

// Run amostra até o contexto acabar; salva ao sair.
func (m *Monitor) Run(ctx context.Context) {
	m.load()
	m.detectCloud(ctx)
	m.refreshContainers(ctx)
	m.refreshInfo(ctx)
	m.sample(ctx, time.Now()) // primeira leitura só marca a base
	go m.slow(ctx, "df", m.refreshDiskUsage)
	go m.slow(ctx, "logs", m.refreshLogStats)

	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	var n int
	for {
		select {
		case <-ctx.Done():
			m.Save()
			return
		case now := <-t.C:
			n++
			every := func(d time.Duration) bool { return n%int(max(1, d/m.cfg.Interval)) == 0 }
			if every(time.Minute) { // a lista tem ~50 KB; mudanças chegam antes pelos eventos (abaixo)
				m.refreshContainers(ctx)
			}
			m.sample(ctx, now)
			if every(30 * time.Second) {
				m.refreshEvents(ctx)
			}
			if every(5 * time.Minute) {
				go m.slow(ctx, "logs", m.refreshLogStats)
				m.refreshStorage()
			}
			if every(30 * time.Minute) {
				go m.slow(ctx, "df", m.refreshDiskUsage)
			}
			if every(time.Hour) {
				m.refreshInfo(ctx)
				m.detectCloud(ctx) // a VM pode ter mudado de shape
				m.prune(now)
			}
			if now.Sub(m.lastSave) >= 10*time.Minute {
				m.lastSave = now
				m.Save()
			}
		}
	}
}

// slow roda uma tarefa demorada sem deixar duas iguais se sobreporem.
func (m *Monitor) slow(ctx context.Context, name string, fn func(context.Context)) {
	if _, running := m.busy.LoadOrStore(name, true); running {
		return
	}
	defer m.busy.Delete(name)
	fn(ctx)
}

func (m *Monitor) day(t time.Time) string   { return t.In(m.cfg.Loc).Format("2006-01-02") }
func (m *Monitor) month(t time.Time) string { return t.In(m.cfg.Loc).Format("2006-01") }

// --- amostragem ------------------------------------------------------------------------

func (m *Monitor) sample(ctx context.Context, now time.Time) {
	raw, err := m.hr.Read()
	if err != nil {
		slog.Error("leitura do host falhou", "err", err)
		return
	}
	units := m.cr.Read()

	var plist []procs.Proc
	m.mu.RLock()
	viewing := time.Since(m.lastView) < time.Minute
	m.mu.RUnlock()
	if viewing {
		plist = m.ps.Sample(len(raw.PerCPU))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if viewing {
		m.procList = plist
	}
	day := m.day(now)
	ts := now.Unix()

	if m.prevHost != nil {
		s := host.Compute(m.prevHost, raw, m.iface)
		m.hostNow = s
		m.st.Host.Add(ts, []float64{s.CPU, s.CPUUser, s.CPUSystem, s.CPUIOWait, s.CPUSteal, s.Load1,
			float64(s.MemUsed), float64(s.MemCache), float64(s.SwapUsed), s.NetRx, s.NetTx,
			s.DiskRead, s.DiskWrite, s.DiskUtil, s.DiskIOPS, float64(s.FSUsed),
			s.PSI.CPUSome10, s.PSI.MemSome10, s.PSI.IOSome10, float64(s.TCP)})
		if m.oomBase == 0 {
			m.oomBase = raw.OOMKills + 1 // +1 para distinguir "ainda não lido" de zero
		} else if raw.OOMKills+1 > m.oomBase {
			m.st.Events = append(m.st.Events, docker.Event{T: ts, Action: "oom_kill_host",
				Container: "kernel", ExitCode: ""})
			m.oomBase = raw.OOMKills + 1
		}
	}
	if n, ok := raw.Net[m.iface]; ok {
		drx, dtx := m.st.Traffic.Observe("host", m.bootID, n.RxBytes, n.TxBytes)
		m.st.Traffic.Add("host", day, drx, dtx)
	}
	m.prevHost = raw

	m.sampleUnits(units, now, day)
}

func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func (m *Monitor) sampleUnits(units []cgroups.Unit, now time.Time, day string) {
	dt := now.Sub(m.prevUnitsT).Seconds()
	cores := float64(max(1, m.hostNow.Cores))
	ts := now.Unix()
	month := m.month(now)
	byID := map[string]docker.Container{}
	for _, c := range m.containers {
		byID[c.ID] = c
	}

	next := map[string]*UnitView{}
	cur := map[string]cgroups.Unit{}
	for _, u := range units {
		cur[u.Key] = u
		v := &UnitView{Kind: u.Kind, Mem: u.MemUsed(), MemLimit: u.MemMax, CPULimit: u.CPUQuota,
			Pids: u.Pids, HasNet: u.HasNet}
		if u.Kind == cgroups.KindContainer {
			c, ok := byID[u.ContainerID]
			if !ok {
				c = docker.Container{ID: u.ContainerID, Name: u.ContainerID[:min(12, len(u.ContainerID))], State: "running", Status: "temporário"}
			}
			cc := c
			v.Container, v.Name, v.Key = &cc, c.Name, "c:"+c.Name
			v.App = m.appKey(c)
		} else {
			v.Name, v.Desc = ServiceName(u.Key)
			v.Key, v.App = "s:"+u.Key, AppSystem
		}
		if old := m.units[u.Key]; old != nil {
			v.throttledAvg = old.throttledAvg
		}
		if p, ok := m.prevUnits[u.Key]; ok && dt > 0 {
			used := float64(sub(u.CPUUsec, p.CPUUsec)) / 1e6 / dt
			v.CPUCores, v.CPU = used, used/cores*100
			if per := sub(u.NrPeriods, p.NrPeriods); per > 0 {
				v.Throttled = float64(sub(u.NrThrottled, p.NrThrottled)) / float64(per) * 100
			}
			a := math.Min(1, dt/300) // média móvel de ~5 min
			v.throttledAvg = v.throttledAvg*(1-a) + v.Throttled*a
			v.IORead = float64(sub(u.IORead, p.IORead)) / dt
			v.IOWrite = float64(sub(u.IOWrite, p.IOWrite)) / dt
			if u.HasNet && p.HasNet {
				v.NetRx = float64(sub(u.NetRx, p.NetRx)) / dt
				v.NetTx = float64(sub(u.NetTx, p.NetTx)) / dt
			}
		}
		if u.Kind == cgroups.KindContainer && u.HasNet {
			drx, dtx := m.st.Traffic.Observe(v.Key, u.ContainerID, u.NetRx, u.NetTx)
			m.st.Traffic.Add(v.Key, day, drx, dtx)
			m.st.Traffic.Add("app:"+v.App, day, drx, dtx)
		}
		v.Today = m.st.Traffic.Day(v.Key, day)
		v.Month = m.st.Traffic.Month(v.Key, month)
		if ls := m.st.LogStats[v.Name]; ls != nil && v.Kind == cgroups.KindContainer {
			v.LogLines, v.LogErrors = logLastHour(ls, ts)
		}
		m.series(v.Key).Add(ts, []float64{v.CPU, float64(v.Mem), v.NetRx, v.NetTx, v.IORead, v.IOWrite})
		next[u.Key] = v
	}
	m.prevUnits, m.prevUnitsT, m.units = cur, now, next
	m.buildApps(ts, day, month)
}

func (m *Monitor) series(key string) *store.Series {
	s := m.st.Units[key]
	if s == nil {
		s = store.NewSeries(unitFields, unitTiers)
		m.st.Units[key] = s
	}
	return s
}

func logLastHour(ls *store.LogStat, now int64) (lines, errs int) {
	for _, c := range ls.Checks {
		if c.T > now-3600 {
			lines += c.Lines
			errs += c.Errors
		}
	}
	return
}

// appKey: projeto do Compose; contêiner solto com nome próprio vira a própria
// app; avulso de nome gerado (docker run --rm) vai para "temporários".
func (m *Monitor) appKey(c docker.Container) string {
	switch {
	case c.Project != "":
		return c.Project
	case autoName.MatchString(c.Name):
		return AppTemp
	}
	return c.Name
}

func (m *Monitor) appName(key string) string {
	switch key {
	case AppSystem:
		return "Sistema (host)"
	case AppKernel:
		return "Kernel e outros"
	case AppTemp:
		return "Contêineres temporários"
	}
	if n, ok := m.cfg.AppNames[key]; ok && n != "" {
		return n
	}
	if key == m.cfg.SelfProject {
		return "Monitor (este painel)"
	}
	return PrettyName(key)
}

// colorOf só lê a cor já atribuída (seguro sob RLock); -3 = cinza de "outras".
func (m *Monitor) colorOf(key string) int {
	switch key {
	case AppSystem:
		return -1
	case AppKernel:
		return -2
	case AppTemp:
		return -3
	}
	if c, ok := m.st.Colors[key]; ok {
		return c
	}
	return -3
}

// color dá a cada app uma posição fixa na paleta (a cor segue a app, nunca o
// ranking). Só chamar com o lock de escrita.
func (m *Monitor) color(key string) int {
	switch key {
	case AppSystem, AppKernel, AppTemp:
		return m.colorOf(key)
	}
	if c, ok := m.st.Colors[key]; ok {
		return c
	}
	used := map[int]bool{}
	for _, c := range m.st.Colors {
		used[c] = true
	}
	c := -3 // paleta esgotada: cinza de "outras"
	for i := 0; i < 8; i++ {
		if !used[i] {
			c = i
			break
		}
	}
	m.st.Colors[key] = c
	return c
}

func (m *Monitor) buildApps(ts int64, day, month string) {
	apps := map[string]*AppView{}
	get := func(key, kind string) *AppView {
		a := apps[key]
		if a == nil {
			if key == AppTemp {
				kind = "temp"
			}
			a = &AppView{Key: key, Name: m.appName(key), Kind: kind, Color: m.colorOf(key), Self: key == m.cfg.SelfProject, Units: []UnitView{}, Components: []Component{}}
			apps[key] = a
		}
		return a
	}
	seen := map[string]bool{}
	for _, v := range m.units {
		kind := "system"
		if v.Kind == cgroups.KindContainer {
			kind = "compose"
			if v.Container != nil && v.Container.Project == "" {
				kind = "standalone"
			}
			seen[v.Name] = true
		}
		a := get(v.App, kind)
		a.Units = append(a.Units, *v)
	}
	// contêineres parados (sem cgroup) também aparecem
	for _, c := range m.containers {
		if seen[c.Name] {
			continue
		}
		kind := "compose"
		if c.Project == "" {
			kind = "standalone"
		}
		cc := c
		k := m.appKey(c)
		v := UnitView{Key: "c:" + c.Name, Name: c.Name, Kind: cgroups.KindContainer, App: k, Container: &cc,
			Today: m.st.Traffic.Day("c:"+c.Name, day), Month: m.st.Traffic.Month("c:"+c.Name, month)}
		get(k, kind).Units = append(get(k, kind).Units, v)
	}

	// cores atribuídas em ordem fixa (apps de verdade primeiro, por nome), não na ordem do mapa
	order := make([]*AppView, 0, len(apps))
	for _, a := range apps {
		order = append(order, a)
	}
	sort.Slice(order, func(i, j int) bool { return appLess(*order[i], *order[j]) })
	for _, a := range order {
		a.Color = m.color(a.Key)
	}

	// disco (da última medição do Docker) e instâncias de cada app
	byApp := map[string][]docker.Container{}
	for _, c := range m.containers {
		k := m.appKey(c)
		byApp[k] = append(byApp[k], c)
	}
	for _, a := range apps {
		a.Disk = m.stor.apps[a.Key]
		if cs := byApp[a.Key]; len(cs) > 0 {
			a.Components = components(cs)
			for _, c := range a.Components {
				if c.API {
					a.API += c.Count
					a.APIRunning += c.Running
				}
			}
		}
		for i := range a.Units {
			u := &a.Units[i]
			if u.Container == nil {
				continue
			}
			if d, ok := m.stor.units[u.Name]; ok {
				dd := d
				u.Disk = &dd
				u.ImageSize, u.ImageUses = m.stor.imgSize[u.Name], m.stor.imgUses[u.Name]
			}
		}
	}

	var sumCPU float64
	var sumMem uint64
	for _, a := range apps {
		for _, u := range a.Units {
			a.CPU += u.CPU
			a.Mem += u.Mem
			a.NetRx += u.NetRx
			a.NetTx += u.NetTx
			a.IORead += u.IORead
			a.IOWrite += u.IOWrite
			a.LogErrors += u.LogErrors
			if u.Container != nil {
				a.Total++
				switch u.Container.State {
				case "running":
					a.Running++
				case "paused":
					a.Paused++
				}
				if u.Container.Health == "unhealthy" {
					a.Unhealthy++
				}
			}
		}
		sumCPU += a.CPU
		sumMem += a.Mem
		a.Today = m.st.Traffic.Day("app:"+a.Key, day)
		a.Month = m.st.Traffic.Month("app:"+a.Key, month)
		a.Status = "ok"
		switch {
		case a.Kind == "system":
		case a.Paused > 0 && a.Unhealthy == 0:
			a.Status = "paused"
		case a.Running == 0:
			a.Status = "stopped"
		case a.Unhealthy > 0:
			a.Status = "crit"
		case a.Running+a.Paused < a.Total:
			a.Status = "warn"
		}
		sort.Slice(a.Units, func(i, j int) bool {
			if a.Units[i].CPU != a.Units[j].CPU {
				return a.Units[i].CPU > a.Units[j].CPU
			}
			return a.Units[i].Mem > a.Units[j].Mem
		})
	}
	// o que o host gasta e nenhuma unidade explica: kernel, buffers, processos soltos
	k := get(AppKernel, "kernel")
	k.CPU = math.Max(0, m.hostNow.CPU-sumCPU)
	k.Mem = sub(m.hostNow.MemUsed, sumMem)

	m.apps = m.apps[:0]
	for _, a := range apps {
		m.series("app:"+a.Key).Add(ts, []float64{a.CPU, float64(a.Mem), a.NetRx, a.NetTx, a.IORead, a.IOWrite})
		m.apps = append(m.apps, *a)
	}
	sort.Slice(m.apps, func(i, j int) bool { return appLess(m.apps[i], m.apps[j]) })
}

func appLess(a, b AppView) bool {
	if oa, ob := appOrder(a), appOrder(b); oa != ob {
		return oa < ob
	}
	return a.Name < b.Name
}

// appOrder: apps de verdade primeiro (por nome), depois o monitor, sistema e kernel.
func appOrder(a AppView) int {
	switch {
	case a.Key == AppKernel:
		return 5
	case a.Key == AppSystem:
		return 4
	case a.Key == AppTemp:
		return 3
	case a.Self:
		return 2
	}
	return 1
}

// --- tarefas periódicas (Docker) -------------------------------------------------------------

func (m *Monitor) refreshContainers(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cs, err := m.dc.Containers(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if m.dockerOK || m.dockerErr == "" {
			slog.Warn("sem acesso ao Docker", "err", err)
		}
		m.dockerOK, m.dockerErr = false, err.Error()
		return
	}
	m.dockerOK, m.dockerErr = true, ""
	m.containers = map[string]docker.Container{}
	resumed := m.recentlyResumed(time.Now())
	for _, c := range cs {
		m.containers[c.ID] = calmHealth(c, resumed[c.Name])
	}
}

// pauseGrace: depois de retomar, o healthcheck ainda diz "unhealthy" até a
// próxima checagem passar (até o intervalo dele, ex.: 30 s).
const pauseGrace = 3 * time.Minute

// recentlyResumed devolve os contêineres retomados (botão ou docker unpause)
// há menos de pauseGrace. Com m.mu travado.
func (m *Monitor) recentlyResumed(now time.Time) map[string]bool {
	out := map[string]bool{}
	from := now.Add(-pauseGrace).Unix()
	for name, t := range m.resumed {
		if t >= from {
			out[name] = true
		} else {
			delete(m.resumed, name)
		}
	}
	if m.st == nil {
		return out
	}
	for i := len(m.st.Events) - 1; i >= 0 && m.st.Events[i].T >= from; i-- {
		if e := m.st.Events[i]; e.Action == "unpause" {
			out[e.Container] = true
		}
	}
	return out
}

// calmHealth tira o julgamento de saúde de quem foi pausado de propósito: o
// Docker marca o contêiner pausado como "unhealthy" (o healthcheck não roda) e
// ele segue assim logo depois de retomar, até a próxima checagem passar. Pausa
// é escolha, não problema: pausado fica sem saúde, recém-retomado fica "starting".
func calmHealth(c docker.Container, resumed bool) docker.Container {
	switch {
	case c.State == "paused":
		c.Health = ""
	case resumed && c.Health == "unhealthy":
		c.Health = "starting"
	}
	return c
}

func (m *Monitor) detectCloud(ctx context.Context) {
	c, ok := detectOracle(ctx)
	if !ok {
		return
	}
	m.mu.Lock()
	if m.cloud.Shape != c.Shape || m.cloud.OCPUs != c.OCPUs {
		slog.Info("VM da Oracle", "regiao", c.Region, "shape", c.Shape, "ocpus", c.OCPUs, "memoria_gb", c.MemGB)
	}
	m.cloud = c
	m.mu.Unlock()
}

func (m *Monitor) refreshInfo(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := m.dc.Info(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
}

func (m *Monitor) refreshDiskUsage(ctx context.Context) {
	du, err := m.dc.DiskUsage(ctx)
	if err != nil {
		slog.Warn("docker system df falhou", "err", err)
		return
	}
	m.mu.Lock()
	m.df = du
	m.mu.Unlock()
	m.refreshStorage()
}

func (m *Monitor) refreshEvents(ctx context.Context) {
	// continua de onde a consulta anterior parou (na primeira, as últimas 24 h)
	m.mu.RLock()
	since := max(m.eventsTo, time.Now().Add(-24*time.Hour).Unix())
	if m.eventsTo == 0 {
		for _, e := range m.st.Events {
			if e.Action != "oom_kill_host" && e.T >= since {
				since = e.T + 1
			}
		}
	}
	m.mu.RUnlock()
	until := time.Now().Unix()
	if since > until {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	evs, err := m.dc.Events(ctx, since, until)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.eventsTo = until + 1
	if len(evs) == 0 {
		m.mu.Unlock()
		return
	}
	// algo mudou (subiu, caiu, saúde): atualiza a lista de contêineres já
	defer m.refreshContainers(ctx)
	m.st.Events = append(m.st.Events, evs...)
	sort.SliceStable(m.st.Events, func(i, j int) bool { return m.st.Events[i].T < m.st.Events[j].T })
	if n := len(m.st.Events); n > 1000 {
		m.st.Events = append([]docker.Event(nil), m.st.Events[n-1000:]...)
	}
	m.mu.Unlock()
}

// refreshLogStats lê só as linhas novas de cada contêiner (até 2000 por
// passada) e conta quantas parecem erro.
func (m *Monitor) refreshLogStats(ctx context.Context) {
	m.mu.RLock()
	var running []docker.Container
	for _, c := range m.containers {
		if c.State == "running" {
			running = append(running, c)
		}
	}
	m.mu.RUnlock()
	now := time.Now().Unix()
	for _, c := range running {
		m.mu.RLock()
		ls := m.st.LogStats[c.Name]
		since := now - 300
		if ls != nil && ls.Since > now-24*3600 {
			since = ls.Since
		}
		m.mu.RUnlock()
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		lines, err := m.dc.Logs(cctx, c.ID, 2000, since)
		cancel()
		if err != nil {
			continue
		}
		check := store.LogCheck{T: now, Lines: len(lines)}
		var errs []docker.LogLine
		for _, l := range lines {
			if errorLine.MatchString(l.Msg) {
				check.Errors++
				errs = append(errs, l)
			}
		}
		m.mu.Lock()
		ls = m.st.LogStats[c.Name]
		if ls == nil {
			ls = &store.LogStat{}
			m.st.LogStats[c.Name] = ls
		}
		ls.Since = now
		ls.Checks = append(ls.Checks, check)
		for len(ls.Checks) > 0 && ls.Checks[0].T < now-24*3600 {
			ls.Checks = ls.Checks[1:]
		}
		for _, e := range errs {
			if len(e.Msg) > 500 {
				e.Msg = e.Msg[:500] + "…"
			}
			ls.LastErrors = append(ls.LastErrors, e)
		}
		if n := len(ls.LastErrors); n > 8 {
			ls.LastErrors = append([]docker.LogLine(nil), ls.LastErrors[n-8:]...)
		}
		m.mu.Unlock()
	}
}

// prune apaga o que ficou velho: séries de unidades sumidas há 30 dias,
// banda diária antiga, estatística de log de contêineres que não existem mais.
func (m *Monitor) prune(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := now.Add(-30 * 24 * time.Hour).Unix()
	for k, s := range m.st.Units {
		if s.Last < cut {
			delete(m.st.Units, k)
		}
	}
	m.st.Traffic.Prune(m.day(now.AddDate(0, -13, 0)), m.day(now.AddDate(-3, 0, 0)))
	for name, ls := range m.st.LogStats {
		if ls.Since < now.Add(-48*time.Hour).Unix() {
			delete(m.st.LogStats, name)
		}
	}
	// cor de app que sumiu há uma semana volta para a paleta
	for k := range m.st.Colors {
		if s := m.st.Units["app:"+k]; s == nil || s.Last < now.Add(-7*24*time.Hour).Unix() {
			delete(m.st.Colors, k)
		}
	}
	active := map[string]bool{}
	for _, c := range m.containers {
		active["c:"+c.Name] = true
	}
	for k := range m.st.Traffic.Last {
		if strings.HasPrefix(k, "c:") && !active[k] {
			delete(m.st.Traffic.Last, k)
		}
	}
}

// --- pausar e retomar ----------------------------------------------------------------------

var (
	ErrSelfPause = errors.New("o próprio monitor não pode ser pausado")
	ErrNoApp     = errors.New("aplicação não encontrada")
)

// SetPaused congela (docker pause) ou descongela todos os contêineres de uma
// app. O próprio painel fica de fora: pausado, não haveria botão para retomar.
func (m *Monitor) SetPaused(ctx context.Context, appKey string, pause bool) ([]string, error) {
	m.mu.RLock()
	var targets []docker.Container
	found, self := false, false
	for _, a := range m.apps {
		if a.Key != appKey || (a.Kind != "compose" && a.Kind != "standalone") {
			continue
		}
		found = true
		if a.Self || a.Key == m.cfg.SelfProject {
			self = true
			break
		}
		for _, u := range a.Units {
			if c := u.Container; c != nil && ((pause && c.State == "running") || (!pause && c.State == "paused")) {
				targets = append(targets, *c)
			}
		}
	}
	m.mu.RUnlock()
	switch {
	case self:
		return nil, ErrSelfPause
	case !found:
		return nil, ErrNoApp
	}
	var done, errs []string
	for _, c := range targets {
		if c.Project != "" && c.Project == m.cfg.SelfProject {
			continue // nunca um contêiner do próprio painel
		}
		if err := m.dc.Pause(ctx, c.ID, pause); err != nil {
			errs = append(errs, c.Name+": "+err.Error())
			continue
		}
		done = append(done, c.Name)
		if !pause {
			m.mu.Lock()
			if m.resumed == nil {
				m.resumed = map[string]int64{}
			}
			m.resumed[c.Name] = time.Now().Unix()
			m.mu.Unlock()
		}
	}
	m.refreshContainers(ctx)
	if len(errs) > 0 {
		return done, errors.New(strings.Join(errs, "; "))
	}
	return done, nil
}
