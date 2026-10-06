package monitor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
)

func TestComponentsGroupsInstances(t *testing.T) {
	// app típica: api1 e api2 = mesma imagem e comando; worker e beat = mesma imagem, comandos diferentes
	cs := []docker.Container{
		{Name: "loja-api1-1", Service: "api1", ImageID: "img", Command: "gunicorn", State: "running"},
		{Name: "loja-api2-1", Service: "api2", ImageID: "img", Command: "gunicorn", State: "running"},
		{Name: "loja-worker-1", Service: "worker", ImageID: "img", Command: "celery worker", State: "running"},
		{Name: "loja-beat-1", Service: "beat", ImageID: "img", Command: "celery beat", State: "exited"},
		{Name: "loja-redis-1", Service: "redis", ImageID: "redis", Command: "redis-server", State: "running"},
	}
	got := components(cs)
	if len(got) != 4 || got[0].Name != "api" || got[0].Count != 2 || got[0].Running != 2 || !got[0].API {
		t.Fatalf("componentes: %+v", got)
	}
	for _, c := range got[1:] {
		if c.API {
			t.Fatalf("só a api é API: %+v", c)
		}
	}
	for _, c := range got[1:] {
		if c.Count != 1 {
			t.Fatalf("só a api deveria ter 2 instâncias: %+v", got)
		}
	}
}

func TestReadLogSizes(t *testing.T) {
	id := "1a0158700961f9ea3184bbbbde23ba2253dcd03d7d7f45860644bc03965c1b3b"
	p := filepath.Join(t.TempDir(), "logsizes.txt")
	os.WriteFile(p, []byte("188217853 /logs/"+id+"/"+id+"-json.log\n1000 /logs/"+id+"/"+id+"-json.log.1\nlixo\n"), 0o644)
	m, ok := readLogSizes(p)
	if !ok || m[id] != 188218853 {
		t.Fatalf("tamanhos: %v %v", ok, m)
	}
	if _, ok := readLogSizes(filepath.Join(t.TempDir(), "nao-existe")); ok {
		t.Fatal("arquivo ausente deveria avisar")
	}
}

func TestComputeStorageSplitsSharedImages(t *testing.T) {
	m := &Monitor{containers: map[string]docker.Container{
		"a1": {ID: "a1", Name: "app-db", Project: "app"},
		"b1": {ID: "b1", Name: "outra-db", Project: "outra"},
		"b2": {ID: "b2", Name: "outra-api", Project: "outra"},
	}}
	du := docker.DiskUsage{
		T: 1, ImagesSize: 900, BuildCacheSize: 5000, ImagesUnused: 100,
		Images: []docker.ImageUse{{ID: "pg", Size: 600}, {ID: "api", Size: 400}}, // soma 1000 → escala 0,9
		Containers: []docker.ContainerDisk{
			{ID: "a1", Name: "app-db", ImageID: "pg", SizeRw: 10, Volumes: []string{"app_data"}},
			{ID: "b1", Name: "outra-db", ImageID: "pg", SizeRw: 20, Volumes: []string{"outra_data"}},
			{ID: "b2", Name: "outra-api", ImageID: "api", SizeRw: 30},
		},
		Volumes: []docker.Volume{{Name: "app_data", Size: 100}, {Name: "outra_data", Size: 200}, {Name: "solto", Project: "app", Size: 7}},
	}
	st := m.computeStorage(du, map[string]uint64{"b2": 50}, true, 100000)
	app, outra := st.apps["app"], st.apps["outra"]
	// postgres (540 escalado) dividido entre as duas apps; a api (360) só da "outra"
	if app.Images != 270 || outra.Images != 270+360 {
		t.Fatalf("imagens: app=%d outra=%d", app.Images, outra.Images)
	}
	if app.Volumes != 107 || outra.Volumes != 200 || outra.Logs != 50 || outra.Layer != 50 {
		t.Fatalf("volumes/logs/camada: app=%+v outra=%+v", app, outra)
	}
	if app.Total != 270+10+107 || st.units["outra-api"].Total != 30+50 {
		t.Fatalf("totais: app=%+v unidade=%+v", app, st.units["outra-api"])
	}
	if st.view.BuildCache != 5000 || st.view.Other != 100000-st.view.Apps-5000-100 {
		t.Fatalf("visão: %+v", st.view)
	}
}

func TestMarkAPIFallbacks(t *testing.T) {
	cases := []struct {
		names []string
		api   []bool
	}{
		{[]string{"portal-app", "portal-tunnel", "portal-warp"}, []bool{true, false, false}},
		{[]string{"blog"}, []bool{true}},
		{[]string{"monitor", "dockerproxy", "sizer", "tunnel"}, []bool{true, false, false, false}},
		{[]string{"api", "db"}, []bool{true, false}},
		{[]string{"meu-site", "postgres", "redis"}, []bool{true, false, false}},
		{[]string{"loja", "worker", "postgres"}, []bool{true, false, false}},
	}
	for _, c := range cases {
		cs := make([]Component, len(c.names))
		for i, n := range c.names {
			cs[i] = Component{Name: n, Count: 1}
		}
		markAPI(cs)
		for i := range cs {
			if cs[i].API != c.api[i] {
				t.Errorf("%v: %s api=%v, queria %v", c.names, cs[i].Name, cs[i].API, c.api[i])
			}
		}
	}
}
