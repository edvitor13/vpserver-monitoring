// Package fleet liga vários painéis: um painel central recebe, de cada
// servidor conectado, um resumo a cada minuto (e pode emprestar o WhatsApp
// dele para os avisos desses servidores). Quem chama é sempre o servidor
// conectado, com um token gerado no central: ele não precisa de endereço
// público, e o central nunca entra em outro servidor.
package fleet

import (
	"sort"
	"strings"

	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

// Report é o resumo que um servidor manda ao central (e o card da tela).
type Report struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	PanelURL    string    `json:"panelUrl,omitempty"`
	At          int64     `json:"at"`
	Where       string    `json:"where,omitempty"` // "Oracle São Paulo · A1.Flex 2 OCPU/12 GB"
	OS          string    `json:"os,omitempty"`
	Uptime      int64     `json:"uptime"`
	CPU         float64   `json:"cpu"` // % da máquina
	MemUsed     uint64    `json:"memUsed"`
	MemTotal    uint64    `json:"memTotal"`
	DiskUsed    uint64    `json:"diskUsed"`
	DiskTotal   uint64    `json:"diskTotal"`
	EgressMonth uint64    `json:"egressMonth"`
	EgressLimit float64   `json:"egressLimit"`
	Crit        int       `json:"crit"`
	Warn        int       `json:"warn"`
	Info        int       `json:"info"`
	Top         []string  `json:"top,omitempty"` // títulos dos alertas mais graves
	AppsUp      int       `json:"appsUp"`
	AppsTotal   int       `json:"appsTotal"`
	Apps        []AppLine `json:"apps,omitempty"`
	WhatsApp    string    `json:"whatsapp"`     // own | central | off
	ShareView   bool      `json:"shareView"`    // deixa o central ver este servidor (só leitura)
	ShareLogs   bool      `json:"shareLogs"`    // ... inclusive os logs
	ShareCtl    bool      `json:"shareControl"` // ... e fazer as ações (controle total)
}

func (r *Report) share() Share {
	return Share{View: r.ShareView, Logs: r.ShareLogs, Control: r.ShareCtl}
}

func (r *Report) setShare(sh Share) {
	r.ShareView, r.ShareLogs, r.ShareCtl = sh.View, sh.View && sh.Logs, sh.View && sh.Control
}

type AppLine struct {
	Name   string  `json:"name"`
	Status string  `json:"status"` // ok | warn | crit | stopped | paused
	CPU    float64 `json:"cpu"`
	Mem    uint64  `json:"mem"`
}

const maxApps, maxTop = 12, 3

// FromOverview monta o resumo a partir do estado do monitor.
func FromOverview(o monitor.Overview, panelURL, whatsapp string) Report {
	s, h, c := o.Server, o.Host, o.Server.Cloud
	r := Report{Name: s.Name, Version: o.Version, PanelURL: panelURL, At: o.Updated, OS: s.OS, CPU: h.CPU,
		MemUsed: h.MemUsed, MemTotal: h.MemTotal, DiskUsed: h.FSUsed, DiskTotal: h.FSTotal,
		EgressMonth: o.Traffic.Month.Tx, EgressLimit: o.Traffic.LimitBytes, WhatsApp: whatsapp, Uptime: int64(h.Uptime)}
	if c.Provider == "oracle" {
		r.Where = strings.TrimSpace("Oracle " + c.RegionName + " · " + strings.TrimPrefix(c.Shape, "VM.Standard."))
	}
	for _, a := range o.Alerts {
		switch a.Level {
		case "crit":
			r.Crit++
		case "warn":
			r.Warn++
		default:
			r.Info++
		}
		if a.Level != "info" && len(r.Top) < maxTop {
			r.Top = append(r.Top, a.Title)
		}
	}
	for _, a := range o.Apps {
		if a.Kind != "compose" && a.Kind != "standalone" {
			continue
		}
		r.AppsTotal++
		if a.Running > 0 {
			r.AppsUp++
		}
		r.Apps = append(r.Apps, AppLine{Name: a.Name, Status: a.Status, CPU: a.CPU, Mem: a.Mem})
	}
	sort.SliceStable(r.Apps, func(i, j int) bool { return r.Apps[i].Mem > r.Apps[j].Mem })
	if len(r.Apps) > maxApps {
		r.Apps = r.Apps[:maxApps]
	}
	return r
}

// clean limita o que veio de fora (o central não confia no tamanho do resumo).
func (r *Report) clean() {
	cut := func(s string, n int) string {
		if rs := []rune(s); len(rs) > n {
			return string(rs[:n])
		}
		return s
	}
	r.Name, r.Version, r.Where, r.OS = cut(r.Name, 60), cut(r.Version, 40), cut(r.Where, 80), cut(r.OS, 60)
	if !strings.HasPrefix(r.PanelURL, "https://") && !strings.HasPrefix(r.PanelURL, "http://") {
		r.PanelURL = ""
	}
	r.PanelURL = cut(r.PanelURL, 200)
	if len(r.Top) > maxTop {
		r.Top = r.Top[:maxTop]
	}
	for i := range r.Top {
		r.Top[i] = cut(r.Top[i], 140)
	}
	if len(r.Apps) > maxApps {
		r.Apps = r.Apps[:maxApps]
	}
	for i := range r.Apps {
		r.Apps[i].Name, r.Apps[i].Status = cut(r.Apps[i].Name, 40), cut(r.Apps[i].Status, 10)
	}
	switch r.WhatsApp {
	case "own", "central", "off":
	default:
		r.WhatsApp = "off"
	}
}
