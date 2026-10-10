package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/age"
	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
	"github.com/edvitor13/vpserver-monitoring/internal/s3"
)

type fakeSrc struct{ cts []monitor.ContainerInfo }

func (f *fakeSrc) Containers() []monitor.ContainerInfo { return f.cts }
func (f *fakeSrc) ServerName() string                  { return "Servidor de Teste" }

func ct(id, project, service, image, state string) monitor.ContainerInfo {
	return monitor.ContainerInfo{Container: docker.Container{ID: id, Name: project + "-" + service + "-1", Project: project, Service: service,
		Image: image, State: state}, AppKey: project, AppName: strings.ToUpper(project[:1]) + project[1:]}
}

type fakeExec struct {
	mu    sync.Mutex
	out   map[string]string // contêiner → saída
	code  int
	errS  string
	calls []string
	env   [][]string
}

func (f *fakeExec) Exec(_ context.Context, container string, cmd, env []string, stdout io.Writer) (int, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, container+" "+cmd[0]+" "+cmd[1])
	f.env = append(f.env, env)
	io.WriteString(stdout, f.out[container])
	return f.code, f.errS, nil
}

type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
	mod  map[string]time.Time
	fail error
}

func (m *memStore) PutFile(_ context.Context, key string, f *os.File) error {
	if m.fail != nil {
		return m.fail
	}
	f.Seek(0, 0)
	b, _ := io.ReadAll(f)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key], m.mod[key] = b, time.Now()
	return nil
}
func (m *memStore) Head(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return 0, errors.New("não existe")
	}
	return int64(len(b)), nil
}
func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return io.NopCloser(bytes.NewReader(m.objs[key])), int64(len(m.objs[key])), nil
}
func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}
func (m *memStore) List(_ context.Context, prefix string, _ int) ([]s3.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []s3.Object
	for k, b := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, s3.Object{Key: k, Size: int64(len(b)), Modified: m.mod[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (m *memStore) keys(sub string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		if strings.Contains(k, sub) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

type env struct {
	s     *Service
	src   *fakeSrc
	dk    *fakeExec
	st    *memStore
	now   *time.Time
	notes *[]string
}

func setup(t *testing.T) env {
	t.Helper()
	src := &fakeSrc{cts: []monitor.ContainerInfo{
		ct("c1", "loja", "db", "postgres:17-alpine", "running"),
		ct("c1b", "loja", "db", "postgres:17-alpine", "running"), // réplica: aparece uma vez
		ct("c2", "loja", "cache", "redis:7", "running"),
		ct("c3", "blog", "mysql", "mariadb:11", "exited"),
		ct("c4", "loja", "api", "ghcr.io/acme/api:1", "running"),
	}}
	dk := &fakeExec{out: map[string]string{"c1": "PGDMP" + strings.Repeat("dados", 30000), "c2": "REDIS0011..."}}
	st := &memStore{objs: map[string][]byte{}, mod: map[string]time.Time{}}
	s := New(src, dk, t.TempDir())
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC) // antes das 06 UTC
	s.now = func() time.Time { return now }
	s.open = func(s3.Config) (Store, error) { return st, nil }
	var notes []string
	s.SetNotify(func(kind, title, text string) { notes = append(notes, kind+"|"+text) })
	return env{s, src, dk, st, &now, &notes}
}

func storage() s3.Config {
	return s3.Config{Endpoint: "https://conta.r2.cloudflarestorage.com", Bucket: "backups", AccessKey: "ak", SecretKey: "sk"}
}

func TestDiscoveryAndSetup(t *testing.T) {
	e := setup(t)
	v := e.s.View()
	if v.Configured || len(v.DBs) != 3 {
		t.Fatalf("bancos encontrados (sem a api, réplica uma vez): %+v", v.DBs)
	}
	ids := []string{v.DBs[0].ID, v.DBs[1].ID, v.DBs[2].ID}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "blog/mysql,loja/cache,loja/db" || v.Server != "servidor-de-teste" {
		t.Fatalf("ids: %v servidor %q", ids, v.Server)
	}
	if _, err := e.s.SetTarget("loja/db", true, "1h", ""); err == nil {
		t.Fatal("sem armazenamento e chave não liga")
	}
	if _, err := e.s.SaveStorage(context.Background(), storage(), "Prefixo Ruim"); err == nil {
		t.Fatal("prefixo inválido")
	}
	sv, err := e.s.SaveStorage(context.Background(), storage(), "")
	if err != nil || !sv.HasSecret || len(e.st.objs) != 0 {
		t.Fatalf("teste do armazenamento grava, confere e apaga: %+v %v %v", sv, err, e.st.objs)
	}
	// salvar de novo sem o segredo mantém o anterior
	c := storage()
	c.SecretKey = ""
	if _, err := e.s.SaveStorage(context.Background(), c, "backups-x"); err != nil || e.s.f.Storage.SecretKey != "sk" || e.s.f.Prefix != "backups-x" {
		t.Fatalf("segredo mantido: %v", err)
	}
	if _, err := e.s.SetPublicKey("age1invalida"); err == nil {
		t.Fatal("chave pública inválida")
	}
	pub, priv, err := e.s.GenerateKey()
	if err != nil || !strings.HasPrefix(pub, "age1") || !strings.HasPrefix(priv, "AGE-SECRET-KEY-1") {
		t.Fatal("gerar o par")
	}
	if b, _ := os.ReadFile(e.s.path); bytes.Contains(b, []byte(priv)) || !bytes.Contains(b, []byte(pub)) {
		t.Fatal("o servidor guarda só a pública")
	}
	for _, bad := range []struct{ every, db string }{{"2h", ""}, {"1h", "nome com espaço"}, {"1h", "x;rm"}} {
		if _, err := e.s.SetTarget("loja/db", true, bad.every, bad.db); err == nil {
			t.Errorf("aceitou %+v", bad)
		}
	}
	if _, err := e.s.SetTarget("loja/api", true, "1h", ""); err == nil {
		t.Fatal("contêiner que não é banco")
	}
	if tg, err := e.s.SetTarget("loja/db", true, "6h", "principal"); err != nil || !tg.Enabled || tg.Engine != "postgres" {
		t.Fatalf("ligar: %+v %v", tg, err)
	}
}

func TestBackupEndToEnd(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.s.SaveStorage(ctx, storage(), "")
	_, priv, _ := e.s.GenerateKey()
	e.s.SetTarget("loja/db", true, "1h", "principal")

	// às 03 UTC: horário (o diário só depois das 06)
	e.s.enqueueDue()
	if !e.s.step(ctx) {
		t.Fatal("deveria rodar")
	}
	keys := e.st.keys("/hourly/")
	if len(keys) != 1 || keys[0] != "vpserver/servidor-de-teste/loja-db/hourly/loja-db-2026-10-09T03-00Z.dump.age" {
		t.Fatalf("chave: %v", keys)
	}
	id, _ := age.ParseIdentity(priv)
	plain, err := age.DecryptBytes(e.st.objs[keys[0]], id)
	if err != nil || string(plain) != e.dk.out["c1"] {
		t.Fatalf("o arquivo no bucket abre com a chave privada e é o dump: %v", err)
	}
	if strings.Join(e.dk.env[0], ",") != "VPMON_DB=principal" || !strings.HasPrefix(e.dk.calls[0], "c1 sh -c") {
		t.Fatalf("exec: %v %v", e.dk.calls, e.dk.env)
	}
	v := e.s.View()
	r := v.Runs[0]
	if r.Error != "" || r.Tier != "hourly" || r.Plain != int64(len(e.dk.out["c1"])) || r.Size != int64(len(e.st.objs[keys[0]])) {
		t.Fatalf("registro: %+v", r)
	}

	// cedo demais para o próximo; às 07 UTC: vira o mensal (o 1º do mês), depois o diário já está coberto
	e.s.enqueueDue()
	if e.s.step(ctx) {
		t.Fatal("antes de 1 h não roda")
	}
	*e.now = e.now.Add(4 * time.Hour)
	e.s.enqueueDue()
	e.s.step(ctx)
	if len(e.st.keys("/monthly/")) != 1 {
		t.Fatalf("mensal: %v", e.st.keys(""))
	}
	*e.now = e.now.Add(time.Hour)
	e.s.enqueueDue()
	e.s.step(ctx)
	if len(e.st.keys("/hourly/")) != 2 || len(e.st.keys("/daily/")) != 0 {
		t.Fatalf("depois do mensal, o dia já está coberto: %v", e.st.keys(""))
	}
	*e.now = e.now.Add(24 * time.Hour) // dia seguinte, depois das 06
	e.s.enqueueDue()
	e.s.step(ctx)
	if len(e.st.keys("/daily/")) != 1 {
		t.Fatalf("diário do dia seguinte: %v", e.st.keys(""))
	}

	// download só dentro dos backups deste servidor
	rc, _, err := e.s.Download(ctx, keys[0])
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	for _, bad := range []string{"outro/servidor/x.age", "vpserver/servidor-de-teste/../x.age", "vpserver/servidor-de-teste/loja-db/a.txt"} {
		if _, _, err := e.s.Download(ctx, bad); err == nil {
			t.Errorf("baixou %q", bad)
		}
	}
	objs, _ := e.s.Objects(ctx, "loja/db")
	if len(objs) != 4 || !strings.Contains(objs[0].Key, "daily") {
		t.Fatalf("lista (mais novo primeiro): %+v", objs)
	}
}

func TestFailuresChecksAlertsAndRetention(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.s.SaveStorage(ctx, storage(), "")
	e.s.GenerateKey()
	e.s.SetTarget("loja/db", true, "1h", "")

	// o dump falha: erro com a mensagem, aviso uma vez, tenta de novo em 15 min
	e.dk.code, e.dk.errS = 1, `pg_dump: error: connection to server failed: FATAL:  database "x" does not exist`
	e.s.enqueueDue()
	e.s.step(ctx)
	r := e.s.View().Runs[0]
	if !strings.Contains(r.Error, "código 1") || !strings.Contains(r.Error, "does not exist") || len(e.st.keys("")) != 0 {
		t.Fatalf("falha: %+v", r)
	}
	if len(*e.notes) != 1 || !strings.HasPrefix((*e.notes)[0], "backup|") {
		t.Fatalf("aviso da falha: %v", *e.notes)
	}
	if a := e.s.Alerts(); len(a) != 1 || a[0].Key != "backup.fail:loja/db" {
		t.Fatalf("alerta de falha: %+v", a)
	}
	*e.now = e.now.Add(10 * time.Minute)
	e.s.enqueueDue()
	if e.s.step(ctx) {
		t.Fatal("antes de 15 min não tenta de novo")
	}
	*e.now = e.now.Add(6 * time.Minute)
	e.s.enqueueDue()
	e.s.step(ctx)
	if len(*e.notes) != 1 {
		t.Fatal("falhando de novo: não repete o aviso")
	}

	// saída que não é dump: recusada mesmo com código 0
	e.dk.code, e.dk.errS = 0, ""
	e.dk.out["c1"] = "não é um dump"
	*e.now = e.now.Add(16 * time.Minute)
	e.s.enqueueDue()
	e.s.step(ctx)
	if r := e.s.View().Runs[0]; !strings.Contains(r.Error, "não parece um dump") {
		t.Fatalf("conferência: %+v", r)
	}

	// atrasado: passou do dobro do intervalo sem um bom
	*e.now = e.now.Add(3 * time.Hour)
	if a := e.s.Alerts(); len(a) != 1 || a[0].Level != "crit" || !strings.Contains(a[0].Title, "atrasado") {
		t.Fatalf("alerta de atraso: %+v", a)
	}

	// volta a funcionar: aviso de volta e retenção apaga o velho
	e.dk.out["c1"] = "PGDMP ok"
	old := "vpserver/servidor-de-teste/loja-db/hourly/loja-db-2026-10-01T00-00Z.dump.age"
	e.st.objs[old], e.st.mod[old] = []byte("velho"), e.now.Add(-5*24*time.Hour)
	keep := "vpserver/servidor-de-teste/loja-db/monthly/loja-db-2026-09-01T06-00Z.dump.age"
	e.st.objs[keep], e.st.mod[keep] = []byte("mensal"), e.now.Add(-40*24*time.Hour)
	e.s.RunNow("loja/db", "ana")
	e.s.step(ctx)
	r = e.s.View().Runs[0]
	if r.Error != "" || r.By != "ana" || r.Pruned != 1 {
		t.Fatalf("de volta: %+v", r)
	}
	if _, ok := e.st.objs[old]; ok {
		t.Fatal("o horário de 5 dias deveria ter saído (retenção 2 dias)")
	}
	if _, ok := e.st.objs[keep]; !ok {
		t.Fatal("o mensal de 40 dias fica (retenção 180)")
	}
	if len(*e.notes) != 2 || !strings.Contains((*e.notes)[1], "voltou a funcionar") {
		t.Fatalf("aviso de volta: %v", *e.notes)
	}
	if a := e.s.Alerts(); len(a) != 0 {
		t.Fatalf("sem alertas depois de um bom: %+v", a)
	}

	// retenção pelas regras do bucket: o painel não apaga nada
	e.s.SaveRetention(Retention{Hourly: 2, Daily: 30, Monthly: 180, DailyAt: 6, ByBucket: true})
	e.st.objs[old], e.st.mod[old] = []byte("velho"), e.now.Add(-5*24*time.Hour)
	e.s.RunNow("loja/db", "")
	e.s.step(ctx)
	if _, ok := e.st.objs[old]; !ok {
		t.Fatal("com a retenção no bucket, o painel não apaga")
	}

	// contêiner parado, envio que falha
	e.s.SetTarget("loja/db", false, "1h", "")
	if err := e.s.RunNow("loja/db", ""); err == nil {
		t.Fatal("desligado não roda")
	}
	e.src.cts[0].State, e.src.cts[1].State = "exited", "exited"
	e.s.SetTarget("loja/db", true, "1h", "")
	e.s.RunNow("loja/db", "")
	e.s.step(ctx)
	if r := e.s.View().Runs[0]; !strings.Contains(r.Error, "não está rodando") {
		t.Fatalf("parado: %+v", r)
	}
	e.src.cts[0].State = "running"
	e.st.fail = errors.New("o armazenamento recusou as credenciais")
	e.s.RunNow("loja/db", "")
	e.s.step(ctx)
	if r := e.s.View().Runs[0]; !strings.Contains(r.Error, "envio") || !strings.Contains(r.Error, "credenciais") {
		t.Fatalf("envio: %+v", r)
	}
	if bad, err := e.s.SaveRetention(Retention{Hourly: 0, Daily: 30, Monthly: 180}); err == nil {
		t.Fatalf("retenção inválida: %+v", bad)
	}
}

func TestQueueOneAtATime(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.s.SaveStorage(ctx, storage(), "")
	e.s.GenerateKey()
	e.s.SetTarget("loja/db", true, "24h", "")
	e.s.SetTarget("loja/cache", true, "24h", "")
	if err := e.s.RunNow("loja/db", "ana"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RunNow("loja/db", "ana"); err == nil {
		t.Fatal("o mesmo banco duas vezes na fila")
	}
	e.s.enqueueDue() // o cache entra; o db já está na fila
	if v := e.s.View(); len(v.Queue) != 2 {
		t.Fatalf("fila: %v", v.Queue)
	}
	for e.s.step(ctx) {
	}
	if len(e.st.keys(".age")) != 2 || len(e.st.keys("loja-cache/monthly/loja-cache-")) != 1 {
		t.Fatalf("os dois feitos (diário: o 1º vira mensal): %v", e.st.keys(""))
	}
}

// No contêiner o sistema de arquivos é só leitura fora de /data: o teste do
// armazenamento não pode depender do /tmp.
func TestStorageTestDoesNotNeedSystemTemp(t *testing.T) {
	e := setup(t)
	missing := filepath.Join(t.TempDir(), "nao-existe", "tmp")
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, missing)
	}
	if _, err := e.s.SaveStorage(context.Background(), storage(), ""); err != nil {
		t.Fatalf("com o /tmp indisponível: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(e.s.tmp, "*")); len(left) != 0 {
		t.Fatalf("sobrou arquivo de teste: %v", left)
	}
}
