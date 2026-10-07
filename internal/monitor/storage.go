package monitor

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
)

// Disk é quanto uma app (ou um contêiner) ocupa no disco.
type Disk struct {
	Images  uint64 `json:"images"`  // imagens que ela usa (compartilhada = dividida entre as apps)
	Layer   uint64 `json:"layer"`   // camada gravável dos contêineres
	Volumes uint64 `json:"volumes"` // volumes nomeados
	Logs    uint64 `json:"logs"`    // logs do Docker (*-json.log)
	Total   uint64 `json:"total"`
}

func (d *Disk) sum() { d.Total = d.Images + d.Layer + d.Volumes + d.Logs }

// StorageView é o disco do servidor dividido: apps, cache e o resto.
type StorageView struct {
	Measured     int64  `json:"measured"`     // quando o Docker mediu (unix); 0 = ainda não
	Apps         uint64 `json:"apps"`         // soma do que as apps ocupam
	BuildCache   uint64 `json:"buildCache"`   // cache de build do Docker (liberável)
	UnusedImages uint64 `json:"unusedImages"` // imagens que nenhum contêiner usa (liberável)
	Logs         uint64 `json:"logs"`         // logs de todos os contêineres (já dentro de Apps)
	Other        uint64 `json:"other"`        // sistema, runners, /var/log, pastas em /opt...
	LogsKnown    bool   `json:"logsKnown"`    // o leitor de tamanho de logs está funcionando
}

type storage struct {
	view    StorageView
	apps    map[string]Disk // por app
	units   map[string]Disk // por nome de contêiner (sem a imagem)
	imgSize map[string]uint64
	imgUses map[string]int // por nome de contêiner: quantos contêineres usam a mesma imagem
}

var logPath = regexp.MustCompile(`/([0-9a-f]{64})/[0-9a-f]{64}-json\.log`)

// readLogSizes lê o arquivo do vpserver-sizer: "tamanho caminho" por linha.
func readLogSizes(path string) (map[string]uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	out := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		size, p, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		m := logPath.FindStringSubmatch(p)
		n, err := strconv.ParseUint(size, 10, 64)
		if m == nil || err != nil {
			continue
		}
		out[m[1]] += n
	}
	return out, true
}

// computeStorage divide o disco entre as apps. Imagens usadas por mais de uma
// app são divididas entre elas; os tamanhos das imagens são escalados para
// bater com o total de camadas sem repetição (LayersSize), já que imagens
// diferentes compartilham camadas.
func (m *Monitor) computeStorage(du docker.DiskUsage, logs map[string]uint64, logsOK bool, fsUsed uint64) storage {
	st := storage{apps: map[string]Disk{}, units: map[string]Disk{}, imgSize: map[string]uint64{}, imgUses: map[string]int{}}
	if du.T == 0 {
		return st
	}
	var sumImg int64
	for _, im := range du.Images {
		sumImg += im.Size
	}
	factor := 1.0
	if sumImg > 0 && du.ImagesSize > 0 && du.ImagesSize < sumImg {
		factor = float64(du.ImagesSize) / float64(sumImg)
	}
	imgSize := map[string]uint64{}
	for _, im := range du.Images {
		imgSize[im.ID] = uint64(float64(im.Size) * factor)
	}

	appOf := func(cd docker.ContainerDisk) string {
		if c, ok := m.containers[cd.ID]; ok {
			return m.appKey(c)
		}
		return m.appKey(docker.Container{Name: cd.Name})
	}
	vols := map[string]docker.Volume{}
	for _, v := range du.Volumes {
		vols[v.Name] = v
	}

	containers := append([]docker.ContainerDisk(nil), du.Containers...)
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	imgApps := map[string]map[string]bool{}
	imgCount := map[string]int{}
	volOwner := map[string]string{}
	for _, cd := range containers {
		app := appOf(cd)
		d := Disk{Layer: uint64(cd.SizeRw), Logs: logs[cd.ID]}
		for _, v := range cd.Volumes {
			d.Volumes += uint64(vols[v].Size)
			if _, ok := volOwner[v]; !ok {
				volOwner[v] = app
			}
		}
		d.sum()
		st.units[cd.Name] = d
		if imgApps[cd.ImageID] == nil {
			imgApps[cd.ImageID] = map[string]bool{}
		}
		imgApps[cd.ImageID][app] = true
		imgCount[cd.ImageID]++

		a := st.apps[app]
		a.Layer += d.Layer
		a.Logs += d.Logs
		st.apps[app] = a
		st.view.Logs += d.Logs
	}
	for _, cd := range containers {
		st.imgSize[cd.Name] = imgSize[cd.ImageID]
		st.imgUses[cd.Name] = imgCount[cd.ImageID]
	}
	for id, apps := range imgApps {
		share := imgSize[id] / uint64(len(apps))
		for app := range apps {
			a := st.apps[app]
			a.Images += share
			st.apps[app] = a
		}
	}
	// volume sem contêiner montado, mas com rótulo do Compose: fica com a app dele
	for _, v := range du.Volumes {
		if _, ok := volOwner[v.Name]; !ok && v.Project != "" {
			volOwner[v.Name] = v.Project
		}
	}
	for name, app := range volOwner {
		a := st.apps[app]
		a.Volumes += uint64(vols[name].Size)
		st.apps[app] = a
	}
	for k, a := range st.apps {
		a.sum()
		st.apps[k] = a
		st.view.Apps += a.Total
	}

	st.view.Measured = du.T
	st.view.BuildCache = uint64(max(0, du.BuildCacheSize))
	st.view.UnusedImages = uint64(max(0, du.ImagesUnused))
	st.view.LogsKnown = logsOK
	known := st.view.Apps + st.view.BuildCache + st.view.UnusedImages
	if fsUsed > known {
		st.view.Other = fsUsed - known
	}
	return st
}

