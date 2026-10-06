package monitor

import "testing"

func TestServiceName(t *testing.T) {
	if n, _ := ServiceName("actions.runner.minhaorg-lojaapp.meu-servidor.service"); n != "Runner do GitHub · lojaapp" {
		t.Fatalf("runner: %q", n)
	}
	if n, _ := ServiceName("snap.oracle-cloud-agent.oracle-cloud-agent-updater.service"); n != "Oracle Cloud Agent (atualizador)" {
		t.Fatalf("updater: %q", n)
	}
	if n, d := ServiceName("algo-novo.service"); n != "algo-novo" || d != "" {
		t.Fatalf("serviço desconhecido deveria aparecer com o próprio nome: %q", n)
	}
}

func TestPrettyAndAppNames(t *testing.T) {
	if got := PrettyName("meu-app_api"); got != "Meu App Api" {
		t.Fatal(got)
	}
	m := ParseAppNames("loja=Loja, financas = Finanças,,lixo")
	if m["loja"] != "Loja" || m["financas"] != "Finanças" || len(m) != 2 {
		t.Fatalf("%v", m)
	}
}

func TestPercentile(t *testing.T) {
	v := make([]float64, 100)
	for i := range v {
		v[i] = float64(i + 1)
	}
	if p := percentile(v, 95); p != 95 {
		t.Fatalf("p95 = %v", p)
	}
}
