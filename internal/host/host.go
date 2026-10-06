// Package host lê as métricas da máquina direto do /proc e do /sys do host.
//
// O contêiner roda com `pid: host`, então /proc é o do host; a rede do host é
// lida em /proc/1/net (o namespace de rede do PID 1), já que /proc/net aponta
// para o namespace do próprio contêiner.
package host

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CPUTimes são os contadores de /proc/stat, em ticks (USER_HZ).
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// Total soma tudo menos guest/guest_nice (que já estão dentro de user/nice).
func (c CPUTimes) Total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

type NetDev struct {
	RxBytes, RxPackets, RxErrs, RxDrop uint64
	TxBytes, TxPackets, TxErrs, TxDrop uint64
}

type DiskStat struct {
	Reads, ReadSectors, Writes, WriteSectors, IOTicksMs uint64
}

// PSI é a "pressão" do kernel (/proc/pressure): % do tempo em que alguma
// tarefa esperou por CPU, memória ou disco.
type PSI struct {
	CPUSome10 float64 `json:"cpu10"`
	CPUSome60 float64 `json:"cpu60"`
	MemSome10 float64 `json:"mem10"`
	MemSome60 float64 `json:"mem60"`
	MemFull10 float64 `json:"memFull10"`
	IOSome10  float64 `json:"io10"`
	IOSome60  float64 `json:"io60"`
	IOFull10  float64 `json:"ioFull10"`
}

// Raw é uma leitura crua (contadores acumulados) num instante.
type Raw struct {
	At           time.Time
	CPU          CPUTimes
	PerCPU       []CPUTimes
	Load1        float64
	Load5        float64
	Load15       float64
	ProcsRunning int
	ProcsTotal   int
	Mem          map[string]uint64 // /proc/meminfo, em bytes
	Net          map[string]NetDev
	Disk         map[string]DiskStat
	PSI          PSI
	TCPInUse     int
	TCPTimeWait  int
	OOMKills     uint64
	SwapIn       uint64
	SwapOut      uint64
	Uptime       float64
	FSTotal      uint64
	FSFree       uint64
	FSAvail      uint64
}

// Reader sabe onde ficam os arquivos do host.
type Reader struct {
	Proc   string // normalmente /proc (com pid: host)
	Sys    string // normalmente /sys
	NetPID string // PID cujo namespace de rede é o do host ("1")
	FSPath string // um caminho no disco raiz, para o statfs (/data)
}

func (r *Reader) proc(p ...string) string { return filepath.Join(append([]string{r.Proc}, p...)...) }

// Read faz uma leitura completa. Erros de arquivos opcionais (PSI, statfs)
// não derrubam a leitura: o campo fica zerado.
func (r *Reader) Read() (*Raw, error) {
	raw := &Raw{At: time.Now()}
	b, err := os.ReadFile(r.proc("stat"))
	if err != nil {
		return nil, err
	}
	raw.CPU, raw.PerCPU, raw.ProcsRunning = ParseStat(b)

	if b, err = os.ReadFile(r.proc("loadavg")); err == nil {
		raw.Load1, raw.Load5, raw.Load15, raw.ProcsTotal = ParseLoadavg(b)
	}
	if b, err = os.ReadFile(r.proc("meminfo")); err == nil {
		raw.Mem = ParseMeminfo(b)
	}
	if b, err = os.ReadFile(r.proc(r.NetPID, "net", "dev")); err == nil {
		raw.Net = ParseNetDev(b)
	}
	if b, err = os.ReadFile(r.proc("diskstats")); err == nil {
		raw.Disk = ParseDiskstats(b, r.Disks())
	}
	raw.PSI = r.readPSI()
	if b, err = os.ReadFile(r.proc(r.NetPID, "net", "sockstat")); err == nil {
		raw.TCPInUse, raw.TCPTimeWait = ParseSockstat(b)
	}
	if b, err = os.ReadFile(r.proc("vmstat")); err == nil {
		v := ParseKV(b)
		raw.OOMKills, raw.SwapIn, raw.SwapOut = v["oom_kill"], v["pswpin"], v["pswpout"]
	}
	if b, err = os.ReadFile(r.proc("uptime")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			raw.Uptime, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if r.FSPath != "" {
		raw.FSTotal, raw.FSFree, raw.FSAvail = statfs(r.FSPath)
	}
	return raw, nil
}

// Disks lista os discos "de verdade" em /sys/block (sem loop, ram, zram...).
func (r *Reader) Disks() map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(filepath.Join(r.Sys, "block"))
	if err != nil {
		return out
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "zram") ||
			strings.HasPrefix(n, "sr") || strings.HasPrefix(n, "nbd") || strings.HasPrefix(n, "dm-") {
			continue
		}
		out[n] = true
	}
	return out
}

