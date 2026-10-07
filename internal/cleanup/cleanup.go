// Package cleanup libera espaço em disco só com o que não afeta as
// aplicações: cache de build do Docker fora de uso, imagens sem nome que
// nenhum contêiner usa e logs do Docker (escolhidos por contêiner). Nunca
// volumes, contêineres, redes nem imagens com nome ou em uso.
//
// O cache e as imagens vão pelo proxy do Docker (que só deixa passar essas
// duas limpezas). Os logs, o painel não alcança: ele pede ao vpserver-cleaner
// (busybox sem rede) por um arquivo numa pasta compartilhada, com os IDs dos
// contêineres; o ajudante zera o log atual e apaga os rotacionados.
package cleanup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

// Docker são as duas limpezas que o proxy deixa passar.
type Docker interface {
	PruneBuildCache(ctx context.Context) (docker.Pruned, error)
	PruneDanglingImages(ctx context.Context) (docker.Pruned, error)
}

// Disk é o monitor: o que dá para limpar e medir de novo depois.
type Disk interface {
	DiskSnapshot() monitor.DiskSnapshot
	RefreshDisk(ctx context.Context)
	RefreshStorage()
}

const (
	maxRuns     = 30
	autoGap     = 6 * time.Hour // limpeza automática: no máximo uma a cada 6 h
	aliveWithin = time.Minute   // o ajudante marca .alive a cada ~20 s
	helperWait  = 90 * time.Second
	pruneWait   = 10 * time.Minute
)

var (
	ErrBusy      = errors.New("já tem uma limpeza em andamento: espere terminar")
	ErrNothing   = errors.New("escolha pelo menos uma coisa para limpar")
	ErrNoHelper  = errors.New("o ajudante que limpa os logs (vpserver-cleaner) não está rodando: atualize o compose do painel")
	ErrUnknownCt = errors.New("contêiner desconhecido (a lista mudou): atualize a tela e escolha de novo")
)

// Request é o que limpar agora.
type Request struct {
	BuildCache bool     `json:"buildCache"`
	Dangling   bool     `json:"dangling"`
	Logs       []string `json:"logs"` // IDs de contêiner
}

func (r Request) empty() bool { return !r.BuildCache && !r.Dangling && len(r.Logs) == 0 }

// Auto é a limpeza automática: quando o disco passa de Threshold%.
type Auto struct {
	Enabled    bool `json:"enabled"`
	Threshold  int  `json:"threshold"` // % do disco
	BuildCache bool `json:"buildCache"`
	Dangling   bool `json:"dangling"`
	Logs       bool `json:"logs"`
	LogsOverMB int  `json:"logsOverMb"` // só logs maiores que isso, por contêiner
}

func defaultAuto() Auto {
	return Auto{Threshold: 85, BuildCache: true, Dangling: true, LogsOverMB: 100}
}

// Step é uma parte de uma limpeza.
type Step struct {
	Item    string   `json:"item"` // build_cache | dangling | logs
	Freed   uint64   `json:"freed"`
	Removed int      `json:"removed"`
	Names   []string `json:"names,omitempty"` // logs: contêineres
	Error   string   `json:"error,omitempty"`
}

// Run é uma limpeza feita (pela tela ou automática).
type Run struct {
	T      int64   `json:"t"`
	By     string  `json:"by"` // usuário; "" = automática
	Steps  []Step  `json:"steps"`
	Freed  uint64  `json:"freed"`
	Before float64 `json:"before"` // % do disco antes
	After  float64 `json:"after"`
	Note   string  `json:"note,omitempty"`
}

type fileData struct {
	Auto     Auto  `json:"auto"`
	Runs     []Run `json:"runs"`
	LastAuto int64 `json:"lastAuto"`
}

type Service struct {
	dk     Docker
	disk   Disk
	path   string
	reqDir string
	now    func() time.Time
	settle time.Duration // espera o disco "assentar" antes de medir o depois
	server func() string
	audit  func(kind, title, text string)

	mu      sync.Mutex
	f       fileData
	running bool
}

// New carrega a configuração salva. reqDir é a pasta dividida com o vpserver-cleaner.
func New(dk Docker, disk Disk, dataDir, reqDir string) *Service {
	s := &Service{dk: dk, disk: disk, reqDir: reqDir, now: time.Now, settle: 6 * time.Second,
		server: func() string { return "servidor" }, f: fileData{Auto: defaultAuto()}}
	if dataDir != "" {
		s.path = filepath.Join(dataDir, "cleanup.json")
		if b, err := os.ReadFile(s.path); err == nil {
			json.Unmarshal(b, &s.f)
		}
	}
	return s
}

