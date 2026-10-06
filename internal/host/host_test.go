package host

import (
	"math"
	"testing"
	"time"
)

// Amostras reais do servidor (Oracle ARM, 2 vCPUs).
const statSample = `cpu  23171870 31396 4822488 560736668 90471 0 305908 747015 0 0
cpu0 10912066 16118 2410929 281019112 47527 0 168392 322427 0 0
cpu1 12259804 15278 2411559 279717556 42943 0 137515 424587 0 0
intr 1961359575 0 108365133
procs_running 3
`

const netdevSample = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 889475398 5941932    0    0    0     0          0         0 889475398 5941932    0    0    0     0       0          0
enp0s6: 13517752964 29706433    0    0    0     0          0         0 17672990946 29937214    0    0    0     0       0          0
`

func TestParseStat(t *testing.T) {
	total, per, running := ParseStat([]byte(statSample))
	if total.User != 23171870 || total.Steal != 747015 || total.Idle != 560736668 {
		t.Fatalf("total errado: %+v", total)
	}
	if len(per) != 2 || per[1].User != 12259804 {
		t.Fatalf("por núcleo errado: %+v", per)
	}
	if running != 3 {
		t.Fatalf("procs_running = %d", running)
	}
}

func TestParseNetDev(t *testing.T) {
	d := ParseNetDev([]byte(netdevSample))
	if d["enp0s6"].RxBytes != 13517752964 || d["enp0s6"].TxBytes != 17672990946 {
		t.Fatalf("enp0s6 errado: %+v", d["enp0s6"])
	}
	if _, ok := d["lo"]; !ok {
		t.Fatal("lo sumiu")
	}
}

func TestParseMeminfoKB(t *testing.T) {
	m := ParseMeminfo([]byte("MemTotal:       12213568 kB\nMemAvailable:    9647456 kB\nHugePages_Total:       0\n"))
	if m["MemTotal"] != 12213568*1024 || m["HugePages_Total"] != 0 {
		t.Fatalf("meminfo errado: %v", m)
	}
}

func TestParseDiskstatsOnlyWholeDisks(t *testing.T) {
	b := []byte(`   8       0 sda 140491 33841 10226032 184861 4318689 4135188 372754958 19055201 0 1995024 19391288 88967 47076 1347401821 151225 0 0
   8       1 sda1 138575 33250 9635472 182008 4318365 4134996 372388962 19039669 0 4463182 19372814 88913 47032 1344608504 151135 0 0
   7       0 loop0 10 0 20 1 0 0 0 0 0 4 1 0 0 0 0 0 0`)
	d := ParseDiskstats(b, map[string]bool{"sda": true})
	if len(d) != 1 || d["sda"].ReadSectors != 10226032 || d["sda"].WriteSectors != 372754958 || d["sda"].IOTicksMs != 1995024 {
		t.Fatalf("diskstats errado: %+v", d)
	}
}

func TestParsePSI(t *testing.T) {
	some, full := ParsePSI([]byte("some avg10=1.02 avg60=1.30 avg300=1.53 total=34650432695\nfull avg10=0.50 avg60=0.00 avg300=0.00 total=0\n"))
	if some != [3]float64{1.02, 1.30, 1.53} || full[0] != 0.5 {
		t.Fatalf("psi errado: %v %v", some, full)
	}
}

func TestParseSockstatAndRoute(t *testing.T) {
	in, tw := ParseSockstat([]byte("sockets: used 312\nTCP: inuse 9 orphan 0 tw 4 alloc 112 mem 37\nUDP: inuse 5 mem 0\n"))
	if in != 9 || tw != 4 {
		t.Fatalf("sockstat: %d %d", in, tw)
	}
	route := "Iface\tDestination\tGateway \tFlags\nenp0s6\t00000000\t0101000A\t0003\nenp0s6\t0001000A\t00000000\t0001\n"
	if got := ParseDefaultRoute([]byte(route)); got != "enp0s6" {
		t.Fatalf("rota padrão = %q", got)
	}
}

func TestComputeRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	a := &Raw{At: t0, CPU: CPUTimes{User: 100, Idle: 900}, PerCPU: []CPUTimes{{}, {}},
		Mem: map[string]uint64{"MemTotal": 1000, "MemAvailable": 600},
		Net: map[string]NetDev{"eth0": {RxBytes: 1000, TxBytes: 5000}}}
	b := &Raw{At: t0.Add(5 * time.Second), CPU: CPUTimes{User: 150, System: 20, Idle: 1810, Steal: 20}, PerCPU: []CPUTimes{{}, {}},
		Mem: map[string]uint64{"MemTotal": 1000, "MemAvailable": 600},
		Net: map[string]NetDev{"eth0": {RxBytes: 2000, TxBytes: 10000}}}
	s := Compute(a, b, "eth0")
	// delta total = 50+20+910+20 = 1000 → usuário 5%, sistema 2%, steal 2%
	if math.Abs(s.CPUUser-5) > 1e-9 || math.Abs(s.CPUSystem-2) > 1e-9 || math.Abs(s.CPUSteal-2) > 1e-9 || math.Abs(s.CPU-7) > 1e-9 {
		t.Fatalf("cpu errada: %+v", s)
	}
	if s.NetRx != 200 || s.NetTx != 1000 || s.MemUsed != 400 {
		t.Fatalf("rede/memória errada: rx=%v tx=%v mem=%v", s.NetRx, s.NetTx, s.MemUsed)
	}
}
