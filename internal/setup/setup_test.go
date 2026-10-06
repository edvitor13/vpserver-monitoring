package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInitCreatesFilesOnceAndKeepsEnv(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := Run(Options{Out: dir, DockerGID: "988", Stdout: &out, MemTotal: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(env)
	for _, want := range []string{"VPMON_USER=admin", "VPMON_FORCE_PASSWORD_CHANGE=true", "DOCKER_GID=988",
		"VPMON_TUNNEL_COMMAND=tunnel --no-autoupdate --url http://vpserver-monitor:8080",
		"\nCOMPOSE_PROFILES=whatsapp\n", "\nVPMON_WA_KEY=", "\nVPMON_WA_DB_PASSWORD="} {
		if !strings.Contains(s, want) {
			t.Fatalf(".env sem %q:\n%s", want, s)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".env")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env deveria ser 600: %v", fi.Mode())
	}
	if !strings.Contains(out.String(), "usuário admin") {
		t.Fatalf("saída sem as instruções: %s", out.String())
	}
	if c, _ := os.ReadFile(filepath.Join(dir, "compose.yml")); !bytes.Equal(c, composeYML) {
		t.Fatal("compose.yml diferente do embutido")
	}
	// rodar de novo (atualização) não mexe no .env
	if err := Run(Options{Out: dir, DockerGID: "1", TunnelToken: "x", Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, ".env")); !bytes.Equal(again, env) {
		t.Fatal("o .env não pode ser reescrito numa atualização")
	}
}

func TestInitWithTunnelToken(t *testing.T) {
	dir := t.TempDir()
	if err := Run(Options{Out: dir, DockerGID: "988", TunnelToken: "eyJabc", Stdout: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "VPMON_TUNNEL_TOKEN=eyJabc\nVPMON_TUNNEL_COMMAND=\n") {
		t.Fatalf("com token, sem Quick Tunnel:\n%s", env)
	}
}

func TestInitNeedsDockerGID(t *testing.T) {
	if err := Run(Options{Out: t.TempDir(), Stdout: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "DOCKER_GID") {
		t.Fatalf("deveria pedir DOCKER_GID: %v", err)
	}
}

func TestInitSmallVMLeavesWhatsAppOff(t *testing.T) {
	dir := t.TempDir()
	if err := Run(Options{Out: dir, DockerGID: "988", Stdout: &bytes.Buffer{}, MemTotal: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "\nCOMPOSE_PROFILES=\n") || strings.Contains(string(env), "\nCOMPOSE_PROFILES=whatsapp") {
		t.Fatalf("VM de 1 GB: WhatsApp desligado:\n%s", env)
	}
}

func TestInitUpgradeAppendsWhatsAppKeys(t *testing.T) {
	dir := t.TempDir()
	old := "VPMON_USER=ana\nVPMON_PASSWORD=x\n"
	os.WriteFile(filepath.Join(dir, ".env"), []byte(old), 0o600)
	if err := Run(Options{Out: dir, Stdout: &bytes.Buffer{}, MemTotal: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	s := string(env)
	if !strings.HasPrefix(s, old) || !strings.Contains(s, "\nVPMON_WA_KEY=") || !strings.Contains(s, "\nCOMPOSE_PROFILES=whatsapp\n") {
		t.Fatalf("atualização deveria só acrescentar:\n%s", s)
	}
	if err := Run(Options{Out: dir, Stdout: &bytes.Buffer{}, MemTotal: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, ".env")); string(again) != s {
		t.Fatal("rodar de novo não pode acrescentar outra vez")
	}
}
