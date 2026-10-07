// Package docker fala com a API do Docker — sempre pelo proxy
// (vpserver-dockerproxy), que só deixa passar GET em poucos caminhos e, de
// escrita, pausar/retomar contêiner e as duas limpezas seguras (cache de build e
// imagens sem nome). O painel nunca recebe o socket do Docker.
package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base string
	hc   *http.Client
	slow *http.Client // limpezas: podem levar minutos (o prazo vem do contexto)
}

// New aceita "http://host:2375" ou "unix:///var/run/docker.sock" (para desenvolvimento).
func New(addr string) *Client {
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 60 * time.Second}
	base := strings.TrimRight(addr, "/")
	if strings.HasPrefix(addr, "unix://") {
		sock := strings.TrimPrefix(addr, "unix://")
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}
		base = "http://docker"
	}
	return &Client{base: base, hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}, slow: &http.Client{Transport: tr}}
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("docker %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	resp, err := c.get(ctx, path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// --- contêineres ----------------------------------------------------------------

type Container struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	State   string            `json:"state"`  // running, exited, restarting, paused...
	Status  string            `json:"status"` // "Up 2 days (healthy)"
	Health  string            `json:"health"` // healthy, unhealthy, starting ou ""
	Created int64             `json:"created"`
	Project string            `json:"project"`
	Service string            `json:"service"`
	Ports   []string          `json:"ports"`
	Labels  map[string]string `json:"-"`
	ImageID string            `json:"-"`
	Command string            `json:"-"` // só para agrupar instâncias; não vai para a tela
}

type apiContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Command string            `json:"Command"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	Health *struct {
		Status string `json:"Status"`
	} `json:"Health"`
}

func healthFromStatus(s string) string {
	switch {
	case strings.Contains(s, "(healthy)"):
		return "healthy"
	case strings.Contains(s, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(s, "(health: starting)"):
		return "starting"
	}
	return ""
}

func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var raw []apiContainer
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, a := range raw {
		ct := Container{
			ID: a.ID, Image: a.Image, ImageID: a.ImageID, Command: a.Command, State: a.State, Status: a.Status, Created: a.Created,
			Labels: a.Labels, Project: a.Labels["com.docker.compose.project"],
			Service: a.Labels["com.docker.compose.service"], Health: healthFromStatus(a.Status),
		}
		if a.Health != nil && a.Health.Status != "" && a.Health.Status != "none" {
			ct.Health = a.Health.Status
		}
		if len(a.Names) > 0 {
			ct.Name = strings.TrimPrefix(a.Names[0], "/")
		}
		seen := map[string]bool{}
		for _, p := range a.Ports {
			s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			if p.PublicPort != 0 {
				s = fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			}
			if !seen[s] {
				seen[s] = true
				ct.Ports = append(ct.Ports, s)
			}
		}
		out = append(out, ct)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// --- logs -------------------------------------------------------------------------

type LogLine struct {
	T      int64  `json:"t"` // unix em milissegundos
	Stream string `json:"s"` // out | err
	Msg    string `json:"m"`
}

// Logs pega as últimas `tail` linhas (opcionalmente só depois de `since`, em unix).
func (c *Client) Logs(ctx context.Context, id string, tail int, since int64) ([]LogLine, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(tail)}}
	if since > 0 {
		q.Set("since", strconv.FormatInt(since, 10))
	}
	resp, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/logs", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return ParseLogs(b), nil
}

// ParseLogs entende os dois formatos: multiplexado (cabeçalho de 8 bytes por
// quadro, contêiner sem TTY) e cru (contêiner com TTY).
func ParseLogs(b []byte) []LogLine {
	var out []LogLine
	multiplexed := len(b) >= 8 && b[0] <= 2 && b[1] == 0 && b[2] == 0 && b[3] == 0
	if !multiplexed {
		for _, ln := range strings.Split(string(b), "\n") {
			if ln != "" {
				out = append(out, parseLine(ln, "out"))
			}
		}
		return out
	}
	var partial [3]strings.Builder
	for len(b) >= 8 {
		stream := b[0]
		size := int(binary.BigEndian.Uint32(b[4:8]))
		b = b[8:]
		if size > len(b) {
			size = len(b)
		}
		chunk := string(b[:size])
		b = b[size:]
		if stream > 2 {
			stream = 1
		}
		partial[stream].WriteString(chunk)
		s := partial[stream].String()
		if !strings.HasSuffix(s, "\n") {
			continue
		}
		partial[stream].Reset()
		name := "out"
		if stream == 2 {
			name = "err"
		}
		for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
			out = append(out, parseLine(ln, name))
		}
	}
	return out
}

func parseLine(ln, stream string) LogLine {
	l := LogLine{Stream: stream, Msg: ln}
	if ts, rest, ok := strings.Cut(ln, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			l.T, l.Msg = t.UnixMilli(), rest
		}
	}
	l.Msg = StripANSI(strings.TrimRight(l.Msg, "\r"))
	return l
}

// StripANSI tira as sequências de cor (ESC[...m) que muitos apps põem no log.
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// --- eventos ----------------------------------------------------------------------

type Event struct {
	T         int64  `json:"t"` // unix
	Action    string `json:"action"`
	Container string `json:"container"`
	Project   string `json:"project"`
	Image     string `json:"image"`
	ExitCode  string `json:"exitCode,omitempty"`
	Requested bool   `json:"requested,omitempty"` // "die" de uma parada pedida (deploy/docker stop), não queda
}

// MarkRequested marca os "die" que vieram de parada pedida: houve "kill" ou
// "stop" do mesmo contêiner até 60 s antes ou depois. Os "kill"/"stop" em si
// saem da lista (são só o pedido). A entrada deve estar em ordem de tempo.
func MarkRequested(evs []Event) []Event {
	out := make([]Event, 0, len(evs))
	for i, e := range evs {
		if e.Action == "kill" || e.Action == "stop" {
			continue
		}
		if e.Action == "die" {
			for j := i - 1; j >= 0 && e.T-evs[j].T <= 60; j-- {
				if evs[j].Container == e.Container && evs[j].Action == "kill" {
					e.Requested = true
				}
			}
			for j := i + 1; j < len(evs) && evs[j].T-e.T <= 60; j++ {
				if evs[j].Container == e.Container && (evs[j].Action == "stop" || evs[j].Action == "kill") {
					e.Requested = true
				}
			}
		}
		out = append(out, e)
	}
	return out
}

// Só o que conta a história de um contêiner; create/rename/destroy de cada
// deploy e os exec_* dos healthchecks seriam ruído. "kill" e "stop" ficam
// porque separam parada pedida (deploy, docker stop) de queda de verdade.
var keepActions = map[string]bool{
	"start": true, "restart": true, "die": true, "oom": true, "pause": true, "unpause": true,
	"kill": true, "stop": true,
}

// Events lê os eventos de contêiner entre since e until (sem ficar escutando).
func (c *Client) Events(ctx context.Context, since, until int64) ([]Event, error) {
	// o filtro vai para o Docker: sem ele, cada consulta traria de volta os ~250
	// eventos guardados (quase todos exec_* dos healthchecks), ~300 KB à toa
	events := []string{"health_status"}
	for a := range keepActions {
		events = append(events, a)
	}
	sort.Strings(events)
	f, _ := json.Marshal(map[string][]string{"type": {"container"}, "event": events})
	q := url.Values{
		"since":   {strconv.FormatInt(since, 10)},
		"until":   {strconv.FormatInt(until, 10)},
		"filters": {string(f)},
	}
	resp, err := c.get(ctx, "/events", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e struct {
			Action string `json:"Action"`
			Time   int64  `json:"time"`
			Actor  struct {
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		action := e.Action
		if strings.HasPrefix(action, "health_status") {
			action = strings.TrimSpace(strings.ReplaceAll(action, "health_status:", "health:"))
		} else if !keepActions[action] {
			continue
		}
		a := e.Actor.Attributes
		out = append(out, Event{
			T: e.Time, Action: action, Container: a["name"], Image: a["image"],
			Project: a["com.docker.compose.project"], ExitCode: a["exitCode"],
		})
	}
	return out, sc.Err()
}

// --- info e uso de disco ------------------------------------------------------------

type Info struct {
	Name              string `json:"name"`
	OperatingSystem   string `json:"os"`
	KernelVersion     string `json:"kernel"`
	Architecture      string `json:"arch"`
	NCPU              int    `json:"cpus"`
	MemTotal          int64  `json:"memTotal"`
	ServerVersion     string `json:"docker"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"running"`
	ContainersStopped int    `json:"stopped"`
	Images            int    `json:"images"`
	Driver            string `json:"storageDriver"`
	LoggingDriver     string `json:"loggingDriver"`
	CgroupVersion     string `json:"cgroupVersion"`
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var raw struct {
		Name, OperatingSystem, KernelVersion, Architecture, ServerVersion string
		Driver, LoggingDriver, CgroupVersion                              string
		NCPU                                                              int
		MemTotal                                                          int64
		Containers, ContainersRunning, ContainersStopped, Images          int
	}
	err := c.getJSON(ctx, "/info", nil, &raw)
	return Info{
		Name: raw.Name, OperatingSystem: raw.OperatingSystem, KernelVersion: raw.KernelVersion,
		Architecture: raw.Architecture, NCPU: raw.NCPU, MemTotal: raw.MemTotal,
		ServerVersion: raw.ServerVersion, Containers: raw.Containers,
		ContainersRunning: raw.ContainersRunning, ContainersStopped: raw.ContainersStopped,
		Images: raw.Images, Driver: raw.Driver, LoggingDriver: raw.LoggingDriver,
		CgroupVersion: raw.CgroupVersion,
	}, err
}

type Volume struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	Size    int64  `json:"size"`
	Refs    int64  `json:"refs"`
}

type ImageUse struct {
	ID         string   `json:"-"`
	Tags       []string `json:"tags"`
	Dangling   bool     `json:"dangling,omitempty"` // sem nome (<none>): sobra de build/atualização
	Size       int64    `json:"size"`
	Containers int64    `json:"containers"`
}

// ContainerDisk é o que cada contêiner ocupa (para dividir o disco por app).
type ContainerDisk struct {
	ID      string
	Name    string
	ImageID string
	SizeRw  int64    // camada gravável
	Volumes []string // volumes nomeados montados
}

type DiskUsage struct {
	T                int64           `json:"t"`
	ImagesSize       int64           `json:"imagesSize"` // camadas de todas as imagens, sem repetir
	ImagesUnused     int64           `json:"imagesUnused"`
	ImagesCount      int             `json:"imagesCount"`
	ContainersSize   int64           `json:"containersSize"` // camada gravável dos contêineres
	VolumesSize      int64           `json:"volumesSize"`
	BuildCacheSize   int64           `json:"buildCacheSize"`
	BuildCacheUnused int64           `json:"buildCacheUnused"`
	DanglingSize     int64           `json:"danglingSize"` // imagens sem nome que nenhum contêiner usa
	DanglingCount    int             `json:"danglingCount"`
	Volumes          []Volume        `json:"volumes"`
	Images           []ImageUse      `json:"images"`
	Containers       []ContainerDisk `json:"-"`
}

// DiskUsage chama /system/df — custa ~1 s (o Docker mede os volumes), então
// o monitor só chama de tempos em tempos.
func (c *Client) DiskUsage(ctx context.Context) (DiskUsage, error) {
	var raw struct {
		LayersSize int64 `json:"LayersSize"`
		Images     []struct {
			ID         string   `json:"Id"`
			RepoTags   []string `json:"RepoTags"`
			Size       int64    `json:"Size"`
			SharedSize int64    `json:"SharedSize"`
			Containers int64    `json:"Containers"`
		} `json:"Images"`
		Containers []struct {
			ID      string   `json:"Id"`
			Names   []string `json:"Names"`
			ImageID string   `json:"ImageID"`
			SizeRw  int64    `json:"SizeRw"`
			Mounts  []struct {
				Type string `json:"Type"`
				Name string `json:"Name"`
			} `json:"Mounts"`
		} `json:"Containers"`
		Volumes []struct {
			Name      string            `json:"Name"`
			Labels    map[string]string `json:"Labels"`
			UsageData *struct {
				Size     int64 `json:"Size"`
				RefCount int64 `json:"RefCount"`
			} `json:"UsageData"`
		} `json:"Volumes"`
		BuildCache []struct {
			Size  int64 `json:"Size"`
			InUse bool  `json:"InUse"`
		} `json:"BuildCache"`
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := c.getJSON(ctx, "/system/df", nil, &raw); err != nil {
		return DiskUsage{}, err
	}
	du := DiskUsage{T: time.Now().Unix(), ImagesSize: raw.LayersSize, ImagesCount: len(raw.Images),
		Volumes: []Volume{}, Images: []ImageUse{}}
	for _, im := range raw.Images {
		if im.Containers == 0 {
			du.ImagesUnused += im.Size - max(0, im.SharedSize)
		}
		tags := im.RepoTags
		dangling := isDangling(tags)
		if dangling {
			tags = []string{"<sem nome>"}
			if im.Containers == 0 {
				du.DanglingSize += im.Size - max(0, im.SharedSize)
				du.DanglingCount++
			}
		}
		du.Images = append(du.Images, ImageUse{ID: im.ID, Tags: tags, Dangling: dangling, Size: im.Size, Containers: im.Containers})
	}
	sort.Slice(du.Images, func(i, j int) bool { return du.Images[i].Size > du.Images[j].Size })
	for _, ct := range raw.Containers {
		du.ContainersSize += max(0, ct.SizeRw)
		cd := ContainerDisk{ID: ct.ID, ImageID: ct.ImageID, SizeRw: max(0, ct.SizeRw)}
		if len(ct.Names) > 0 {
			cd.Name = strings.TrimPrefix(ct.Names[0], "/")
		}
		for _, m := range ct.Mounts {
			if m.Type == "volume" && m.Name != "" {
				cd.Volumes = append(cd.Volumes, m.Name)
			}
		}
		du.Containers = append(du.Containers, cd)
	}
	for _, v := range raw.Volumes {
		vol := Volume{Name: v.Name, Project: v.Labels["com.docker.compose.project"]}
		if v.UsageData != nil {
			vol.Size, vol.Refs = max(0, v.UsageData.Size), v.UsageData.RefCount
		}
		du.VolumesSize += vol.Size
		du.Volumes = append(du.Volumes, vol)
	}
	sort.Slice(du.Volumes, func(i, j int) bool { return du.Volumes[i].Size > du.Volumes[j].Size })
	for _, b := range raw.BuildCache {
		du.BuildCacheSize += b.Size
		if !b.InUse {
			du.BuildCacheUnused += b.Size
		}
	}
	return du, nil
}

// Ping confere se o proxy e o Docker respondem.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.get(ctx, "/_ping", nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// Pause congela (pause) ou descongela (unpause) um contêiner. É a única
// escrita que o painel faz no Docker; o proxy só deixa passar estes dois POST.
func (c *Client) Pause(ctx context.Context, id string, pause bool) error {
	action := "pause"
	if !pause {
		action = "unpause"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/containers/"+url.PathEscape(id)+"/"+action, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker %s: %d %s", action, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// isDangling: imagem sem nome (o Docker devolve sem tags ou com "<none>:<none>").
func isDangling(tags []string) bool {
	for _, t := range tags {
		if t != "<none>:<none>" && t != "" {
			return false
		}
	}
	return true
}

// Pruned é o resultado de uma limpeza.
type Pruned struct {
	Freed   int64 // bytes liberados (o que o Docker diz)
	Removed int   // itens apagados (caches ou imagens)
}

func (c *Client) post(ctx context.Context, path string, q url.Values, v any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.slow.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// PruneBuildCache apaga o cache de build que não está em uso (o mesmo que
// "docker builder prune -a"). Não mexe em imagem, contêiner nem volume: o
// próximo build só demora mais.
func (c *Client) PruneBuildCache(ctx context.Context) (Pruned, error) {
	var r struct {
		CachesDeleted  []string `json:"CachesDeleted"`
		SpaceReclaimed int64    `json:"SpaceReclaimed"`
	}
	err := c.post(ctx, "/build/prune", url.Values{"all": {"1"}}, &r)
	return Pruned{Freed: r.SpaceReclaimed, Removed: len(r.CachesDeleted)}, err
}

// PruneDanglingImages apaga só as imagens sem nome ("docker image prune",
// sem -a). O Docker nunca apaga imagem que algum contêiner usa, nem parado.
func (c *Client) PruneDanglingImages(ctx context.Context) (Pruned, error) {
	var r struct {
		ImagesDeleted []struct {
			Deleted string `json:"Deleted"`
		} `json:"ImagesDeleted"`
		SpaceReclaimed int64 `json:"SpaceReclaimed"`
	}
	err := c.post(ctx, "/images/prune", url.Values{"filters": {`{"dangling":["true"]}`}}, &r)
	n := 0
	for _, d := range r.ImagesDeleted {
		if d.Deleted != "" {
			n++
		}
	}
	return Pruned{Freed: r.SpaceReclaimed, Removed: n}, err
}
