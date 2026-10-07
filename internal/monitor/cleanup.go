package monitor

import (
	"context"
	"sort"
)

// O que a tela de limpeza precisa saber do disco: o que dá para liberar com
// segurança (cache de build sem uso, imagens sem nome, logs do Docker).

// LogFile é o log do Docker de um contêiner.
type LogFile struct {
	ID      string `json:"id"` // ID completo do contêiner (64 hex): é o nome da pasta do log
	Name    string `json:"name"`
	App     string `json:"app"`
	AppName string `json:"appName"`
	Size    uint64 `json:"size"` // atual + rotacionados
}

type DiskSnapshot struct {
	Measured      int64     `json:"measured"` // quando o Docker mediu (unix); 0 = ainda não
	FSUsed        uint64    `json:"fsUsed"`
	FSTotal       uint64    `json:"fsTotal"`
	BuildCache    uint64    `json:"buildCache"` // cache de build fora de uso
	DanglingSize  uint64    `json:"danglingSize"`
	DanglingCount int       `json:"danglingCount"`
	LogsKnown     bool      `json:"logsKnown"` // o vpserver-sizer está medindo os logs
	Logs          []LogFile `json:"logs"`      // maiores primeiro
}

func (m *Monitor) DiskSnapshot() DiskSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := DiskSnapshot{Measured: m.df.T, FSUsed: m.hostNow.FSUsed, FSTotal: m.hostNow.FSTotal,
		BuildCache: uint64(max(0, m.df.BuildCacheUnused)), DanglingSize: uint64(max(0, m.df.DanglingSize)),
		DanglingCount: m.df.DanglingCount, LogsKnown: m.logsOK, Logs: []LogFile{}}
	for id, size := range m.logSizes {
		c, ok := m.containers[id]
		if !ok || size == 0 {
			continue // contêiner que não existe mais: o Docker apaga o log junto
		}
		key := m.appKey(c)
		d.Logs = append(d.Logs, LogFile{ID: id, Name: c.Name, App: key, AppName: m.appName(key), Size: size})
	}
	sort.Slice(d.Logs, func(i, j int) bool {
		if d.Logs[i].Size != d.Logs[j].Size {
			return d.Logs[i].Size > d.Logs[j].Size
		}
		return d.Logs[i].Name < d.Logs[j].Name
	})
	return d
}

// RefreshDisk mede o disco de novo (depois de uma limpeza), sem esperar a volta normal.
func (m *Monitor) RefreshDisk(ctx context.Context) { m.refreshDiskUsage(ctx) }
