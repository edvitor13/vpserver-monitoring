// Package backup faz os backups dos bancos de dados dos contêineres: dump
// dentro do contêiner (docker exec com comando fixo), cifrado na hora com age
// (o servidor só tem a chave pública), enviado a um bucket S3/R2, conferido
// pelo tamanho, com agenda, níveis (hourly/daily/monthly), retenção,
// histórico e avisos. Só administradores mexem (a API confere).
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/age"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
	"github.com/edvitor13/vpserver-monitoring/internal/s3"
)

// Execer é o docker exec (o cliente do Docker).
type Execer interface {
	Exec(ctx context.Context, container string, cmd, env []string, stdout io.Writer) (int, string, error)
}

// Store é o bucket (o cliente S3); trocável nos testes.
type Store interface {
	PutFile(ctx context.Context, key string, f *os.File) error
	Head(ctx context.Context, key string) (int64, error)
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string, max int) ([]s3.Object, error)
}

// Source é o monitor: os contêineres e o nome do servidor.
type Source interface {
	Containers() []monitor.ContainerInfo
	ServerName() string
}

var everies = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour}

const (
	retryAfter = 15 * time.Minute // depois de uma falha, tenta de novo em 15 min
	dumpLimit  = 3 * time.Hour
	maxRuns    = 60
	tmpDirName = "backup-tmp"
)

// Retention é quanto tempo guardar cada nível (dias); ByBucket = quem apaga é o bucket.
type Retention struct {
	Hourly   int  `json:"hourly"`
	Daily    int  `json:"daily"`
	Monthly  int  `json:"monthly"`
	ByBucket bool `json:"byBucket"`
	DailyAt  int  `json:"dailyAt"` // hora (UTC) a partir da qual o backup do dia vira o "daily"
}

// Target é um banco escolhido para backup.
type Target struct {
	ID          string `json:"id"` // app/serviço (estável entre recriações do contêiner)
	Engine      string `json:"engine"`
	Enabled     bool   `json:"enabled"`
	Every       string `json:"every"`              // 1h | 6h | 24h
	Database    string `json:"database,omitempty"` // opcional: VPMON_DB
	LastRun     int64  `json:"lastRun,omitempty"`
	LastOK      int64  `json:"lastOk,omitempty"`
	LastErr     string `json:"lastErr,omitempty"`
	LastSize    int64  `json:"lastSize,omitempty"`
	LastKey     string `json:"lastKey,omitempty"`
	LastDaily   string `json:"lastDaily,omitempty"`   // AAAA-MM-DD do último daily
	LastMonthly string `json:"lastMonthly,omitempty"` // AAAA-MM do último monthly
	EnabledAt   int64  `json:"enabledAt,omitempty"`
}

