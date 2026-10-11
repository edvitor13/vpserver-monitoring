package monitor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/edvitor13/vpserver-monitoring/internal/i18n"
)

func TestRedact(t *testing.T) {
	// linha no formato do log do Caddy (token de login na URL do websocket); token e IP inventados
	in := `"uri": "/ws/notifications/?token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJleGVtcGxvIiwiZXhwIjoxfQ.ZmFrZS1zaWduYXR1cmUtZm9yLXRlc3RzLW9ubHk" 403 ` +
		`"client_ip": "203.0.113.57" Authorization: Bearer abcdef1234567890xyz password=hunter2hunter "email": "fulano@exemplo.com.br"`
	out := Redact(in)
	for _, leak := range []string{"eyJhbGci", "113.57", "abcdef1234567890xyz", "hunter2hunter", "fulano@"} {
		if strings.Contains(out, leak) {
			t.Fatalf("vazou %q em: %s", leak, out)
		}
	}
	for _, keep := range []string{"/ws/notifications/", "403", "203.0.113.x", "password="} {
		if !strings.Contains(out, keep) {
			t.Fatalf("sumiu %q (deveria ficar) em: %s", keep, out)
		}
	}
	// saída de comando (chat de SSH): .env, compose, export, chave privada
	env := `DB_HOST=db
DB_PASSWORD=SuperSecreta123
AWS_SECRET_ACCESS_KEY: wJalrXUtnFEMIK7MDENG
export GITHUB_TOKEN="ghp_abc123"
POSTGRES_PASS=s3nh4
Redis_Pwd: x1y2
tests passed: 42
-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAA
AAAA
-----END OPENSSH PRIVATE KEY-----
fim`
	out = Redact(env)
	for _, leak := range []string{"SuperSecreta123", "wJalrXUtnFEMIK7MDENG", "ghp_abc123", "s3nh4", "x1y2", "b3BlbnNzaC1rZXkt"} {
		if strings.Contains(out, leak) {
			t.Fatalf("vazou %q em: %s", leak, out)
		}
	}
	for _, keep := range []string{"DB_HOST=db", "DB_PASSWORD=[oculto]", "tests passed: 42", "[chave privada]\nfim"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("sumiu %q em: %s", keep, out)
		}
	}
	// linha comum de acesso não muda (fora o fim do IP)
	if got := Redact(`172.19.0.3:34146 - "GET /health/live/ HTTP/1.1" 200`); got != `172.19.0.x:34146 - "GET /health/live/ HTTP/1.1" 200` {
		t.Fatalf("linha comum mudou: %s", got)
	}
}

func TestFmtField(t *testing.T) {
	cases := map[string]string{
		fmtField("cpu", 3.456):       "3,5%",
		fmtField("cpu", 42):          "42%",
		fmtField("tx", 1500):         "1,5 KB/s",
		fmtField("mem", 2.5*(1<<30)): "2,5 GB",
		fmtField("load1", 0.215):     "0,21",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("formatou %q, queria %q", got, want)
		}
	}
}

// Rótulos das consultas da IA (o que o chat mostra enquanto ela trabalha): o
// português sai como antes e, traduzido na saída (i18n.Tr), o inglês sai inteiro.
func TestAILabelsInEnglish(t *testing.T) {
	m := &Monitor{}
	for _, c := range []struct{ tool, args, pt, en string }{
		{"historico_servidor", `{"campos":["cpu","mem"],"periodo":"24h"}`,
			"Histórico do servidor · CPU total, memória em uso · 24 h", "Server history · total CPU, memory in use · 24 h"},
		{"comparar_apps", `{"campo":"mem","periodo":"24h"}`,
			"Comparando apps · memória em uso · 24 h", "Comparing apps · memory in use · 24 h"},
		{"eventos", `{"horas":24}`, "Eventos dos contêineres", "Container events"},
		{"logs", `{"conteiner":"blog-worker-1"}`, "Logs de blog-worker-1", "Logs from blog-worker-1"},
		{"logs", `{"conteiner":"loja-api-1","so_erros":true}`, "Logs de loja-api-1 · só erros", "Logs from loja-api-1 · errors only"},
		{"logs", `{"conteiner":"*","so_erros":true,"filtro":"timeout"}`,
			`Logs de todos os contêineres · só erros · "timeout"`, `Logs from all containers · errors only · "timeout"`},
		{"historico", `{"alvo":"loja","periodo":"7d"}`, "Histórico de loja · 7 dias", "loja history · 7 days"},
		{"historico_servidor", `{"campos":["load1","util","psiio"],"periodo":"1y"}`,
			"Histórico do servidor · carga 1 min, disco ocupado (%), pressão de disco · 1 ano",
			"Server history · 1-min load, disk busy (%), disk pressure · 1 year"},
		{"banda", `{}`, "Banda por dia, mês e app", "Bandwidth by day, month and app"},
		{"processos", `{}`, "Processos que mais gastam", "Top processes"},
		{"disco_docker", `{}`, "Disco do Docker", "Docker disk"},
	} {
		pt := m.aiLabel(c.tool, c.args)
		if pt != c.pt {
			t.Errorf("%s %s: %q, esperava %q", c.tool, c.args, pt, c.pt)
		}
		if en := i18n.Tr("en", pt); en != c.en {
			t.Errorf("%q em inglês: %q, esperava %q", pt, en, c.en)
		}
	}

	// toda ferramenta, com todo campo e período: nada de português no inglês
	pt := regexp.MustCompile(`(?i)[à-ÿ]|\b(de|do|da|dos|em|no|uso|dias|ano|todos|erros|rede|entrada|disco|leitura|escrita|carga|espera|pressão|conexões|histórico|comparando|banda|processos|eventos|servidor|usuário|sistema|espaço|usado|ocupado|operações)\b`)
	campos, _ := json.Marshal(hostFieldEnum)
	for _, tl := range m.aiTools() {
		name := tl.Function.Name
		for _, campo := range unitFieldEnum {
			for _, per := range rangeEnum {
				for _, alvo := range []string{"loja-api-1", "*"} {
					args := fmt.Sprintf(`{"campos":%s,"campo":%q,"periodo":%q,"alvo":"loja","conteiner":%q,"so_erros":true,"filtro":"timeout"}`, campos, campo, per, alvo)
					label := m.aiLabel(name, args)
					if label == name {
						t.Fatalf("%s não tem rótulo em aiLabel", name)
					}
					if en := i18n.Tr("en", label); pt.MatchString(en) {
						t.Errorf("sobrou português (%q) em %q (de %q)", pt.FindString(en), en, label)
					}
				}
			}
		}
	}
}
