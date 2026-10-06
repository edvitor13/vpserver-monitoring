package host

// Sample é o estado do host já em taxas (% e bytes/s), calculado entre duas leituras.
type Sample struct {
	T int64 `json:"t"`

	// CPU em % da máquina inteira (100 = todos os núcleos ocupados).
	CPU       float64   `json:"cpu"`
	CPUUser   float64   `json:"cpuUser"`
	CPUSystem float64   `json:"cpuSystem"`
	CPUIOWait float64   `json:"cpuIowait"`
	CPUSteal  float64   `json:"cpuSteal"`
	PerCPU    []float64 `json:"perCpu"`
	Cores     int       `json:"cores"`

	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
	ProcsRunning int     `json:"procsRunning"`
	Procs        int     `json:"procs"`

	MemTotal     uint64 `json:"memTotal"`
	MemUsed      uint64 `json:"memUsed"` // total - disponível (o que a Oracle chama de uso)
	MemAvailable uint64 `json:"memAvailable"`
	MemFree      uint64 `json:"memFree"` // livre de verdade (sem nem cache)
	MemCache     uint64 `json:"memCache"`
	SwapTotal    uint64 `json:"swapTotal"`
	SwapUsed     uint64 `json:"swapUsed"`

	Iface        string  `json:"iface"`
	NetRx        float64 `json:"netRx"` // bytes/s na placa principal
	NetTx        float64 `json:"netTx"`
	NetRxCounter uint64  `json:"netRxCounter"` // acumulado desde o boot
	NetTxCounter uint64  `json:"netTxCounter"`
	NetErrors    uint64  `json:"netErrors"`

	DiskRead  float64 `json:"diskRead"` // bytes/s
	DiskWrite float64 `json:"diskWrite"`
	DiskIOPS  float64 `json:"diskIops"`
	DiskUtil  float64 `json:"diskUtil"` // % do tempo com E/S em andamento

	FSTotal uint64 `json:"fsTotal"`
	FSUsed  uint64 `json:"fsUsed"`
	FSAvail uint64 `json:"fsAvail"`

	PSI PSI `json:"psi"`

	TCP      int     `json:"tcp"`
	TCPTW    int     `json:"tcpTimeWait"`
	OOMKills uint64  `json:"oomKills"`
	SwapIn   uint64  `json:"swapIn"`
	SwapOut  uint64  `json:"swapOut"`
	Uptime   float64 `json:"uptime"`
}

func pct(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func cpuDelta(a, b CPUTimes) (busy, user, sys, iowait, steal float64) {
	total := sub(b.Total(), a.Total())
	if total == 0 {
		return
	}
	user = pct(sub(b.User+b.Nice, a.User+a.Nice), total)
	sys = pct(sub(b.System+b.IRQ+b.SoftIRQ, a.System+a.IRQ+a.SoftIRQ), total)
	iowait = pct(sub(b.IOWait, a.IOWait), total)
	steal = pct(sub(b.Steal, a.Steal), total)
	busy = user + sys
	return
}

// Compute transforma duas leituras em taxas. iface é a placa de saída para a internet.
func Compute(prev, cur *Raw, iface string) Sample {
	dt := cur.At.Sub(prev.At).Seconds()
	if dt <= 0 {
		dt = 1
	}
	s := Sample{T: cur.At.Unix(), Cores: len(cur.PerCPU)}
	s.CPU, s.CPUUser, s.CPUSystem, s.CPUIOWait, s.CPUSteal = cpuDelta(prev.CPU, cur.CPU)
	for i := range cur.PerCPU {
		if i < len(prev.PerCPU) {
			b, _, _, _, _ := cpuDelta(prev.PerCPU[i], cur.PerCPU[i])
			s.PerCPU = append(s.PerCPU, b)
		}
	}
	s.Load1, s.Load5, s.Load15 = cur.Load1, cur.Load5, cur.Load15
	s.ProcsRunning, s.Procs = cur.ProcsRunning, cur.ProcsTotal

	m := cur.Mem
	s.MemTotal, s.MemAvailable = m["MemTotal"], m["MemAvailable"]
	s.MemUsed = sub(s.MemTotal, s.MemAvailable)
	s.MemCache = m["Buffers"] + m["Cached"] + m["SReclaimable"]
	s.MemFree = m["MemFree"]
	s.SwapTotal = m["SwapTotal"]
	s.SwapUsed = sub(m["SwapTotal"], m["SwapFree"])

	s.Iface = iface
	if n, ok := cur.Net[iface]; ok {
		p := prev.Net[iface]
		s.NetRx = float64(sub(n.RxBytes, p.RxBytes)) / dt
		s.NetTx = float64(sub(n.TxBytes, p.TxBytes)) / dt
		s.NetRxCounter, s.NetTxCounter = n.RxBytes, n.TxBytes
		s.NetErrors = n.RxErrs + n.TxErrs + n.RxDrop + n.TxDrop
	}

	var rd, wr, ios, ticks uint64
	for name, d := range cur.Disk {
		p := prev.Disk[name]
		rd += sub(d.ReadSectors, p.ReadSectors)
		wr += sub(d.WriteSectors, p.WriteSectors)
		ios += sub(d.Reads+d.Writes, p.Reads+p.Writes)
		if t := sub(d.IOTicksMs, p.IOTicksMs); t > ticks {
			ticks = t
		}
	}
	s.DiskRead, s.DiskWrite = float64(rd*512)/dt, float64(wr*512)/dt
	s.DiskIOPS = float64(ios) / dt
	s.DiskUtil = min(100, float64(ticks)/(dt*1000)*100)

	s.FSTotal, s.FSAvail = cur.FSTotal, cur.FSAvail
	s.FSUsed = sub(cur.FSTotal, cur.FSFree)

	s.PSI = cur.PSI
	s.TCP, s.TCPTW = cur.TCPInUse, cur.TCPTimeWait
	s.OOMKills, s.SwapIn, s.SwapOut = cur.OOMKills, cur.SwapIn, cur.SwapOut
	s.Uptime = cur.Uptime
	return s
}