// RefreshStorage relê os tamanhos de log (depois de zerar logs).
func (m *Monitor) RefreshStorage() { m.refreshStorage() }

// refreshStorage relê os tamanhos de log e refaz a divisão com a última medição do Docker.
func (m *Monitor) refreshStorage() {
	logs, ok := readLogSizes(m.cfg.LogSizes)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logSizes, m.logsOK = logs, ok
	m.stor = m.computeStorage(m.df, logs, ok, m.hostNow.FSUsed)
}

// --- instâncias -------------------------------------------------------------------

// Component é uma peça de uma app (api, worker, banco...) e quantas
// instâncias dela existem: contêineres da mesma app com a mesma imagem e o
// mesmo comando (ex.: api1 e api2 da mesma app; réplicas do Compose).
type Component struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Running int    `json:"running"`
	API     bool   `json:"api"` // é a peça que atende (API/app/web), não banco, fila, túnel...
}

var (
	trailingNum = regexp.MustCompile(`[-_]?\d+$`)
	// nome de serviço que costuma ser a API/app que atende requisições
	apiLike = regexp.MustCompile(`(?i)(^|[-_])(api|app|web|www|server|backend|frontend|site|http|front|monitor)\d*($|[-_])`)
	// peças de apoio: banco, cache, fila, agendador, túnel, proxy...
	infraLike = regexp.MustCompile(`(?i)(postgres|mysql|maria|mongo|redis|valkey|memcache|rabbit|kafka|queue|worker|beat|schedul|cron|celery|tunnel|cloudflared|proxy|caddy|nginx|traefik|haproxy|warp|sizer|backup|(^|[-_])(db|mq|cache)\d*($|[-_]))`)
)

// markAPI marca quais componentes são a API. Se nenhum tiver nome de API, a
// peça principal é o que não for apoio (ex.: uma app de contêiner único).
func markAPI(cs []Component) {
	found := false
	for i := range cs {
		if apiLike.MatchString(cs[i].Name) && !infraLike.MatchString(cs[i].Name) {
			cs[i].API, found = true, true
		}
	}
	if found {
		return
	}
	for i := range cs {
		if !infraLike.MatchString(cs[i].Name) {
			cs[i].API = true
		}
	}
}

func components(cs []docker.Container) []Component {
	type group struct {
		names   []string
		running int
	}
	groups := map[string]*group{}
	var order []string
	for _, c := range cs {
		img := c.ImageID
		if img == "" {
			img = c.Image
		}
		k := img + "\x00" + c.Command
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
			order = append(order, k)
		}
		n := c.Service
		if n == "" {
			n = c.Name
		}
		g.names = append(g.names, n)
		if c.State == "running" {
			g.running++
		}
	}
	out := make([]Component, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Strings(g.names)
		name := g.names[0]
		if len(g.names) > 1 {
			if base := trailingNum.ReplaceAllString(name, ""); base != "" {
				name = base
			}
		}
		out = append(out, Component{Name: name, Count: len(g.names), Running: g.running})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	markAPI(out)
	return out
}