// SetNotify liga o aviso pelo WhatsApp (tipo "cleanup") e o nome do servidor nas mensagens.
func (s *Service) SetNotify(server func() string, audit func(kind, title, text string)) {
	s.server, s.audit = server, audit
}

// View é a tela.
type View struct {
	Disk       monitor.DiskSnapshot `json:"disk"`
	LogsHelper bool                 `json:"logsHelper"` // o vpserver-cleaner está rodando
	Auto       Auto                 `json:"auto"`
	Runs       []Run                `json:"runs"` // mais recente primeiro
	Running    bool                 `json:"running"`
	LastAuto   int64                `json:"lastAuto"`
}

func (s *Service) View() View {
	v := View{Disk: s.disk.DiskSnapshot(), LogsHelper: s.helperAlive()}
	s.mu.Lock()
	defer s.mu.Unlock()
	v.Auto, v.Running, v.LastAuto, v.Runs = s.f.Auto, s.running, s.f.LastAuto, make([]Run, 0, len(s.f.Runs))
	for i := len(s.f.Runs) - 1; i >= 0; i-- {
		v.Runs = append(v.Runs, s.f.Runs[i])
	}
	return v
}

func (s *Service) helperAlive() bool {
	if s.reqDir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(s.reqDir, ".alive"))
	return err == nil && s.now().Sub(st.ModTime()) < aliveWithin
}

// SaveAuto valida e grava a limpeza automática.
func (s *Service) SaveAuto(a Auto) (Auto, error) {
	switch {
	case a.Threshold < 50 || a.Threshold > 98:
		return Auto{}, errors.New("o limite do disco vai de 50% a 98%")
	case a.LogsOverMB < 10 || a.LogsOverMB > 100_000:
		return Auto{}, errors.New("o tamanho mínimo dos logs vai de 10 MB a 100 GB")
	case a.Enabled && !a.BuildCache && !a.Dangling && !a.Logs:
		return Auto{}, errors.New("marque pelo menos uma coisa para a limpeza automática")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Auto = a
	return a, s.saveLocked()
}

// Start começa uma limpeza pedida na tela (roda em segundo plano).
func (s *Service) Start(by string, req Request) error {
	if req.empty() {
		return ErrNothing
	}
	if len(req.Logs) > 0 {
		known := map[string]bool{}
		for _, l := range s.disk.DiskSnapshot().Logs {
			known[l.ID] = true
		}
		for _, id := range req.Logs {
			if !known[id] {
				return ErrUnknownCt
			}
		}
		if !s.helperAlive() {
			return ErrNoHelper
		}
	}
	if !s.begin() {
		return ErrBusy
	}
	go s.exec(context.Background(), by, req, "")
	return nil
}

func (s *Service) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func pct(d monitor.DiskSnapshot) float64 {
	if d.FSTotal == 0 {
		return 0
	}
	return float64(d.FSUsed) / float64(d.FSTotal) * 100
}

// Tick confere a limpeza automática (o Run chama a cada minuto).
func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	a, last, running := s.f.Auto, s.f.LastAuto, s.running
	s.mu.Unlock()
	if !a.Enabled || running {
		return
	}
	snap := s.disk.DiskSnapshot()
	p := pct(snap)
	if snap.FSTotal == 0 || p < float64(a.Threshold) || s.now().Sub(time.Unix(last, 0)) < autoGap {
		return
	}
	var req Request
	req.BuildCache = a.BuildCache && snap.BuildCache > 0
	req.Dangling = a.Dangling && snap.DanglingCount > 0
	if a.Logs && s.helperAlive() {
		for _, l := range snap.Logs {
			if l.Size > uint64(a.LogsOverMB)<<20 {
				req.Logs = append(req.Logs, l.ID)
			}
		}
	}
	if !s.begin() {
		return
	}
	s.mu.Lock()
	s.f.LastAuto = s.now().Unix()
	s.mu.Unlock()
	note := ""
	if req.empty() {
		note = "Nada do que está marcado tinha o que limpar."
	}
	s.exec(ctx, "", req, note)
}

// Run confere a limpeza automática a cada minuto.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

