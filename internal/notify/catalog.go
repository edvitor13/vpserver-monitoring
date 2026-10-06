package notify

import (
	"strings"
	"time"
)

// Kind é um tipo de notificação que a pessoa liga ou desliga na tela.
type Kind struct {
	Key     string `json:"key"`
	Group   string `json:"group"` // alertas | mudancas | resumos | ia
	Label   string `json:"label"`
	Desc    string `json:"desc"`
	Default bool   `json:"default"`
	AI      bool   `json:"ai,omitempty"` // precisa da IA configurada
}

// Catalog é a lista da tela, na ordem em que aparece. Ligado por padrão: o
// que exige ação (queda, recurso no limite, disco, limites grátis), resumos
// diário e mensal e as análises da IA que valem a mensagem.
var Catalog = []Kind{
	{"app_down", "alertas", "Aplicação caiu ou travou", "Contêiner unhealthy, reiniciando sem parar, parado com erro, caindo várias vezes ou morto por falta de memória.", true, false},
	{"resources", "alertas", "Servidor no limite", "CPU acima de 90% por mais de 10 min, memória quase esgotada, swap, disco travando ou o kernel matando processos.", true, false},
	{"disk", "alertas", "Disco enchendo", "Disco acima de 80% ou previsão de encher em menos de 30 dias.", true, false},
	{"limits", "alertas", "Limites do plano grátis", "Saída de dados perto do limite do mês, VM com cara de ociosa pela regra da Oracle ou shape que não é grátis.", true, false},
	{"monitor", "alertas", "Problemas do próprio painel", "O painel perdeu o acesso ao Docker e parou de enxergar as aplicações.", true, false},
	{"app_warn", "alertas", "Avisos menores das aplicações", "Contêiner perto do limite de memória, segurado no limite de CPU ou com muitos erros no log.", false, false},
	{"resolved", "alertas", "Avisar quando resolver", "Uma mensagem curta quando um alerta avisado deixa de valer.", true, false},

	{"apps", "mudancas", "Aplicação nova ou removida", "Quando aparece um projeto novo no Docker do servidor, ou quando um some.", true, false},
	{"deploys", "mudancas", "Aplicação atualizada (deploy)", "Quando os contêineres de uma app voltam com outra imagem.", false, false},
	{"security", "mudancas", "Segurança do painel", "Muitas senhas erradas no login e troca da senha do painel.", true, false},
	{"logins", "mudancas", "Cada entrada no painel", "Uma mensagem a cada login, com o IP.", false, false},
	{"pauses", "mudancas", "App pausada ou retomada pela tela", "Um registro de quem usou o botão Pausar/Retomar (com o IP). Pausar nunca vira alerta.", false, false},

	{"daily", "resumos", "Resumo diário", "Como foi ontem: CPU, memória, disco, banda, apps que mais consumiram e quedas.", true, false},
	{"weekly", "resumos", "Resumo semanal", "Toda segunda: os últimos 7 dias comparados com a semana anterior.", false, false},
	{"monthly", "resumos", "Fechamento do mês", "No dia 1: o mês anterior fechado, com a banda total contra o limite grátis.", true, false},

	{"ai_daily", "ia", "Análise da IA no resumo diário", "A IA olha os dados de ontem e comenta o que chama atenção, com sugestões. Vai junto do resumo diário.", true, true},
	{"ai_incident", "ia", "Diagnóstico de incidentes", "Quando uma app cai ou o servidor aperta, a IA lê logs e eventos e manda a causa provável e o que fazer (até 6 por dia).", true, true},
	{"ai_weekly", "ia", "Relatório semanal de otimização", "Toda segunda: onde dá para economizar recurso, limpezas seguras de disco e riscos para os limites grátis.", true, true},
	{"ai_logs", "ia", "Erros dos logs do dia", "Todo dia: a IA agrupa os erros dos logs de cada app e aponta os que se repetem.", false, true},
}

func kindOf(key string) (Kind, bool) {
	for _, k := range Catalog {
		if k.Key == key {
			return k, true
		}
	}
	return Kind{}, false
}

// DefaultEvents são os tipos ligados por padrão.
func DefaultEvents() map[string]bool {
	out := map[string]bool{}
	for _, k := range Catalog {
		out[k.Key] = k.Default
	}
	return out
}

// category diz a que tipo um alerta do painel pertence ("" = não vira
// notificação) e quanto tempo ele precisa durar para ser avisado (evita
// mensagem por pico de segundos ou por contêiner recriado num deploy).
func category(key, level string) (string, time.Duration) {
	prefix, _, _ := strings.Cut(key, ":")
	switch prefix {
	case "app.unhealthy", "app.restarting", "app.exited", "app.crashes", "app.oom":
		return "app_down", 2 * time.Minute
	case "app.memlimit", "app.cpulimit", "app.logerrors":
		return "app_warn", 5 * time.Minute
	case "host.cpu":
		if level != "crit" { // 75% é só "alta"; avisa a partir de 90%
			return "", 0
		}
		return "resources", 5 * time.Minute
	case "host.mem", "host.swap", "host.psimem", "host.psiio", "host.oomkill":
		return "resources", 3 * time.Minute
	case "host.disk", "host.diskdays":
		return "disk", 2 * time.Minute
	case "limit.egress", "limit.egressproj", "limit.idle", "limit.paid":
		return "limits", 2 * time.Minute
	case "monitor.docker":
		return "monitor", 5 * time.Minute
	}
	return "", 0
}
