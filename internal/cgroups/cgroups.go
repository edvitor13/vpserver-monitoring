// Package cgroups lê o consumo de cada contêiner e de cada serviço do host
// direto da árvore cgroup v2 (montada só para leitura em /host/cgroup).
//
// É bem mais leve que a API de stats do Docker: um punhado de arquivos
// pequenos por unidade, sem abrir stream nenhum.
package cgroups

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edvitor13/vpserver-monitoring/internal/host"
)

const (
	KindContainer = "container"
	KindService   = "service"
	KindSlice     = "slice"
)

// Unit são os contadores acumulados de uma unidade (contêiner, serviço ou slice).
type Unit struct {
	Key         string // nome do diretório: docker-<id>.scope, docker.service, user.slice
	Kind        string
	ContainerID string

	CPUUsec       uint64
	ThrottledUsec uint64
	NrPeriods     uint64
	NrThrottled   uint64
	CPUQuota      float64 // limite em núcleos (0 = sem limite)

	MemCurrent uint64
	MemFile    uint64 // cache de arquivos (inclui shmem)
	MemShmem   uint64 // memória compartilhada (ex.: shared_buffers do Postgres)
	MemAnon    uint64
	MemMax     uint64 // 0 = sem limite

	IORead  uint64
	IOWrite uint64
	Pids    uint64

	HasNet bool // false para serviços e contêineres com network_mode: host
	NetRx  uint64
	NetTx  uint64
}

// MemUsed é a memória dos processos: anônima + compartilhada (ex.: o
// shared_buffers do Postgres). Fica de fora o que o kernel devolve quando
// precisa (cache de arquivos e slab recuperável) — o `docker stats` conta
// parte disso e mostra o containerd com 500 MB que na verdade são cache.
// A memória do kernel fica no "Kernel e outros" do painel.
func (u *Unit) MemUsed() uint64 {
	if u.MemAnon == 0 && u.MemShmem == 0 && u.MemFile == 0 {
		return u.MemCurrent // sem memory.stat: o melhor que dá
	}
	return u.MemAnon + u.MemShmem
}

type Reader struct {
	Root      string // /host/cgroup
	Proc      string // /proc (do host)
	HostIface string // placa do host: se aparecer na rede do contêiner, ele usa a rede do host
}

// Read lê todas as unidades de system.slice, mais user.slice e init.scope.
func (r *Reader) Read() []Unit {
	var out []Unit
	sys := filepath.Join(r.Root, "system.slice")
	if ents, err := os.ReadDir(sys); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			switch {
			case strings.HasPrefix(n, "docker-") && strings.HasSuffix(n, ".scope"):
				u := r.unit(filepath.Join(sys, n), n, KindContainer)
				u.ContainerID = strings.TrimSuffix(strings.TrimPrefix(n, "docker-"), ".scope")
				r.readNet(filepath.Join(sys, n), &u)
				out = append(out, u)
			case strings.HasSuffix(n, ".service"):
				out = append(out, r.unit(filepath.Join(sys, n), n, KindService))
			}
		}
	}
	// Driver cgroupfs (sem systemd): /docker/<id>
	if ents, err := os.ReadDir(filepath.Join(r.Root, "docker")); err == nil {
		for _, e := range ents {
			if e.IsDir() && len(e.Name()) == 64 {
				p := filepath.Join(r.Root, "docker", e.Name())
				u := r.unit(p, "docker-"+e.Name()+".scope", KindContainer)
				u.ContainerID = e.Name()
				r.readNet(p, &u)
				out = append(out, u)
			}
		}
	}
	for _, n := range []string{"user.slice", "init.scope"} {
		if _, err := os.Stat(filepath.Join(r.Root, n)); err == nil {
			out = append(out, r.unit(filepath.Join(r.Root, n), n, KindSlice))
		}
	}
	return out
}

func read(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

func (r *Reader) unit(dir, key, kind string) Unit {
	u := Unit{Key: key, Kind: kind}
	cpu := host.ParseKV(read(filepath.Join(dir, "cpu.stat")))
	u.CPUUsec, u.ThrottledUsec = cpu["usage_usec"], cpu["throttled_usec"]
	u.NrPeriods, u.NrThrottled = cpu["nr_periods"], cpu["nr_throttled"]
	u.CPUQuota = ParseCPUMax(read(filepath.Join(dir, "cpu.max")))

	u.MemCurrent = parseUint(read(filepath.Join(dir, "memory.current")))
	ms := host.ParseKV(read(filepath.Join(dir, "memory.stat")))
	u.MemFile, u.MemShmem, u.MemAnon = ms["file"], ms["shmem"], ms["anon"]
	u.MemMax = parseUint(read(filepath.Join(dir, "memory.max"))) // "max" vira 0

	u.IORead, u.IOWrite = ParseIOStat(read(filepath.Join(dir, "io.stat")))
	u.Pids = parseUint(read(filepath.Join(dir, "pids.current")))
	return u
}

// readNet lê a rede do contêiner pelo /proc/<pid>/net/dev de um processo dele.
func (r *Reader) readNet(dir string, u *Unit) {
	pid := firstPID(dir)
	if pid == "" {
		return
	}
	devs := host.ParseNetDev(read(filepath.Join(r.Proc, pid, "net", "dev")))
	if len(devs) == 0 {
		return
	}
	if _, ok := devs[r.HostIface]; ok && r.HostIface != "" {
		return // network_mode: host — a rede já está no total do host
	}
	u.HasNet = true
	for name, d := range devs {
		if name == "lo" {
			continue
		}
		u.NetRx += d.RxBytes
		u.NetTx += d.TxBytes
	}
}

// firstPID acha um processo da unidade (procura nos sub-cgroups se a raiz estiver vazia).
func firstPID(dir string) string {
	if f := strings.Fields(string(read(filepath.Join(dir, "cgroup.procs")))); len(f) > 0 && f[0] != "0" {
		return f[0]
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if e.IsDir() {
			if p := firstPID(filepath.Join(dir, e.Name())); p != "" {
				return p
			}
		}
	}
	return ""
}

func parseUint(b []byte) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// ParseCPUMax lê "50000 100000" (= 0,5 núcleo) ou "max 100000" (sem limite).
func ParseCPUMax(b []byte) float64 {
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] == "max" {
		return 0
	}
	q, _ := strconv.ParseFloat(f[0], 64)
	p, _ := strconv.ParseFloat(f[1], 64)
	if p == 0 {
		return 0
	}
	return q / p
}

// ParseIOStat soma rbytes/wbytes de todos os dispositivos.
func ParseIOStat(b []byte) (rd, wr uint64) {
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			n, _ := strconv.ParseUint(v, 10, 64)
			switch k {
			case "rbytes":
				rd += n
			case "wbytes":
				wr += n
			}
		}
	}
	return
}
