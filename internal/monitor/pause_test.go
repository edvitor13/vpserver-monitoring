package monitor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/store"
)

func TestSetPaused(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			calls = append(calls, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`[]`)) // lista de contêineres depois da ação
	}))
	defer srv.Close()

	ctr := func(id, name, project, state string) *docker.Container {
		return &docker.Container{ID: id, Name: name, Project: project, State: state}
	}
	m := &Monitor{cfg: Config{SelfProject: "vpserver-monitoring"}, dc: docker.New(srv.URL), containers: map[string]docker.Container{},
		apps: []AppView{
			{Key: "vpserver-monitoring", Kind: "compose", Self: true, Units: []UnitView{{Container: ctr("m1", "vpserver-monitor", "vpserver-monitoring", "running")}}},
			{Key: "loja", Kind: "compose", Units: []UnitView{
				{Container: ctr("a1", "loja-api", "loja", "running")},
				{Container: ctr("a2", "loja-db", "loja", "running")},
				{Container: ctr("a3", "loja-job", "loja", "exited")},
			}},
			{Key: "_system", Kind: "system"},
		}}
	ctx := context.Background()

	if _, err := m.SetPaused(ctx, "vpserver-monitoring", true); !errors.Is(err, ErrSelfPause) {
		t.Fatalf("o próprio monitor não pode ser pausado: %v", err)
	}
	if _, err := m.SetPaused(ctx, "_system", true); !errors.Is(err, ErrNoApp) {
		t.Fatalf("serviços do host não são app pausável: %v", err)
	}
	names, err := m.SetPaused(ctx, "loja", true)
	if err != nil || strings.Join(names, ",") != "loja-api,loja-db" {
		t.Fatalf("pausa só os que estão rodando: %v %v", names, err)
	}
	mu.Lock()
	got := strings.Join(calls, " ")
	mu.Unlock()
	if got != "/containers/a1/pause /containers/a2/pause" {
		t.Fatalf("chamadas ao Docker: %s", got)
	}
}

// Pausar é escolha, não problema: o Docker marca o pausado como "unhealthy"
// (e o recém-retomado também, até a próxima checagem), e isso não pode virar alerta.
func TestPausedIsNotAProblem(t *testing.T) {
	now := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
		 {"Id":"p1","Names":["/loja-api"],"State":"paused","Status":"Up 2 hours (Paused)","Labels":{"com.docker.compose.project":"loja"}},
		 {"Id":"r1","Names":["/loja-worker"],"State":"running","Status":"Up 2 hours (unhealthy)","Labels":{"com.docker.compose.project":"loja"}},
		 {"Id":"x1","Names":["/blog-api"],"State":"running","Status":"Up 2 hours (unhealthy)","Labels":{"com.docker.compose.project":"blog"}}]`))
	}))
	defer srv.Close()
	m := &Monitor{cfg: Config{Loc: time.UTC, Limits: Limits{EgressTB: 10}}, dc: docker.New(srv.URL), st: store.NewState(now.Add(-time.Hour).Unix()),
		containers: map[string]docker.Container{}, resumed: map[string]int64{}, units: map[string]*UnitView{}}
	m.st.Host = store.NewSeries(hostFields, hostTiers)
	m.st.Events = []docker.Event{{T: now.Add(-time.Minute).Unix(), Action: "unpause", Container: "loja-worker"}} // retomado há 1 min (docker unpause)
	m.refreshContainers(context.Background())

	health := map[string]string{}
	for _, c := range m.containers {
		health[c.Name] = c.Health
	}
	if health["loja-api"] != "" || health["loja-worker"] != "starting" || health["blog-api"] != "unhealthy" {
		t.Fatalf("pausado sem saúde, recém-retomado 'starting', o resto como veio: %v", health)
	}

	// mesmo que chegue "unhealthy" num pausado, e perto do limite de memória, nada de alerta
	c := docker.Container{Name: "loja-api", State: "paused", Health: "unhealthy"}
	m.dockerOK = true
	m.apps = []AppView{{Key: "loja", Kind: "compose", Paused: 1, Total: 1, Units: []UnitView{{Key: "c:loja-api", Container: &c, Mem: 95, MemLimit: 100, LogErrors: 50}}}}
	for _, a := range m.Overview().Alerts {
		if strings.HasPrefix(a.Key, "app.") || strings.Contains(a.Title, "loja-api") {
			t.Fatalf("pausado virou alerta: %+v", a)
		}
	}
}
