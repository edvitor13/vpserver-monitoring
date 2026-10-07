package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

const (
	idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeDocker struct {
	mu    sync.Mutex
	calls []string
	fail  error
}

func (d *fakeDocker) PruneBuildCache(context.Context) (docker.Pruned, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "build")
	return docker.Pruned{Freed: 3 << 30, Removed: 12}, d.fail
}
func (d *fakeDocker) PruneDanglingImages(context.Context) (docker.Pruned, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "dangling")
	return docker.Pruned{Freed: 500 << 20, Removed: 4}, nil
}

type fakeDisk struct {
	mu        sync.Mutex
	snap      monitor.DiskSnapshot
	refreshed int
}

func (d *fakeDisk) DiskSnapshot() monitor.DiskSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snap
}
func (d *fakeDisk) RefreshDisk(context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshed++
	d.snap.FSUsed = 60
}
func (d *fakeDisk) RefreshStorage() {}

// helper faz o papel do vpserver-cleaner: lê req-*, responde res-*.
func helper(t *testing.T, dir string, stop chan struct{}) {
	os.WriteFile(filepath.Join(dir, ".alive"), nil, 0o644)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			reqs, _ := filepath.Glob(filepath.Join(dir, "req-*"))
			for _, r := range reqs {
				b, _ := os.ReadFile(r)
				var out strings.Builder
				for _, id := range strings.Fields(string(b)) {
					fmt.Fprintf(&out, "%d /logs/%s/%s-json.log\n", 1<<20, id, id)
				}
				os.Remove(r)
				os.WriteFile(filepath.Join(dir, "res-"+strings.TrimPrefix(filepath.Base(r), "req-")), []byte(out.String()), 0o644)
			}
		}
	}()
}

func setup(t *testing.T) (*Service, *fakeDocker, *fakeDisk, string, *[]string) {
	t.Helper()
	dir, req := t.TempDir(), t.TempDir()
	dk := &fakeDocker{}
	disk := &fakeDisk{snap: monitor.DiskSnapshot{FSUsed: 90, FSTotal: 100, BuildCache: 3 << 30, DanglingCount: 4, DanglingSize: 500 << 20,
		Logs: []monitor.LogFile{{ID: idA, Name: "loja-api", App: "loja", Size: 300 << 20}, {ID: idB, Name: "blog", App: "blog", Size: 5 << 20}}}}
	s := New(dk, disk, dir, req)
	s.settle = 0
	var msgs []string
	var mu sync.Mutex
	s.SetNotify(func() string { return "srv" }, func(kind, title, text string) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, kind+"|"+text)
	})
	return s, dk, disk, req, &msgs
}

