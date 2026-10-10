package sshchat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/sshchat/sshtest"
	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/ssh"
)

// --- ajudantes -----------------------------------------------------------------------------

func newService(t *testing.T, f *sshtest.Server) *Service {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.nets = func() []string { return []string{"172.20.0.0/16"} }
	if f != nil {
		if err := s.SetTarget("tester", "127.0.0.1", f.Port()); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// waitDone espera o último bloco terminar e devolve a saída dele.
func waitDone(t *testing.T, sh *Shell) (BlockView, State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, bl := sh.Snapshot(0, 0)
		if len(bl) > 0 && !st.Busy {
			return bl[len(bl)-1], st
		}
		if time.Now().After(deadline) {
			t.Fatalf("o comando não terminou: %+v %+v", st, bl)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		sh.Wait(ctx, st.Ver)
		cancel()
	}
}

func waitOut(t *testing.T, sh *Shell, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, bl := sh.Snapshot(0, 0)
		if len(bl) > 0 && strings.Contains(bl[len(bl)-1].Out, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sem %q na saída: %+v", want, bl)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runOK(t *testing.T, sh *Shell, cmd string) (BlockView, State) {
	t.Helper()
	if _, err := sh.Run("chefe", "203.0.113.7", cmd); err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return waitDone(t, sh)
}

// --- testes --------------------------------------------------------------------------------

func TestKeyPersistsAndSetupScript(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.nets = func() []string { return []string{"172.20.0.0/16"} }
	v := s.View()
	if v.Enabled || v.User != DefaultUser || v.Host != DefaultHost || v.Port != 22 {
		t.Fatalf("padrão: %+v", v)
	}
	if !strings.HasPrefix(v.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(v.PublicKey, " vpserver-monitoring") {
		t.Fatalf("chave pública: %q", v.PublicKey)
	}
	if v.AuthLine != `from="172.20.0.0/16",restrict,pty `+v.PublicKey {
		t.Fatalf("linha do authorized_keys: %q", v.AuthLine)
	}
	for _, want := range []string{"useradd --create-home --shell /bin/bash \"$U\"", "usermod -aG \"$G\" \"$U\"", "U=vpserver-ssh",
		"K='" + v.AuthLine + "'", "chmod 600", "sudo passwd vpserver-ssh"} {
		if !strings.Contains(v.Setup, want) {
			t.Fatalf("o comando de preparo não tem %q:\n%s", want, v.Setup)
		}
	}
	if strings.Contains(v.Setup, "PRIVATE") || strings.Contains(v.Setup, s.f.Key) {
		t.Fatal("a chave privada não pode aparecer no comando")
	}
	st, _ := os.Stat(filepath.Join(dir, "ssh.json"))
	if st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("ssh.json aberto demais: %v", st.Mode())
	}
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s2.nets = s.nets
	if s2.View().PublicKey != v.PublicKey {
		t.Fatal("a chave do painel tem de durar entre reinícios")
	}
	if err := s2.NewKey(); err != nil || s2.View().PublicKey == v.PublicKey {
		t.Fatalf("trocar a chave: %v", err)
	}
	if err := s2.SetTarget("Root;rm", "x", 22); err == nil {
		t.Fatal("usuário com caractere estranho tem de ser recusado")
	}
	if err := s2.SetTarget("ok", "host com espaço", 22); err == nil {
		t.Fatal("endereço com espaço tem de ser recusado")
	}
}

func TestProbeEnableAndHostKeyPinning(t *testing.T) {
	ctx := context.Background()
	f := sshtest.New(t)
	s := newService(t, f)

	// ninguém escutando
	s2 := newService(t, nil)
	s2.SetTarget("tester", "127.0.0.1", 1)
	if p := s2.Probe(ctx, false); p.Reachable || p.Error == "" {
		t.Fatalf("sem servidor: %+v", p)
	}

	// abrir a tela só confere se o sshd responde: nenhuma tentativa de login
	if p := s.Probe(ctx, false); !p.Reachable || p.Authorized || p.Error != "" || f.Attempts() != 0 {
		t.Fatalf("sem login: %+v, %d tentativas", p, f.Attempts())
	}
	// servidor responde, chave ainda não autorizada
	p := s.Probe(ctx, true)
	if !p.Reachable || p.Authorized || !strings.HasPrefix(p.HostKey, "SHA256:") || !strings.Contains(p.Error, "passo 1") {
		t.Fatalf("antes do preparo: %+v", p)
	}
	if err := s.Enable(ctx, "chefe"); err == nil || s.Enabled() {
		t.Fatal("não pode ligar sem a chave autorizada")
	}

	f.Allow(s.signer.PublicKey())
	if p := s.Probe(ctx, true); !p.Authorized || !p.Sudo || p.Groups != "tester sudo docker" {
		t.Fatalf("depois do preparo: %+v", p)
	}
	if _, err := s.Open(ctx, "chefe"); err != ErrDisabled {
		t.Fatalf("desligado não abre sessão: %v", err)
	}
	if err := s.Enable(ctx, "chefe"); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); !v.Enabled || v.HostKey != ssh.FingerprintSHA256(f.HostKey()) || v.EnabledBy != "chefe" {
		t.Fatalf("ligado: %+v", v)
	}
	if err := s.SetTarget("outro", "127.0.0.1", 22); err != ErrEnabled {
		t.Fatal("ligado, não muda o alvo")
	}

	// o servidor troca de chave: a sessão é recusada
	f2 := sshtest.New(t) // outra chave de servidor
	f2.Allow(s.signer.PublicKey())
	s.mu.Lock()
	s.f.Port = f2.Port()
	s.mu.Unlock()
	if _, err := s.Open(ctx, "chefe"); err == nil || !strings.Contains(err.Error(), "chave do servidor SSH mudou") {
		t.Fatalf("chave do servidor diferente tem de ser recusada: %v", err)
	}
	if p := s.Probe(ctx, false); !p.HostKeyNew {
		t.Fatalf("a verificação avisa que a chave mudou: %+v", p)
	}
}

func enabled(t *testing.T) (*Service, *sshtest.Server, *Shell) {
	t.Helper()
	f := sshtest.New(t)
	s := newService(t, f)
	f.Allow(s.signer.PublicKey())
	if err := s.Enable(context.Background(), "chefe"); err != nil {
		t.Fatal(err)
	}
	sh, err := s.Open(context.Background(), "chefe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sh.Close("fim do teste") })
	return s, f, sh
}

func TestShellCommandsCwdAndCodes(t *testing.T) {
	s, f, sh := enabled(t)
	if !strings.Contains(f.RC(), "PROMPT_COMMAND=__vpmon") || !strings.Contains(f.RC(), "stty -echo") {
		t.Fatalf("rc gravado: %q", f.RC())
	}
	st, bl := sh.Snapshot(0, 0)
	if !st.Ready || st.Busy || st.Cwd != "/home/tester" || st.UID != 1000 || len(bl) != 0 {
		t.Fatalf("o que o bash imprime ao abrir não vira bloco: %+v %+v", st, bl)
	}
	b, _ := runOK(t, sh, "echo olá mundo")
	if b.Out != "olá mundo\r\n" || *b.Code != 0 || b.Cwd != "/home/tester" {
		t.Fatalf("echo: %+v", b)
	}
	b, st = runOK(t, sh, "cd /srv/app")
	if st.Cwd != "/srv/app" || *b.Code != 0 {
		t.Fatalf("cd: %+v", st)
	}
	if b, _ = runOK(t, sh, "false"); *b.Code != 1 || b.Cwd != "/srv/app" {
		t.Fatalf("false: %+v", b)
	}
	if b, _ = runOK(t, sh, "split"); b.Out != "abc" || *b.Code != 0 {
		t.Fatalf("marcador partido entre leituras: %+v", b)
	}
	if b, _ = runOK(t, sh, "noise"); b.Out != "abc\r\n" {
		t.Fatalf("\\x1e/\\x1f do programa não podem fingir entrada: %q", b.Out)
	}
	// várias linhas viram um comando só
	if b, _ = runOK(t, sh, "echo um\necho dois\r\n"); b.Out != "um\r\ndois\r\n" || b.Cmd != "echo um\necho dois" {
		t.Fatalf("várias linhas: %+v", b)
	}
	// controle no meio do comando some
	if b, _ = runOK(t, sh, "echo a\x1b[31mb\x07"); b.Out != "a[31mb\r\n" {
		t.Fatalf("caracteres de controle: %q", b.Out)
	}
	if _, err := sh.Run("chefe", "", "   "); err != ErrEmpty {
		t.Fatalf("vazio: %v", err)
	}
	// histórico para o autocompletar
	h := s.History("chefe")
	if len(h) == 0 || h[len(h)-1] != "echo a[31mb" || h[0] != "echo olá mundo" {
		t.Fatalf("histórico: %q", h)
	}
	runOK(t, sh, "echo olá mundo")
	if h := s.History("chefe"); h[len(h)-1] != "echo olá mundo" || strings.Count(strings.Join(h, "|"), "echo olá mundo") != 1 {
		t.Fatalf("repetido sobe para o fim, sem duplicar: %q", h)
	}
}

func TestShellInputsKeysAndSudo(t *testing.T) {
	s, _, sh := enabled(t)
	if _, err := sh.Run("chefe", "", "sleep"); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Run("chefe", "", "echo x"); err != ErrBusy {
		t.Fatalf("com algo rodando, comando novo é recusado: %v", err)
	}
	if err := sh.Key("ctrl-c"); err != nil {
		t.Fatal(err)
	}
	if b, _ := waitDone(t, sh); *b.Code != 130 {
		t.Fatalf("Ctrl+C: %+v", b)
	}
	if err := sh.Key("ctrl-d"); err != ErrIdle {
		t.Fatalf("Ctrl+D com o shell livre fecharia a sessão: %v", err)
	}
	if err := sh.Input("chefe", "", "x", false); err != ErrIdle {
		t.Fatalf("entrada sem nada rodando: %v", err)
	}

	// senha: vai para o programa, aparece como pontinhos, não vai para o registro
	sh.Run("chefe", "", "askpass")
	waitOut(t, sh, "Password: ")
	if err := sh.Input("chefe", "", "segredo-123", true); err != nil {
		t.Fatal(err)
	}
	b, _ := waitDone(t, sh)
	if !strings.Contains(b.Out, "\x1e••••••\x1f") || !strings.Contains(b.Out, "got 11") || strings.Contains(b.Out, "segredo") {
		t.Fatalf("senha: %q", b.Out)
	}

	// sudo -i vira o shell do chat como root (mesmo rc), e exit volta
	sh.Run("chefe", "", "sudo  -i")
	waitOut(t, sh, "[sudo] password")
	sh.Input("chefe", "", "errada", true)
	if b, st := waitDone(t, sh); b.Note != "modo sudo" || b.Cmd != "sudo  -i" || *b.Code != 1 || st.UID != 1000 {
		t.Fatalf("senha errada: continua sem sudo: %+v %+v", b, st)
	}
	sh.Run("chefe", "", "sudo su -")
	waitOut(t, sh, "[sudo] password")
	sh.Input("chefe", "", "certa", true)
	if _, st := waitDone(t, sh); st.UID != 0 || st.Cwd != "/root" {
		t.Fatalf("modo sudo: %+v", st)
	}
	if _, st := runOK(t, sh, "exit"); st.UID != 1000 || st.Closed != "" {
		t.Fatalf("exit sai do sudo sem fechar a sessão: %+v", st)
	}

	// registro: comandos com código, entradas, senha nunca
	log := s.Log(100)
	var all []string
	for _, e := range log {
		all = append(all, e.Kind+":"+e.Text)
		if strings.Contains(e.Text, "segredo") || strings.Contains(e.Text, "certa") {
			t.Fatalf("senha no registro: %+v", e)
		}
	}
	j := strings.Join(all, "|")
	if !strings.Contains(j, "cmd:sleep") || !strings.Contains(j, "input:(senha)") || !strings.Contains(j, "cmd:sudo su -") {
		t.Fatalf("registro: %s", j)
	}
	for _, e := range log {
		if e.Kind == "cmd" && e.Text == "sleep" && (e.Code == nil || *e.Code != 130 || e.IP != "") {
			t.Fatalf("o código de saída vai junto do comando: %+v", e)
		}
	}

	// exit no shell do usuário fecha a sessão
	sh.Run("chefe", "", "exit")
	deadline := time.Now().Add(3 * time.Second)
	for sh.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sh.Alive() || s.Shell("chefe") != nil {
		t.Fatal("exit no shell do usuário fecha a sessão")
	}
}

func TestOutputCapAndIncrementalPoll(t *testing.T) {
	_, _, sh := enabled(t)
	b, _ := runOK(t, sh, "big")
	if b.Base == 0 || len(b.Out) > maxOut+maxOut/4 || !strings.HasSuffix(b.Out, "ação\n") {
		t.Fatalf("saída grande guarda só o fim: base %d, %d bytes", b.Base, len(b.Out))
	}
	if !utf8Valid(b.Out) { // o corte cai numa fronteira de caractere (nunca no meio do "ç")
		t.Fatalf("corte no meio de um caractere: %q", b.Out[:20])
	}
	// pedido incremental: do bloco b a partir de o
	sh.Run("chefe", "", "askpass")
	waitOut(t, sh, "Password: ")
	st, bl := sh.Snapshot(0, 0)
	cur := bl[len(bl)-1]
	_, inc := sh.Snapshot(cur.ID, cur.To)
	if len(inc) != 1 || inc[0].Out != "" || inc[0].From != cur.To {
		t.Fatalf("nada novo: %+v", inc)
	}
	sh.Input("chefe", "", "abc", false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	sh.Wait(ctx, st.Ver)
	cancel()
	b2, _ := waitDone(t, sh)
	_, inc = sh.Snapshot(cur.ID, cur.To)
	if len(inc) != 1 || inc[0].Out != b2.Out[cur.To:] || !strings.HasPrefix(inc[0].Out, "\x1eabc\x1f") {
		t.Fatalf("só o que veio depois: %q", inc[0].Out)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }

func TestCompleteAndReap(t *testing.T) {
	s, _, sh := enabled(t)
	ctx := context.Background()
	if items, err := sh.Complete(ctx, "dock", "cmd"); err != nil || strings.Join(items, ",") != "docker,docker-compose" {
		t.Fatalf("comandos: %v %v", items, err)
	}
	if items, _ := sh.Complete(ctx, "ap", "file"); strings.Join(items, ",") != "app/,app.log" {
		t.Fatalf("arquivos: %v", items)
	}

	// parada há mais de 30 min: fecha
	now := time.Now()
	s.now = func() time.Time { return now }
	s.Reap()
	if s.Shell("chefe") == nil {
		t.Fatal("ainda não venceu")
	}
	now = now.Add(IdleAfter + time.Minute)
	s.Reap()
	if s.Shell("chefe") != nil || sh.Alive() {
		t.Fatal("parada há mais de 30 min fecha")
	}
	if _, err := sh.Run("chefe", "", "echo x"); err != ErrClosed {
		t.Fatalf("sessão fechada: %v", err)
	}
	// reabrir cria outra sessão
	sh2, err := s.Open(ctx, "chefe")
	if err != nil || sh2.ID == sh.ID {
		t.Fatalf("reabrir: %v", err)
	}
	if err := s.Disable(); err != nil || sh2.Alive() || s.Shell("chefe") != nil {
		t.Fatal("desligar fecha as sessões")
	}
}

func TestRootShellAliases(t *testing.T) {
	for cmd, want := range map[string]bool{
		"sudo -i": true, "sudo -s": true, "sudo su": true, "sudo su -": true, "sudo bash": true, "su": true, "su -": true,
		"sudo su - root": true, "sudo -i -u root": true,
		"sudo ls": false, "sudo -i ls": false, "echo sudo -i": false, "sudo systemctl restart x": false, "sudoedit x": false,
	} {
		if got := rootShell.MatchString(cmd); got != want {
			t.Errorf("%q: %v", cmd, got)
		}
	}
}

func TestFeedAndUTF8Helpers(t *testing.T) {
	if partialPrefix([]byte("abc\x1b]77"), markStart) != 4 || partialPrefix([]byte("abc"), markStart) != 0 {
		t.Fatal("partialPrefix")
	}
	b := []byte("ação")
	if completeUTF8(b[:2]) != 1 || completeUTF8(b) != len(b) || completeUTF8(b[:3]) != 3 {
		t.Fatalf("completeUTF8: %d %d", completeUTF8(b[:2]), completeUTF8(b[:3]))
	}
}