func (s *Service) exec(ctx context.Context, by string, req Request, note string) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	before := s.disk.DiskSnapshot()
	run := Run{T: s.now().Unix(), By: by, Before: pct(before), Note: note, Steps: []Step{}}
	prune := func(item string, fn func(context.Context) (docker.Pruned, error)) {
		c, cancel := context.WithTimeout(ctx, pruneWait)
		defer cancel()
		p, err := fn(c)
		st := Step{Item: item, Freed: uint64(max(0, p.Freed)), Removed: p.Removed}
		if err != nil {
			st.Error = err.Error()
		}
		run.Steps = append(run.Steps, st)
	}
	if req.BuildCache {
		prune("build_cache", s.dk.PruneBuildCache)
	}
	if req.Dangling {
		prune("dangling", s.dk.PruneDanglingImages)
	}
	if len(req.Logs) > 0 {
		names := map[string]string{}
		for _, l := range before.Logs {
			names[l.ID] = l.Name
		}
		st := Step{Item: "logs"}
		for _, id := range req.Logs {
			st.Names = append(st.Names, names[id])
		}
		freed, n, err := s.truncateLogs(ctx, req.Logs)
		st.Freed, st.Removed = freed, n
		if err != nil {
			st.Error = err.Error()
		}
		run.Steps = append(run.Steps, st)
		s.disk.RefreshStorage()
	}
	for _, st := range run.Steps {
		run.Freed += st.Freed
	}
	if !req.empty() {
		if s.settle > 0 {
			time.Sleep(s.settle) // a próxima leitura do disco já vem com o espaço liberado
		}
		s.disk.RefreshDisk(ctx)
	}
	run.After = pct(s.disk.DiskSnapshot())
	if run.After == 0 {
		run.After = run.Before
	}
	slog.Info("limpeza do disco", "por", byText(by), "liberado", run.Freed, "passos", len(run.Steps))

	s.mu.Lock()
	s.f.Runs = append(s.f.Runs, run)
	if len(s.f.Runs) > maxRuns {
		s.f.Runs = s.f.Runs[len(s.f.Runs)-maxRuns:]
	}
	s.saveLocked()
	s.mu.Unlock()
	if s.audit != nil && (run.Freed > 0 || failed(run) || by != "") {
		s.audit("cleanup", title(run), Message(s.server(), run))
	}
}

func failed(r Run) bool {
	for _, st := range r.Steps {
		if st.Error != "" {
			return true
		}
	}
	return false
}

func byText(by string) string {
	if by == "" {
		return "automática"
	}
	return by
}

// truncateLogs pede ao vpserver-cleaner para zerar os logs destes contêineres
// e espera a resposta (bytes liberados, arquivos mexidos).
func (s *Service) truncateLogs(ctx context.Context, ids []string) (uint64, int, error) {
	if !s.helperAlive() {
		return 0, 0, ErrNoHelper
	}
	b := make([]byte, 8)
	rand.Read(b)
	nonce := hex.EncodeToString(b)
	var body strings.Builder
	for _, id := range ids {
		body.WriteString(id + "\n")
	}
	tmp := filepath.Join(s.reqDir, "tmp-"+nonce)
	if err := os.WriteFile(tmp, []byte(body.String()), 0o644); err != nil {
		return 0, 0, fmt.Errorf("não consegui pedir a limpeza dos logs: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(s.reqDir, "req-"+nonce)); err != nil {
		os.Remove(tmp)
		return 0, 0, fmt.Errorf("não consegui pedir a limpeza dos logs: %w", err)
	}
	res := filepath.Join(s.reqDir, "res-"+nonce)
	deadline := s.now().Add(helperWait)
	for {
		if raw, err := os.ReadFile(res); err == nil {
			os.Remove(res)
			var freed uint64
			n := 0
			for _, ln := range strings.Split(string(raw), "\n") {
				size, _, ok := strings.Cut(strings.TrimSpace(ln), " ")
				if v, err := strconv.ParseUint(size, 10, 64); ok && err == nil {
					freed += v
					n++
				}
			}
			return freed, n, nil
		}
		if s.now().After(deadline) {
			os.Remove(filepath.Join(s.reqDir, "req-"+nonce))
			return 0, 0, errors.New("o ajudante de limpeza dos logs não respondeu a tempo")
		}
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (s *Service) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s.f, "", " ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