func wait(t *testing.T, s *Service) View {
	t.Helper()
	for i := 0; i < 200; i++ {
		if v := s.View(); !v.Running {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a limpeza não terminou")
	return View{}
}

func TestManualCleanup(t *testing.T) {
	s, dk, disk, req, msgs := setup(t)
	if err := s.Start("ana", Request{}); !errors.Is(err, ErrNothing) {
		t.Fatalf("pedido vazio: %v", err)
	}
	if err := s.Start("ana", Request{Logs: []string{idA}}); !errors.Is(err, ErrNoHelper) {
		t.Fatalf("sem o ajudante não limpa log: %v", err)
	}
	stop := make(chan struct{})
	defer close(stop)
	helper(t, req, stop)
	if err := s.Start("ana", Request{Logs: []string{"../../etc"}}); !errors.Is(err, ErrUnknownCt) {
		t.Fatalf("só contêineres da lista: %v", err)
	}
	if err := s.Start("ana", Request{BuildCache: true, Dangling: true, Logs: []string{idA}}); err != nil {
		t.Fatal(err)
	}
	v := wait(t, s)
	if len(v.Runs) != 1 {
		t.Fatalf("registro: %+v", v.Runs)
	}
	r := v.Runs[0]
	if r.By != "ana" || len(r.Steps) != 3 || r.Freed != 3<<30+500<<20+1<<20 || r.Before != 90 || r.After != 60 {
		t.Fatalf("limpeza: %+v", r)
	}
	if r.Steps[2].Item != "logs" || r.Steps[2].Names[0] != "loja-api" {
		t.Fatalf("logs: %+v", r.Steps[2])
	}
	if strings.Join(dk.calls, ",") != "build,dangling" || disk.refreshed != 1 {
		t.Fatalf("Docker: %v, medições: %d", dk.calls, disk.refreshed)
	}
	if len(*msgs) != 1 || !strings.HasPrefix((*msgs)[0], "cleanup|") || !strings.Contains((*msgs)[0], "Feita por ana") ||
		!strings.Contains((*msgs)[0], "Liberado: *3,5 GB*") || !strings.Contains((*msgs)[0], "90% → 60%") {
		t.Fatalf("aviso: %v", *msgs)
	}
	if left, _ := filepath.Glob(filepath.Join(req, "*-*")); len(left) != 0 {
		t.Fatalf("sobrou pedido/resposta na pasta: %v", left)
	}
	if again := New(&fakeDocker{}, disk, filepath.Dir(s.path), req); len(again.View().Runs) != 1 {
		t.Fatal("o histórico sobrevive ao reinício")
	}
}

func TestCleanupErrorIsRecorded(t *testing.T) {
	s, dk, _, _, msgs := setup(t)
	dk.fail = errors.New("proxy recusou")
	if err := s.Start("ana", Request{BuildCache: true}); err != nil {
		t.Fatal(err)
	}
	r := wait(t, s).Runs[0]
	if r.Steps[0].Error == "" || !strings.Contains((*msgs)[0], "falhou (proxy recusou)") {
		t.Fatalf("falha registrada e avisada: %+v %v", r, *msgs)
	}
}

func TestBusy(t *testing.T) {
	s, _, _, _, _ := setup(t)
	s.running = true
	if err := s.Start("ana", Request{BuildCache: true}); !errors.Is(err, ErrBusy) {
		t.Fatalf("uma por vez: %v", err)
	}
}

func TestAutoCleanup(t *testing.T) {
	s, dk, disk, req, msgs := setup(t)
	now := time.Now() // o .alive do ajudante usa o relógio de verdade
	s.now = func() time.Time { return now }
	ctx := context.Background()
	s.Tick(ctx)
	if len(dk.calls) != 0 {
		t.Fatal("desligada por padrão")
	}
	if _, err := s.SaveAuto(Auto{Enabled: true, Threshold: 40, LogsOverMB: 100}); err == nil {
		t.Fatal("limite fora da faixa")
	}
	if _, err := s.SaveAuto(Auto{Enabled: true, Threshold: 85, LogsOverMB: 100}); err == nil {
		t.Fatal("ligada sem nada marcado")
	}
	if _, err := s.SaveAuto(Auto{Enabled: true, Threshold: 85, BuildCache: true, Dangling: true, Logs: true, LogsOverMB: 100}); err != nil {
		t.Fatal(err)
	}
	disk.snap.FSUsed = 80 // abaixo do limite
	s.Tick(ctx)
	if len(dk.calls) != 0 {
		t.Fatal("abaixo do limite não limpa")
	}
	stop := make(chan struct{})
	defer close(stop)
	helper(t, req, stop)
	disk.snap.FSUsed = 90
	s.Tick(ctx)
	r := s.View().Runs[0]
	if r.By != "" || strings.Join(dk.calls, ",") != "build,dangling" || len(r.Steps) != 3 || len(r.Steps[2].Names) != 1 || r.Steps[2].Names[0] != "loja-api" {
		t.Fatalf("automática: só logs acima de 100 MB (%+v, %v)", r, dk.calls)
	}
	if !strings.Contains((*msgs)[0], "Limpeza automática") {
		t.Fatalf("aviso da automática: %v", *msgs)
	}
	disk.snap.FSUsed = 95
	now = now.Add(5 * time.Hour)
	s.Tick(ctx)
	os.Chtimes(filepath.Join(req, ".alive"), now, now)
	if len(s.View().Runs) != 1 {
		t.Fatal("no máximo uma a cada 6 h")
	}
	now = now.Add(2 * time.Hour)
	os.Chtimes(filepath.Join(req, ".alive"), now, now)
	disk.snap.BuildCache, disk.snap.DanglingCount, disk.snap.Logs = 0, 0, nil
	s.Tick(ctx)
	v := s.View()
	if len(v.Runs) != 2 || v.Runs[0].Note == "" || len(dk.calls) != 2 || len(*msgs) != 1 {
		t.Fatalf("nada a limpar: registra sem chamar o Docker nem avisar (%+v %v %v)", v.Runs[0], dk.calls, *msgs)
	}
}

func TestBytes(t *testing.T) {
	for v, want := range map[uint64]string{0: "0 B", 1536: "1,5 KB", 3 << 30: "3,0 GB", 150 << 20: "150 MB"} {
		if got := Bytes(v); got != want {
			t.Errorf("%d: %q (esperava %q)", v, got, want)
		}
	}
}