// DiskSizes devolve o tamanho de cada disco em bytes (/sys/block/<d>/size, em setores de 512).
func (r *Reader) DiskSizes() map[string]uint64 {
	out := map[string]uint64{}
	for d := range r.Disks() {
		if b, err := os.ReadFile(filepath.Join(r.Sys, "block", d, "size")); err == nil {
			if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil {
				out[d] = n * 512
			}
		}
	}
	return out
}

// DefaultIface acha a interface da rota padrão do host (ex.: enp0s6).
func (r *Reader) DefaultIface() string {
	b, err := os.ReadFile(r.proc(r.NetPID, "net", "route"))
	if err != nil {
		return ""
	}
	return ParseDefaultRoute(b)
}

func (r *Reader) readPSI() PSI {
	var p PSI
	if b, err := os.ReadFile(r.proc("pressure", "cpu")); err == nil {
		s, _ := ParsePSI(b)
		p.CPUSome10, p.CPUSome60 = s[0], s[1]
	}
	if b, err := os.ReadFile(r.proc("pressure", "memory")); err == nil {
		s, f := ParsePSI(b)
		p.MemSome10, p.MemSome60, p.MemFull10 = s[0], s[1], f[0]
	}
	if b, err := os.ReadFile(r.proc("pressure", "io")); err == nil {
		s, f := ParsePSI(b)
		p.IOSome10, p.IOSome60, p.IOFull10 = s[0], s[1], f[0]
	}
	return p
}

// --- parsers (puros, testados com amostras reais do servidor) ---------------

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func cpuFrom(f []string) CPUTimes {
	g := func(i int) uint64 {
		if i < len(f) {
			return atou(f[i])
		}
		return 0
	}
	return CPUTimes{g(1), g(2), g(3), g(4), g(5), g(6), g(7), g(8)}
}

func ParseStat(b []byte) (total CPUTimes, per []CPUTimes, running int) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu":
			total = cpuFrom(f)
		case strings.HasPrefix(f[0], "cpu"):
			per = append(per, cpuFrom(f))
		case f[0] == "procs_running" && len(f) > 1:
			running, _ = strconv.Atoi(f[1])
		}
	}
	return
}

func ParseLoadavg(b []byte) (l1, l5, l15 float64, procs int) {
	f := strings.Fields(string(b))
	if len(f) < 4 {
		return
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	if i := strings.IndexByte(f[3], '/'); i > 0 {
		procs, _ = strconv.Atoi(f[3][i+1:])
	}
	return
}

// ParseMeminfo devolve os valores em bytes.
func ParseMeminfo(b []byte) map[string]uint64 {
	out := map[string]uint64{}
	for _, ln := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n := atou(f[0])
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

func ParseNetDev(b []byte) map[string]NetDev {
	out := map[string]NetDev{}
	for _, ln := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		out[strings.TrimSpace(name)] = NetDev{
			RxBytes: atou(f[0]), RxPackets: atou(f[1]), RxErrs: atou(f[2]), RxDrop: atou(f[3]),
			TxBytes: atou(f[8]), TxPackets: atou(f[9]), TxErrs: atou(f[10]), TxDrop: atou(f[11]),
		}
	}
	return out
}

// ParseDiskstats lê só os discos pedidos (as partições ficam de fora para não contar em dobro).
func ParseDiskstats(b []byte, disks map[string]bool) map[string]DiskStat {
	out := map[string]DiskStat{}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 14 || !disks[f[2]] {
			continue
		}
		out[f[2]] = DiskStat{
			Reads: atou(f[3]), ReadSectors: atou(f[5]),
			Writes: atou(f[7]), WriteSectors: atou(f[9]),
			IOTicksMs: atou(f[12]),
		}
	}
	return out
}

// ParsePSI devolve [avg10, avg60, avg300] das linhas "some" e "full".
func ParsePSI(b []byte) (some, full [3]float64) {
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 {
			continue
		}
		var dst *[3]float64
		switch f[0] {
		case "some":
			dst = &some
		case "full":
			dst = &full
		default:
			continue
		}
		for i, kv := range f[1:4] {
			_, v, _ := strings.Cut(kv, "=")
			dst[i], _ = strconv.ParseFloat(v, 64)
		}
	}
	return
}

func ParseSockstat(b []byte) (inuse, tw int) {
	for _, ln := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(ln, "TCP:") {
			continue
		}
		f := strings.Fields(ln)
		for i := 1; i+1 < len(f); i += 2 {
			n, _ := strconv.Atoi(f[i+1])
			switch f[i] {
			case "inuse":
				inuse = n
			case "tw":
				tw = n
			}
		}
	}
	return
}

// ParseKV lê arquivos "chave valor" (vmstat, memory.stat, cpu.stat).
func ParseKV(b []byte) map[string]uint64 {
	out := map[string]uint64{}
	for _, ln := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(ln, " ")
		if ok {
			out[k] = atou(strings.TrimSpace(v))
		}
	}
	return out
}

func ParseDefaultRoute(b []byte) string {
	for _, ln := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(ln)
		if len(f) > 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}
