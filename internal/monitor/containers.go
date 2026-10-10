package monitor

import (
	"sort"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
)

// ContainerInfo é um contêiner com o nome amigável da app (para os backups).
type ContainerInfo struct {
	docker.Container
	AppKey  string
	AppName string
}

// Containers devolve os contêineres conhecidos agora (cópia), por nome.
func (m *Monitor) Containers() []ContainerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ContainerInfo, 0, len(m.containers))
	for _, c := range m.containers {
		k := m.appKey(c)
		out = append(out, ContainerInfo{Container: c, AppKey: k, AppName: m.appName(k)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
