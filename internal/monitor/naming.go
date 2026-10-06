package monitor

import (
	"strings"
	"unicode"
)

// Nomes amigáveis para os serviços do host. Serviço que não estiver aqui
// aparece com o nome do systemd mesmo — nada quebra quando surgir um novo.
var serviceNames = []struct {
	match string // prefixo do nome da unidade
	name  string
	desc  string
}{
	{"docker.service", "Docker (motor)", "Executa os contêineres. Picos aqui costumam ser build de imagem durante um deploy."},
	{"containerd.service", "containerd", "Camada de baixo do Docker que roda os processos dos contêineres."},
	{"actions.runner.", "Runner do GitHub", "Executa os deploys automáticos (GitHub Actions) deste repositório."},
	{"unified-monitoring-agent.service", "Oracle · agente de logs", "Agente da Oracle (fluentd) que envia logs/métricas para o console da OCI."},
	{"snap.oracle-cloud-agent.oracle-cloud-agent-updater", "Oracle Cloud Agent (atualizador)", "Mantém o agente da Oracle atualizado."},
	{"snap.oracle-cloud-agent.oracle-cloud-agent", "Oracle Cloud Agent", "Agente da Oracle: métricas do console, comandos remotos, plugins."},
	{"ssh.service", "SSH", "Servidor SSH (as sessões abertas aparecem em \"Sessões de usuário\")."},
	{"systemd-journald.service", "journald", "Guarda os logs do sistema."},
	{"rsyslog.service", "rsyslog", "Grava os logs do sistema em /var/log."},
	{"snapd.service", "snapd", "Gerenciador de pacotes snap."},
	{"multipathd.service", "multipathd", "Caminhos do disco de bloco (iSCSI) da Oracle."},
	{"iscsid.service", "iscsid", "Conexão iSCSI do disco de bloco da Oracle."},
	{"unattended-upgrades.service", "Atualizações automáticas", "Instala atualizações de segurança do Ubuntu."},
	{"cron.service", "cron", "Tarefas agendadas."},
	{"user.slice", "Sessões de usuário", "Logins SSH e comandos rodados à mão (inclui deploys manuais por script)."},
	{"init.scope", "systemd (PID 1)", "O processo inicial do sistema."},
}

// ServiceName devolve nome e descrição de uma unidade do systemd.
func ServiceName(unit string) (name, desc string) {
	for _, s := range serviceNames {
		if strings.HasPrefix(unit, s.match) {
			name, desc = s.name, s.desc
			if s.match == "actions.runner." {
				// actions.runner.<dono>-<repo>.<nome>.service → "Runner do GitHub · <repo>"
				rest := strings.TrimPrefix(unit, s.match)
				repo, _, _ := strings.Cut(rest, ".")
				if _, r, ok := strings.Cut(repo, "-"); ok {
					repo = r
				}
				name += " · " + repo
			}
			return
		}
	}
	return strings.TrimSuffix(unit, ".service"), ""
}

// PrettyName transforma "meu-app_api" em "Meu App Api".
func PrettyName(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	if len(words) == 0 {
		return s
	}
	return strings.Join(words, " ")
}

// ParseAppNames lê "loja=Loja,api-x=API X".
func ParseAppNames(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
