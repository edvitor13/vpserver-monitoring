package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cleanerScript tira o script do vpserver-cleaner de um compose (sem o "$$" do Compose).
func cleanerScript(t *testing.T, compose string) string {
	t.Helper()
	_, block, ok := strings.Cut(compose, "\n  cleaner:\n")
	if !ok {
		t.Fatal("o compose não tem o vpserver-cleaner")
	}
	_, block, _ = strings.Cut(block, "      - |\n")
	block, _, _ = strings.Cut(block, "\n    volumes:")
	var lines []string
	for _, ln := range strings.Split(block, "\n") {
		lines = append(lines, strings.TrimPrefix(ln, "        "))
	}
	return strings.ReplaceAll(strings.Join(lines, "\n"), "$$", "$")
}

func TestCleanerSameInBothComposes(t *testing.T) {
	deploy, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if cleanerScript(t, string(deploy)) != cleanerScript(t, string(composeYML)) {
		t.Fatal("o script do vpserver-cleaner difere entre deploy/compose.yml e o do instalador")
	}
	for _, c := range []string{string(deploy), string(composeYML)} {
		// escritas: só pausar/retomar, o exec dos backups e as duas limpezas
		if !strings.Contains(c, `-allowPOST=(/v1\.[0-9]+)?/(containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/(pause|unpause|exec)|exec/[a-f0-9]+/start|build/prune|images/prune)`+"\n") {
			t.Fatal("o proxy deve liberar só pausar/retomar, o exec dos backups e as duas limpezas")
		}
		// leitura: nada de inspect de contêiner (mostraria o ambiente, com senhas)
		if !strings.Contains(c, `|exec/[a-f0-9]+/json)`+"\n") || strings.Contains(c, "containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/json") {
			t.Fatal("o proxy deve ler só o resultado do exec, nunca o inspect do contêiner")
		}
	}
}

// Roda o script de verdade (uma volta) contra pastas temporárias.
func TestCleanerScript(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sem sh nesta máquina")
	}
	if _, err := exec.LookPath("stat"); err != nil {
		t.Skip("sem stat nesta máquina")
	}
	base := t.TempDir()
	req, logs, sizes := filepath.Join(base, "req"), filepath.Join(base, "logs"), filepath.Join(base, "sizes")
	for _, d := range []string{req, sizes} {
		os.MkdirAll(d, 0o755)
	}
	idA, idB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(logs, idA, idA+"-json.log"), strings.Repeat("x", 1000))
	write(filepath.Join(logs, idA, idA+"-json.log.1"), strings.Repeat("y", 500))
	write(filepath.Join(logs, idA, "config.v2.json"), "segredo")
	write(filepath.Join(logs, idB, idB+"-json.log"), strings.Repeat("z", 300))
	// pedido com lixo: só o ID válido conta
	write(filepath.Join(req, "req-00ff"), idA+"\n../../etc\n"+strings.Repeat("a", 63)+"\nZZ"+idB[2:]+"\n")
	write(filepath.Join(req, "req-nao-hex"), idB+"\n")

	slash := func(p string) string { return filepath.ToSlash(p) }
	script := cleanerScript(t, string(composeYML))
	script = strings.Replace(script, "R=/req L=/logs S=/sizes", "R='"+slash(req)+"' L='"+slash(logs)+"' S='"+slash(sizes)+"'", 1)
	script = strings.Replace(script, "sleep 2", "exit 0", 1) // uma volta só
	out, err := exec.Command(sh, "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}

	size := func(p string) int64 {
		st, err := os.Stat(p)
		if err != nil {
			return -1
		}
		return st.Size()
	}
	if size(filepath.Join(logs, idA, idA+"-json.log")) != 0 {
		t.Fatal("o log atual deveria estar zerado (não apagado)")
	}
	if size(filepath.Join(logs, idA, idA+"-json.log.1")) != -1 {
		t.Fatal("o rotacionado deveria ter sido apagado")
	}
	if size(filepath.Join(logs, idA, "config.v2.json")) != 7 || size(filepath.Join(logs, idB, idB+"-json.log")) != 300 {
		t.Fatal("mexeu no que não devia")
	}
	res, err := os.ReadFile(filepath.Join(req, "res-00ff"))
	if err != nil {
		t.Fatalf("sem resposta: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(res)), "\n"); len(lines) != 2 || !strings.HasPrefix(lines[0], "1000 ") || !strings.HasPrefix(lines[1], "500 ") {
		t.Fatalf("resposta: %q", res)
	}
	if _, err := os.Stat(filepath.Join(req, "req-nao-hex")); !os.IsNotExist(err) {
		t.Fatal("pedido com nome inválido é descartado")
	}
	if size(filepath.Join(logs, idB, idB+"-json.log")) != 300 {
		t.Fatal("pedido com nome inválido não pode limpar nada")
	}
	if _, err := os.Stat(filepath.Join(req, ".alive")); err != nil {
		t.Fatal("o ajudante marca que está vivo")
	}
	if b, _ := os.ReadFile(filepath.Join(sizes, "logsizes.txt")); !strings.Contains(string(b), "300 ") {
		t.Fatalf("tamanhos atualizados: %q", b)
	}
}
