package sshchat

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/ssh"
)

// Ponta a ponta com o OpenSSH e o bash de verdade: sobe um sshd sem root
// (só o próprio usuário entra) numa porta qualquer, com a linha do
// authorized_keys que a tela manda colar. Pulado onde não há sshd (no CI, o
// workflow instala o openssh-server).
func TestRealSSHD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sshd de verdade só no Linux")
	}
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("sem /usr/sbin/sshd")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("sem bash")
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if me.Uid == "0" {
		t.Skip("rode como usuário comum (o sshd sem root só deixa entrar o próprio usuário)")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)

	s, err := New(filepath.Join(dir))
	if err != nil {
		t.Fatal(err)
	}
	s.nets = func() []string { return []string{"127.0.0.0/8"} }

	_, hpriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(hpriv, "host")
	hostKey := filepath.Join(dir, "host_key")
	os.WriteFile(hostKey, pem.EncodeToMemory(blk), 0o600)
	auth := filepath.Join(dir, "authorized_keys")
	os.WriteFile(auth, []byte(s.View().AuthLine+"\n"), 0o600)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg := filepath.Join(dir, "sshd_config")
	os.WriteFile(cfg, []byte(fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
AuthorizedKeysFile %s
PidFile %s
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
LogLevel ERROR
`, port, hostKey, auth, filepath.Join(dir, "sshd.pid"))), 0o600)
	cmd := exec.Command(sshd, "-D", "-e", "-f", cfg)
	var serr strings.Builder
	cmd.Stderr = &serr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; ; i++ {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			c.Close()
			break
		}
		if i > 100 {
			t.Fatalf("sshd não subiu: %s", serr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	ctx := context.Background()
	if err := s.SetTarget(me.Username, "127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	if p := s.Probe(ctx, true); !p.Authorized {
		t.Fatalf("verificação: %+v\n%s", p, serr.String())
	}
	if err := s.Enable(ctx, "chefe"); err != nil {
		t.Fatal(err)
	}
	sh, err := s.Open(ctx, "chefe")
	if err != nil {
		t.Fatalf("abrir: %v\n%s", err, serr.String())
	}
	defer sh.Close("fim")
	st, bl := sh.Snapshot(0, 0)
	if !st.Ready || len(bl) != 0 || st.Cwd == "" || st.UID == 0 {
		t.Fatalf("ao abrir: %+v %+v", st, bl)
	}

	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	b, _ := runOK(t, sh, "echo olá; echo erro >&2")
	if norm(b.Out) != "olá\nerro\n" || *b.Code != 0 {
		t.Fatalf("echo: %q", b.Out)
	}
	if b, st = runOK(t, sh, "cd /tmp && X=42"); st.Cwd != "/tmp" {
		t.Fatalf("cd: %+v", st)
	}
	if b, _ = runOK(t, sh, `echo "$X $PWD $TERM"`); norm(b.Out) != "42 /tmp dumb\n" {
		t.Fatalf("variável e pasta continuam de um comando para o outro: %q", b.Out)
	}
	if b, _ = runOK(t, sh, "(exit 3)"); *b.Code != 3 {
		t.Fatalf("código: %+v", b)
	}
	if b, _ = runOK(t, sh, `echo "oi!"`); norm(b.Out) != "oi!\n" {
		t.Fatalf("! não pode virar expansão de histórico: %q", b.Out)
	}
	if b, _ = runOK(t, sh, "for i in 1 2 3\ndo\n  echo n$i\ndone"); norm(b.Out) != "n1\nn2\nn3\n" {
		t.Fatalf("várias linhas: %q", b.Out)
	}

	// senha pedida no terminal (como o sudo): campo escondido, sem eco
	sh.Run("chefe", "", `read -s -p "Password: " x; echo; echo "got ${#x}"`)
	waitOut(t, sh, "Password: ")
	sh.Input("chefe", "", "segredo-123", true)
	if b, _ = waitDone(t, sh); strings.Contains(b.Out, "segredo") || !strings.Contains(norm(b.Out), "got 11\n") {
		t.Fatalf("senha: %q", b.Out)
	}

	// programa lendo linhas: a entrada chega, o eco não volta
	sh.Run("chefe", "", "head -n 2")
	sh.Input("chefe", "", "linha um", false)
	waitOut(t, sh, "linha um\r\n")
	sh.Input("chefe", "", "linha dois", false)
	if b, _ = waitDone(t, sh); norm(b.Out) != "\x1elinha um\x1flinha um\n\x1elinha dois\x1flinha dois\n" {
		t.Fatalf("entradas: %q", b.Out)
	}

	// Ctrl+C mata o comando, não o shell; Ctrl+D fecha a entrada
	sh.Run("chefe", "", "sleep 30")
	time.Sleep(300 * time.Millisecond)
	sh.Key("ctrl-c")
	if b, st = waitDone(t, sh); *b.Code != 130 || st.Closed != "" {
		t.Fatalf("Ctrl+C: %+v %+v", b, st)
	}
	sh.Run("chefe", "", "cat; echo fim")
	sh.Input("chefe", "", "abc", false)
	sh.Key("ctrl-d")
	if b, _ = waitDone(t, sh); !strings.HasSuffix(norm(b.Out), "abc\nfim\n") {
		t.Fatalf("Ctrl+D: %q", b.Out)
	}
	if b, _ = runOK(t, sh, "git --version >/dev/null 2>&1; systemctl --version >/dev/null 2>&1; echo ok"); norm(b.Out) != "ok\n" {
		t.Fatalf("paginadores desligados: %q", b.Out)
	}

	// autocompletar
	if items, _ := sh.Complete(ctx, "ech", "cmd"); !contains(items, "echo") {
		t.Fatalf("comandos: %v", items)
	}
	os.MkdirAll(filepath.Join(dir, "pasta-x"), 0o700)
	runOK(t, sh, "cd "+quote(dir))
	if items, _ := sh.Complete(ctx, "pasta", "file"); !contains(items, "pasta-x/") {
		t.Fatalf("arquivos: %v", items)
	}

	// modo sudo (onde o sudo não pede senha, como no CI)
	if exec.Command("sudo", "-n", "true").Run() == nil {
		b, st = runOK(t, sh, "sudo -i")
		if st.UID != 0 || b.Note != "modo sudo" || *b.Code != 0 {
			t.Fatalf("modo sudo: %+v %+v", b, st)
		}
		if b, _ = runOK(t, sh, "whoami; cd /root && echo $TERM"); norm(b.Out) != "root\ndumb\n" {
			t.Fatalf("root com o rc do chat: %q", b.Out)
		}
		if _, st = runOK(t, sh, "exit"); st.UID == 0 || st.Closed != "" {
			t.Fatalf("exit volta ao usuário: %+v", st)
		}
	} else {
		t.Log("sudo pede senha aqui: modo sudo não testado")
	}

	// exit no shell do usuário encerra a sessão
	sh.Run("chefe", "", "exit")
	for i := 0; sh.Alive() && i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if sh.Alive() {
		t.Fatal("exit encerra a sessão")
	}
}