// Run é um backup feito (ou tentado).
type Run struct {
	T        int64  `json:"t"`
	Target   string `json:"target"`
	Engine   string `json:"engine"`
	By       string `json:"by"` // usuário; "" = agendado
	Key      string `json:"key,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Size     int64  `json:"size"` // cifrado
	Plain    int64  `json:"plain"`
	Seconds  int64  `json:"seconds"`
	Error    string `json:"error,omitempty"`
	Pruned   int    `json:"pruned,omitempty"` // arquivos velhos apagados
	PruneErr string `json:"pruneErr,omitempty"`
}

type fileData struct {
	Storage   s3.Config          `json:"storage"`
	Prefix    string             `json:"prefix"`
	PublicKey string             `json:"publicKey"`
	Retention Retention          `json:"retention"`
	Targets   map[string]*Target `json:"targets"`
	Runs      []Run              `json:"runs"`
}

type Service struct {
	src    Source
	dk     Execer
	path   string
	tmp    string
	now    func() time.Time
	open   func(s3.Config) (Store, error)
	notify func(kind, title, text string)

	mu      sync.Mutex
	f       fileData
	running string // banco em backup agora
	queue   []queued
	wake    chan struct{}
}

type queued struct{ id, by string }

func defaults() fileData {
	return fileData{Prefix: "vpserver", Retention: Retention{Hourly: 2, Daily: 30, Monthly: 180, DailyAt: 6}, Targets: map[string]*Target{}}
}

// New carrega o que está salvo (backup.json, 600) e limpa temporários antigos.
func New(src Source, dk Execer, dataDir string) *Service {
	s := &Service{src: src, dk: dk, now: time.Now, f: defaults(), wake: make(chan struct{}, 1),
		open: func(c s3.Config) (Store, error) { return s3.New(c) }}
	if dataDir != "" {
		s.path = filepath.Join(dataDir, "backup.json")
		s.tmp = filepath.Join(dataDir, tmpDirName)
		os.RemoveAll(s.tmp) // sobra de um backup interrompido
		if b, err := os.ReadFile(s.path); err == nil {
			json.Unmarshal(b, &s.f)
			if s.f.Targets == nil {
				s.f.Targets = map[string]*Target{}
			}
		}
	}
	return s
}

// SetNotify liga os avisos (tipo "backup").
func (s *Service) SetNotify(fn func(kind, title, text string)) { s.notify = fn }

// --- a tela --------------------------------------------------------------------------------

// DB é um banco encontrado (com o que estiver configurado).
type DB struct {
	Target
	App       string `json:"app"`
	AppName   string `json:"appName"`
	Service   string `json:"service"`
	Container string `json:"container"`
	Image     string `json:"image"`
	State     string `json:"state"`
	Found     bool   `json:"found"` // o contêiner existe agora
	Next      int64  `json:"next,omitempty"`
}

type View struct {
	Configured bool      `json:"configured"`
	Storage    StorageV  `json:"storage"`
	Prefix     string    `json:"prefix"`
	Server     string    `json:"server"`
	PublicKey  string    `json:"publicKey"`
	Retention  Retention `json:"retention"`
	DBs        []DB      `json:"dbs"`
	Engines    []*Engine `json:"engines"`
	Runs       []Run     `json:"runs"`
	Running    string    `json:"running"`
	Queue      []string  `json:"queue"`
}

// StorageV é o armazenamento sem o segredo.
type StorageV struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"accessKey"`
	HasSecret bool   `json:"hasSecret"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "servidor"
	}
	return s
}

// targetID: app/serviço do Compose; contêiner solto = o nome dele.
func targetID(c monitor.ContainerInfo) string {
	if c.Project != "" && c.Service != "" {
		return c.Project + "/" + c.Service
	}
	return c.Name
}

func (s *Service) View() View {
	// o monitor é consultado ANTES de travar: ele chama Alerts (que trava) com a trava dele
	cts, server := s.src.Containers(), slug(s.src.ServerName())
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Prefix: s.f.Prefix, Server: server, PublicKey: s.f.PublicKey, Retention: s.f.Retention,
		Engines: Engines, Running: s.running, DBs: []DB{}, Runs: make([]Run, 0, len(s.f.Runs)), Queue: []string{},
		Storage: StorageV{Endpoint: s.f.Storage.Endpoint, Region: s.f.Storage.Region, Bucket: s.f.Storage.Bucket,
			AccessKey: s.f.Storage.AccessKey, HasSecret: s.f.Storage.SecretKey != ""}}
	v.Configured = v.Storage.HasSecret && v.PublicKey != ""
	seen := map[string]bool{}
	for _, c := range cts {
		eng := Detect(c.Image)
		if eng == "" {
			continue
		}
		id := targetID(c)
		if seen[id] {
			continue // réplicas: uma vez só
		}
		seen[id] = true
		db := DB{Target: Target{ID: id, Engine: eng, Every: "1h"}, App: c.AppKey, AppName: c.AppName, Service: c.Service,
			Container: c.Name, Image: c.Image, State: c.State, Found: true}
		if t := s.f.Targets[id]; t != nil {
			db.Target = *t
			db.Engine = eng
		}
		db.Next = s.nextLocked(&db.Target)
		v.DBs = append(v.DBs, db)
	}
	for id, t := range s.f.Targets { // configurado, mas o contêiner sumiu
		if !seen[id] && t.Enabled {
			v.DBs = append(v.DBs, DB{Target: *t, Container: id, Found: false})
		}
	}
	sort.SliceStable(v.DBs, func(i, j int) bool {
		if v.DBs[i].Enabled != v.DBs[j].Enabled {
			return v.DBs[i].Enabled
		}
		return v.DBs[i].ID < v.DBs[j].ID
	})
	for i := len(s.f.Runs) - 1; i >= 0; i-- {
		v.Runs = append(v.Runs, s.f.Runs[i])
	}
	for _, q := range s.queue {
		v.Queue = append(v.Queue, q.id)
	}
	return v
}

func (s *Service) nextLocked(t *Target) int64 {
	if !t.Enabled {
		return 0
	}
	every := everies[t.Every]
	if t.LastRun == 0 {
		return s.now().Unix()
	}
	if t.LastErr != "" && t.LastRun >= t.LastOK {
		return t.LastRun + int64(retryAfter/time.Second)
	}
	return t.LastRun + int64(every/time.Second)
}

// --- configuração ---------------------------------------------------------------------------

var prefixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// SaveStorage testa (grava, confere, lista e apaga um arquivo: as permissões
// que os backups usam) e só então guarda. secret vazio = mantém o salvo.
func (s *Service) SaveStorage(ctx context.Context, c s3.Config, prefix string) (StorageV, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "vpserver"
	}
	if !prefixRe.MatchString(prefix) {
		return StorageV{}, errors.New("prefixo inválido (letras minúsculas, números, ponto, hífen e sublinhado)")
	}
	s.mu.Lock()
	if strings.TrimSpace(c.SecretKey) == "" {
		c.SecretKey = s.f.Storage.SecretKey
	}
	s.mu.Unlock()
	if c.Region == "" {
		c.Region = "auto"
	}
	st, err := s.open(c)
	if err != nil {
		return StorageV{}, err
	}
	if err := testStore(ctx, st, prefix+"/"+slug(s.src.ServerName())+"/.vpserver-teste"); err != nil {
		return StorageV{}, err
	}
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.AccessKey, c.SecretKey = strings.TrimSpace(c.AccessKey), strings.TrimSpace(c.SecretKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Storage, s.f.Prefix = c, prefix
	if err := s.saveLocked(); err != nil {
		return StorageV{}, err
	}
	return StorageV{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, AccessKey: c.AccessKey, HasSecret: true}, nil
}

func testStore(ctx context.Context, st Store, key string) error {
	tmp, err := os.CreateTemp("", "vpserver-teste-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	body := "teste do VPServer: pode apagar\n"
	tmp.WriteString(body)
	if err := st.PutFile(ctx, key, tmp); err != nil {
		return fmt.Errorf("gravar no bucket: %w", err)
	}
	if n, err := st.Head(ctx, key); err != nil || n != int64(len(body)) {
		if err == nil {
			err = errors.New("tamanho diferente do enviado")
		}
		return fmt.Errorf("conferir no bucket: %w", err)
	}
	if _, err := st.List(ctx, key, 10); err != nil {
		return fmt.Errorf("listar o bucket (precisa para a retenção): %w", err)
	}
	if err := st.Delete(ctx, key); err != nil {
		return fmt.Errorf("apagar do bucket (precisa para a retenção): %w", err)
	}
	return nil
}

// GenerateKey cria o par: guarda a pública e devolve a privada (só desta vez).
func (s *Service) GenerateKey() (pub, priv string, err error) {
	id, err := age.GenerateIdentity()
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.PublicKey = id.Recipient().String()
	return s.f.PublicKey, id.String(), s.saveLocked()
}

// SetPublicKey usa uma chave pública que o administrador já tem.
func (s *Service) SetPublicKey(pub string) (string, error) {
	r, err := age.ParseRecipient(pub)
	if err != nil {
		return "", errors.New("chave pública inválida (começa com age1...)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.PublicKey = r.String()
	return s.f.PublicKey, s.saveLocked()
}

func (s *Service) SaveRetention(r Retention) (Retention, error) {
	switch {
	case r.Hourly < 1 || r.Hourly > 30:
		return Retention{}, errors.New("os horários ficam de 1 a 30 dias")
	case r.Daily < 1 || r.Daily > 365:
		return Retention{}, errors.New("os diários ficam de 1 a 365 dias")
	case r.Monthly < 1 || r.Monthly > 3650:
		return Retention{}, errors.New("os mensais ficam de 1 a 3650 dias")
	case r.DailyAt < 0 || r.DailyAt > 23:
		return Retention{}, errors.New("a hora do diário vai de 0 a 23 (UTC)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Retention = r
	return r, s.saveLocked()
}

var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.$-]{0,63}$`)

