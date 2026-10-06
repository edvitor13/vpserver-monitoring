package cgroups

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCPUMax(t *testing.T) {
	if q := ParseCPUMax([]byte("50000 100000\n")); q != 0.5 {
		t.Fatalf("cpu.max = %v", q)
	}
	if q := ParseCPUMax([]byte("max 100000\n")); q != 0 {
		t.Fatalf("sem limite deveria ser 0, veio %v", q)
	}
}

func TestParseIOStat(t *testing.T) {
	rd, wr := ParseIOStat([]byte("8:0 rbytes=4096 wbytes=8192 rios=1 wios=2 dbytes=0 dios=0\n259:0 rbytes=10 wbytes=0 rios=1 wios=0\n"))
	if rd != 4106 || wr != 8192 {
		t.Fatalf("io.stat: %d %d", rd, wr)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Monta uma árvore cgroup falsa (valores de um contêiner real) e confere a leitura.
func TestReadTree(t *testing.T) {
	root, proc := t.TempDir(), t.TempDir()
	id := "5b67884cda4f8aea693a3eccd3ccad16aee4b9e4bab7b78e3e4b74279b25f5e5"
	ct := filepath.Join(root, "system.slice", "docker-"+id+".scope")
	svc := filepath.Join(root, "system.slice", "docker.service")
	write(t, filepath.Join(ct, "cpu.stat"), "usage_usec 1664803507\nnr_periods 45637\nnr_throttled 25523\nthrottled_usec 1215456241\n")
	write(t, filepath.Join(ct, "cpu.max"), "50000 100000\n")
	write(t, filepath.Join(ct, "memory.current"), "28971008\n")
	write(t, filepath.Join(ct, "memory.stat"), "anon 27820032\nfile 1000000\nshmem 400000\ninactive_file 4096\n")
	write(t, filepath.Join(ct, "memory.max"), "268435456\n")
	write(t, filepath.Join(ct, "cgroup.procs"), "4242\n")
	write(t, filepath.Join(proc, "4242", "net", "dev"), "h\nh\n    lo: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\n  eth0: 2342380   25237    0    0    0     0          0         0 23058244   24641    0    0    0     0       0          0\n")
	write(t, filepath.Join(svc, "cpu.stat"), "usage_usec 99\n")
	write(t, filepath.Join(svc, "memory.current"), "1000\n")
	write(t, filepath.Join(svc, "memory.max"), "max\n")

	units := (&Reader{Root: root, Proc: proc, HostIface: "enp0s6"}).Read()
	var c, s *Unit
	for i := range units {
		switch units[i].Kind {
		case KindContainer:
			c = &units[i]
		case KindService:
			s = &units[i]
		}
	}
	if c == nil || s == nil {
		t.Fatalf("faltou unidade: %+v", units)
	}
	if c.ContainerID != id || c.CPUQuota != 0.5 || c.MemMax != 268435456 || c.MemUsed() != 27820032+400000 {
		t.Fatalf("contêiner errado: %+v", c)
	}
	if !c.HasNet || c.NetRx != 2342380 || c.NetTx != 23058244 {
		t.Fatalf("rede do contêiner errada: %+v", c)
	}
	if s.MemMax != 0 || s.HasNet || s.CPUUsec != 99 {
		t.Fatalf("serviço errado: %+v", s)
	}
}

// Contêiner com network_mode: host não pode somar a rede do host como se fosse dele.
func TestHostNetworkContainerSkipsNet(t *testing.T) {
	root, proc := t.TempDir(), t.TempDir()
	ct := filepath.Join(root, "system.slice", "docker-abc.scope")
	write(t, filepath.Join(ct, "cgroup.procs"), "7\n")
	write(t, filepath.Join(proc, "7", "net", "dev"), "h\nh\nenp0s6: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n")
	units := (&Reader{Root: root, Proc: proc, HostIface: "enp0s6"}).Read()
	if len(units) != 1 || units[0].HasNet {
		t.Fatalf("esperava contêiner sem rede própria: %+v", units)
	}
}
