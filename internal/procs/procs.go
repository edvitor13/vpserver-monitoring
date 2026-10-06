// Package procs lista os processos do host que mais consomem CPU e memória.
//
// Lê só /proc/<pid>/stat e /proc/<pid>/cgroup (legíveis por qualquer
// usuário); nunca a linha de comando, que pode carregar segredos.
package procs

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const userHZ = 100 // ticks por segundo do /proc (fixo em Linux)

type Proc struct {
	PID     int     `json:"pid"`
	Name    string  `json:"name"`
	State   string  `json:"state"`
	Threads int     `json:"threads"`
	CPU     float64 `json:"cpu"` // % da máquina inteira
	RSS     uint64  `json:"rss"`
	Cgroup  string  `json:"-"` // diretório do cgroup (docker-<id>.scope, ssh.service...)
	Unit    string  `json:"unit"`
	App     string  `json:"app"`
}

type Sampler struct {
	Proc  string
	prev  map[int]uint64
	prevT time.Time
	page  uint64
}

func NewSampler(proc string) *Sampler {
	return &Sampler{Proc: proc, page: uint64(os.Getpagesize())}
}

// Sample lê todos os processos; o % de CPU é relativo à leitura anterior
// (na primeira, fica zerado).
func (s *Sampler) Sample(cores int) []Proc {
	ents, err := os.ReadDir(s.Proc)
	if err != nil {
		return nil
	}
	now := time.Now()
	dt := now.Sub(s.prevT).Seconds()
	cur := make(map[int]uint64, len(ents))
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Proc, e.Name(), "stat"))
		if err != nil {
			continue
		}
		p, ticks, ok := ParseStat(b, s.page)
		if !ok {
			continue
		}
		p.PID = pid
		cur[pid] = ticks
		if old, ok := s.prev[pid]; ok && dt > 0 && ticks >= old && cores > 0 {
			p.CPU = float64(ticks-old) / userHZ / dt / float64(cores) * 100
		}
		if p.RSS == 0 && p.CPU == 0 {
			continue // threads do kernel e processos parados
		}
		if cg, err := os.ReadFile(filepath.Join(s.Proc, e.Name(), "cgroup")); err == nil {
			p.Cgroup = CgroupLeaf(string(cg))
		}
		out = append(out, p)
	}
	s.prev, s.prevT = cur, now
	return out
}

// ParseStat lê /proc/<pid>/stat. O nome vem entre parênteses e pode ter
// espaços, então o resto é cortado a partir do último ')'.
func ParseStat(b []byte, page uint64) (p Proc, ticks uint64, ok bool) {
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return p, 0, false
	}
	p.Name = s[open+1 : close]
	f := strings.Fields(s[close+1:])
	if len(f) < 22 {
		return p, 0, false
	}
	// f[0]=state(3) ... f[11]=utime(14) f[12]=stime(15) f[17]=threads(20) f[21]=rss(24)
	p.State = f[0]
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	p.Threads, _ = strconv.Atoi(f[17])
	rss, _ := strconv.ParseInt(f[21], 10, 64)
	if rss > 0 {
		p.RSS = uint64(rss) * page
	}
	return p, ut + st, true
}

// CgroupLeaf pega a unidade de "0::/system.slice/docker-<id>.scope" →
// "docker-<id>.scope"; para sessões, "user.slice".
func CgroupLeaf(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		_, path, ok := strings.Cut(ln, "::")
		if !ok {
			continue
		}
		// dentro de um cgroup namespace o caminho vem relativo ("/../docker-x.scope")
		var parts []string
		for _, p := range strings.Split(strings.Trim(path, "/"), "/") {
			if p != ".." && p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 {
			return "/"
		}
		if parts[0] == "user.slice" || parts[0] == "init.scope" {
			return parts[0]
		}
		if parts[0] == "system.slice" && len(parts) > 1 {
			return parts[1]
		}
		if parts[0] == "docker" && len(parts) > 1 {
			return "docker-" + parts[1] + ".scope"
		}
		return parts[len(parts)-1]
	}
	return ""
}

// Top devolve os n maiores por CPU e os n maiores por memória (sem repetir).
func Top(ps []Proc, n int) (byCPU, byMem []Proc) {
	c := append([]Proc(nil), ps...)
	sort.Slice(c, func(i, j int) bool { return c[i].CPU > c[j].CPU })
	m := append([]Proc(nil), ps...)
	sort.Slice(m, func(i, j int) bool { return m[i].RSS > m[j].RSS })
	return c[:min(n, len(c))], m[:min(n, len(m))]
}
