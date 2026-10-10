package monitor

import (
	"strings"
	"testing"
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