// SetTarget liga/desliga e ajusta um banco encontrado.
func (s *Service) SetTarget(id string, enabled bool, every, database string) (Target, error) {
	var found *monitor.ContainerInfo
	for _, c := range s.src.Containers() {
		if targetID(c) == id && Detect(c.Image) != "" {
			c := c
			found = &c
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.f.Targets[id]
	if found == nil && (t == nil || enabled) {
		return Target{}, errors.New("banco não encontrado entre os contêineres")
	}
	if _, ok := everies[every]; !ok {
		return Target{}, errors.New("frequência inválida")
	}
	database = strings.TrimSpace(database)
	if database != "" && !dbNameRe.MatchString(database) {
		return Target{}, errors.New("nome de banco inválido")
	}
	if enabled && (s.f.Storage.SecretKey == "" || s.f.PublicKey == "") {
		return Target{}, errors.New("configure o armazenamento e a chave antes de ligar um backup")
	}
	if t == nil {
		t = &Target{ID: id}
		s.f.Targets[id] = t
	}
	if found != nil {
		t.Engine = Detect(found.Image)
	}
	if enabled && !t.Enabled {
		t.EnabledAt = s.now().Unix()
	}
	t.Enabled, t.Every, t.Database = enabled, every, database
	return *t, s.saveLocked()
}

// --- execução ------------------------------------------------------------------------------

// RunNow põe um banco na fila agora.
func (s *Service) RunNow(id, by string) error {
	s.mu.Lock()
	t := s.f.Targets[id]
	if t == nil || !t.Enabled {
		s.mu.Unlock()
		return errors.New("ligue o backup deste banco primeiro")
	}
	if s.running == id {
		s.mu.Unlock()
		return errors.New("o backup deste banco já está rodando")
	}
	for _, q := range s.queue {
		if q.id == id {
			s.mu.Unlock()
			return errors.New("o backup deste banco já está na fila")
		}
	}
	s.queue = append(s.queue, queued{id: id, by: by})
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Run agenda os vencidos a cada minuto e faz um backup por vez.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.enqueueDue()
		for s.step(ctx) {
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

func (s *Service) enqueueDue() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().Unix()
	ids := make([]string, 0, len(s.f.Targets))
	for id := range s.f.Targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
outer:
	for _, id := range ids {
		t := s.f.Targets[id]
		if !t.Enabled || s.running == id || s.nextLocked(t) > now {
			continue
		}
		for _, q := range s.queue {
			if q.id == id {
				continue outer
			}
		}
		s.queue = append(s.queue, queued{id: id})
	}
}

// step faz o próximo da fila (false = fila vazia).
func (s *Service) step(ctx context.Context) bool {
	s.mu.Lock()
	if len(s.queue) == 0 || s.running != "" {
		s.mu.Unlock()
		return false
	}
	q := s.queue[0]
	s.queue = s.queue[1:]
	s.running = q.id
	s.mu.Unlock()
	s.backup(ctx, q.id, q.by)
	s.mu.Lock()
	s.running = ""
	s.mu.Unlock()
	return true
}

// tierFor decide o nível deste backup (o primeiro do mês/dia depois da hora vira monthly/daily).
func tierFor(t *Target, now time.Time, dailyAt int) string {
	u := now.UTC()
	if u.Hour() >= dailyAt || t.Every == "24h" {
		if t.LastMonthly != u.Format("2006-01") {
			return "monthly"
		}
		if t.LastDaily != u.Format("2006-01-02") {
			return "daily"
		}
	}
	return "hourly"
}

// counting guarda o começo e o fim do que passa (para conferir o dump).
type counting struct {
	w    io.Writer
	n    int64
	head []byte
	tail []byte
}

func (c *counting) Write(p []byte) (int, error) {
	if len(c.head) < 64 {
		c.head = append(c.head, p[:min(len(p), 64-len(c.head))]...)
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > 512 {
		c.tail = c.tail[len(c.tail)-512:]
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

func (s *Service) backup(ctx context.Context, id, by string) {
	start := s.now()
	s.mu.Lock()
	t := s.f.Targets[id]
	if t == nil {
		s.mu.Unlock()
		return
	}
	tg := *t
	cfg, pub, prefix, ret := s.f.Storage, s.f.PublicKey, s.f.Prefix, s.f.Retention
	s.mu.Unlock()
	run := Run{T: start.Unix(), Target: id, Engine: tg.Engine, By: by}
	tier := tierFor(&tg, start, ret.DailyAt)
	err := func() error {
		eng := engineByKey(tg.Engine)
		if eng == nil {
			return errors.New("tipo de banco desconhecido")
		}
		rcp, err := age.ParseRecipient(pub)
		if err != nil {
			return errors.New("configure a chave de criptografia")
		}
		st, err := s.open(cfg)
		if err != nil {
			return fmt.Errorf("armazenamento: %w", err)
		}
		var ct string
		for _, c := range s.src.Containers() {
			if targetID(c) == id && c.State == "running" {
				ct = c.ID
				break
			}
		}
		if ct == "" {
			return errors.New("o contêiner do banco não está rodando")
		}
		if err := os.MkdirAll(s.tmp, 0o700); err != nil {
			return err
		}
		b := make([]byte, 6)
		rand.Read(b)
		tmpPath := filepath.Join(s.tmp, hex.EncodeToString(b)+".age")
		f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer os.Remove(tmpPath)
		defer f.Close()
		enc, err := age.Encrypt(f, rcp)
		if err != nil {
			return err
		}
		cw := &counting{w: enc}
		var env []string
		if tg.Database != "" {
			env = append(env, "VPMON_DB="+tg.Database)
		}
		dctx, cancel := context.WithTimeout(ctx, dumpLimit)
		code, stderr, err := s.dk.Exec(dctx, ct, []string{"sh", "-c", eng.script}, env, cw)
		cancel()
		if err != nil {
			return fmt.Errorf("dump: %w", err)
		}
		if code != 0 {
			if stderr == "" {
				stderr = "sem mensagem"
			}
			return fmt.Errorf("o dump terminou com erro (código %d): %s", code, short(stderr))
		}
		if err := enc.Close(); err != nil {
			return err
		}
		run.Plain = cw.n
		if cw.n == 0 {
			return errors.New("o dump saiu vazio")
		}
		if err := eng.check(cw.head, cw.tail); err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			return err
		}
		run.Size = info.Size()
		stamp := start.UTC().Format("2006-01-02T15-04Z")
		name := slug(strings.ReplaceAll(id, "/", "-"))
		key := fmt.Sprintf("%s/%s/%s/%s/%s-%s.%s.age", prefix, slug(s.src.ServerName()), name, tier, name, stamp, eng.Ext)
		uctx, ucancel := context.WithTimeout(ctx, dumpLimit)
		defer ucancel()
		if err := st.PutFile(uctx, key, f); err != nil {
			return fmt.Errorf("envio: %w", err)
		}
		if n, err := st.Head(uctx, key); err != nil || n != run.Size {
			if err == nil {
				err = fmt.Errorf("no bucket tem %d bytes, enviados %d", n, run.Size)
			}
			return fmt.Errorf("conferência no bucket: %w", err)
		}
		run.Key, run.Tier = key, tier
		if !ret.ByBucket {
			run.Pruned, run.PruneErr = s.prune(uctx, st, fmt.Sprintf("%s/%s/%s/", prefix, slug(s.src.ServerName()), name), ret, start)
		}
		return nil
	}()
	run.Seconds = int64(s.now().Sub(start).Seconds())
	if err != nil {
		run.Error = short(err.Error())
	}
	s.record(id, run, tier, err == nil, s.src.ServerName())
}

// prune apaga o que passou da retenção de cada nível deste banco.
func (s *Service) prune(ctx context.Context, st Store, base string, ret Retention, now time.Time) (int, string) {
	n := 0
	for tier, days := range map[string]int{"hourly": ret.Hourly, "daily": ret.Daily, "monthly": ret.Monthly} {
		objs, err := st.List(ctx, base+tier+"/", 5000)
		if err != nil {
			return n, short(err.Error())
		}
		limit := now.Add(-time.Duration(days) * 24 * time.Hour)
		for _, o := range objs {
			if !o.Modified.IsZero() && o.Modified.Before(limit) && strings.HasSuffix(o.Key, ".age") {
				if err := st.Delete(ctx, o.Key); err != nil {
					return n, short(err.Error())
				}
				n++
			}
		}
	}
	return n, ""
}

func (s *Service) record(id string, run Run, tier string, ok bool, server string) {
	s.mu.Lock()
	t := s.f.Targets[id]
	wasFailing := false
	if t != nil {
		wasFailing = t.LastErr != ""
		t.LastRun, t.LastErr = run.T, run.Error
		if ok {
			t.LastOK, t.LastSize, t.LastKey = run.T, run.Size, run.Key
			u := time.Unix(run.T, 0).UTC()
			switch tier {
			case "monthly":
				t.LastMonthly, t.LastDaily = u.Format("2006-01"), u.Format("2006-01-02")
			case "daily":
				t.LastDaily = u.Format("2006-01-02")
			}
		}
	}
	s.f.Runs = append(s.f.Runs, run)
	if len(s.f.Runs) > maxRuns {
		s.f.Runs = s.f.Runs[len(s.f.Runs)-maxRuns:]
	}
	s.saveLocked()
	s.mu.Unlock()
	if ok {
		slog.Info("backup feito", "banco", id, "nivel", tier, "bytes", run.Size, "segundos", run.Seconds)
	} else {
		slog.Warn("backup falhou", "banco", id, "erro", run.Error)
	}
	if s.notify == nil {
		return
	}
	switch {
	case !ok && !wasFailing:
		s.notify("backup", "Backup falhou: "+id, fmt.Sprintf("❌ *Backup falhou · %s*\nBanco *%s*: %s\nO painel tenta de novo em 15 min.", server, id, run.Error))
	case ok && wasFailing:
		s.notify("backup", "Backup voltou: "+id, fmt.Sprintf("✅ *Backup voltou a funcionar · %s*\nBanco *%s*: %s enviado.", server, id, human(run.Size)))
	}
}

// Alerts: backups ligados que estão atrasados ou falhando (aparecem em Infos).
func (s *Service) Alerts() []monitor.Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []monitor.Alert
	for id, t := range s.f.Targets {
		if !t.Enabled {
			continue
		}
		every := everies[t.Every]
		since := t.LastOK
		if since == 0 {
			since = t.EnabledAt
		}
		late := since > 0 && now.Sub(time.Unix(since, 0)) > 2*every+30*time.Minute
		switch {
		case late:
			when := "nunca deu certo"
			if t.LastOK > 0 {
				when = "o último bom foi há " + humanDur(now.Sub(time.Unix(t.LastOK, 0)))
			}
			out = append(out, monitor.Alert{Key: "backup.late:" + id, Level: "crit", Area: "monitor",
				Title:  "Backup atrasado: " + id,
				Detail: "O backup de " + id + " " + when + "." + map[bool]string{true: " Último erro: " + t.LastErr, false: ""}[t.LastErr != ""]})
		case t.LastErr != "":
			out = append(out, monitor.Alert{Key: "backup.fail:" + id, Level: "warn", Area: "monitor",
				Title: "O último backup de " + id + " falhou", Detail: t.LastErr + " (tenta de novo em 15 min)."})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Objects lista os arquivos de um banco no bucket (mais novos primeiro).
func (s *Service) Objects(ctx context.Context, id string) ([]s3.Object, error) {
	s.mu.Lock()
	cfg, prefix := s.f.Storage, s.f.Prefix
	s.mu.Unlock()
	st, err := s.open(cfg)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("%s/%s/%s/", prefix, slug(s.src.ServerName()), slug(strings.ReplaceAll(id, "/", "-")))
	objs, err := st.List(ctx, base, 2000)
	if err != nil {
		return nil, err
	}
	sort.Slice(objs, func(i, j int) bool { // mais novos primeiro (pela data no nome)
		if a, b := stampOf(objs[i].Key), stampOf(objs[j].Key); a != b {
			return a > b
		}
		return objs[i].Key > objs[j].Key
	})
	if len(objs) > 300 {
		objs = objs[:300]
	}
	return objs, nil
}

var stampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}-\d{2}Z`)

func stampOf(k string) string { return stampRe.FindString(k) }

// Download abre um arquivo do bucket deste servidor (só dentro do prefixo).
func (s *Service) Download(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	cfg, prefix := s.f.Storage, s.f.Prefix
	s.mu.Unlock()
	base := prefix + "/" + slug(s.src.ServerName()) + "/"
	if !strings.HasPrefix(key, base) || strings.Contains(key, "..") || !strings.HasSuffix(key, ".age") {
		return nil, 0, errors.New("arquivo fora dos backups deste servidor")
	}
	st, err := s.open(cfg)
	if err != nil {
		return nil, 0, err
	}
	return st.Get(ctx, key)
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

func human(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", f, units[i]), ".", ",", 1)
}

func humanDur(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d dias", int(d.Hours()/24))
	}
	return fmt.Sprintf("%d h", int(d.Hours()))
}
