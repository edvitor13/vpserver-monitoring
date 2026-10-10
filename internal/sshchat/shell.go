package sshchat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/ssh"
)

// O shell do chat é um bash interativo num pty, sem readline e sem eco, com
// um rc próprio (rcScript): o prompt é vazio e, no lugar dele, o bash manda um
// marcador invisível (OSC 777) com o código de saída, o uid e a pasta atual.
// Assim cada comando vira um bloco com começo, saída e fim, e a pasta, as
// variáveis e o modo sudo continuam de um comando para o outro.

// rcScript é gravado em ~/.vpmon-rc a cada sessão (o mesmo serve ao root no modo sudo).
const rcScript = `# vpserver-monitoring: shell do chat de SSH (o painel regrava este arquivo a cada sessão)
[ -r ~/.bashrc ] && . ~/.bashrc >/dev/null 2>&1
stty -echo 2>/dev/null
set +H
export TERM=dumb PAGER=cat GIT_PAGER=cat SYSTEMD_PAGER=cat MANPAGER=cat SYSTEMD_COLORS=0 NO_COLOR=1 DEBIAN_FRONTEND=noninteractive
unset HISTFILE
PS1= PS2=
__vpmon() { local c=$?; printf '\033]777;vpmon;%s;%s;%s\007' "$c" "$EUID" "$(pwd | base64 -w0 2>/dev/null)"; }
PROMPT_COMMAND=__vpmon
`

var markStart = []byte("\x1b]777;vpmon;")

const (
	maxOut    = 64 << 10 // saída guardada por bloco (o fim; o começo é cortado)
	maxBlocks = 40
	maxCmd    = 16 << 10
	inOpen    = '\x1e' // entrada digitada num programa rodando: aparece na saída entre \x1e e \x1f
	inClose   = '\x1f'
)

var (
	ErrBusy   = errors.New("ainda há um comando rodando: espere, mande uma entrada ou use Ctrl+C")
	ErrIdle   = errors.New("não há comando rodando")
	ErrClosed = errors.New("a sessão de SSH fechou")
	ErrEmpty  = errors.New("comando vazio")
	ErrLong   = errors.New("comando grande demais")
)

// rootShell são os jeitos comuns de virar root: viram o shell do chat como root
// (com o mesmo rc), senão o chat perderia os marcadores.
var rootShell = regexp.MustCompile(`^(sudo\s+-[is]|sudo\s+-i\s+-u\s+root|sudo\s+(su|bash|sh)(\s+-l?)?(\s+root)?|sudo\s+su\s+-\s+root|su(\s+-)?(\s+root)?)$`)

// Block é um comando e a saída dele.
type Block struct {
	ID    int    `json:"id"`
	Cmd   string `json:"cmd"`
	Note  string `json:"note,omitempty"`
	By    string `json:"by"`
	Start int64  `json:"start"`
	End   int64  `json:"end,omitempty"`
	Code  *int   `json:"code,omitempty"`
	UID   int    `json:"uid"`
	Cwd   string `json:"cwd"`
	out   []byte
	base  int64 // bytes cortados do começo
}

// Shell é uma sessão aberta (um bash num pty) de uma pessoa do painel.
type Shell struct {
	ID     string
	owner  string
	svc    *Service
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	rcPath string
	opened time.Time
	wmu    sync.Mutex // escrita no stdin

	mu      sync.Mutex
	changed chan struct{}
	ver     int64
	blocks  []*Block
	nextID  int
	ready   bool
	busy    bool
	uid     int
	cwd     string
	closed  string
	last    time.Time
	pend    []byte
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func startShell(ctx context.Context, c *ssh.Client, owner string, svc *Service) (*Shell, error) {
	home, err := run(c, `printf %s "$HOME"`, nil)
	if err != nil || !strings.HasPrefix(home, "/") {
		return nil, fmt.Errorf("não consegui ler a pasta do usuário no servidor (%q): %v", short(home), err)
	}
	if out, err := run(c, "umask 077 && cat > ~/.vpmon-rc", []byte(rcScript)); err != nil {
		return nil, errors.New("não consegui gravar o rc do shell: " + short(out+" "+err.Error()))
	}
	se, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := se.RequestPty("dumb", 50, 200, modes); err != nil {
		se.Close()
		return nil, errors.New("o servidor recusou o terminal (pty): " + err.Error())
	}
	stdin, err := se.StdinPipe()
	if err != nil {
		se.Close()
		return nil, err
	}
	stdout, err := se.StdoutPipe()
	if err != nil {
		se.Close()
		return nil, err
	}
	rc := strings.TrimRight(home, "/") + "/.vpmon-rc"
	if err := se.Start("exec bash --noediting --rcfile " + quote(rc) + " -i"); err != nil {
		se.Close()
		return nil, err
	}
	now := svc.now()
	sh := &Shell{ID: newID(), owner: owner, svc: svc, client: c, sess: se, stdin: stdin, rcPath: rc, opened: now,
		last: now, changed: make(chan struct{}), nextID: 1}
	go sh.read(stdout)
	go func() {
		se.Wait()
		sh.Close("o shell terminou")
	}()
	go sh.keepalive()
	for {
		sh.mu.Lock()
		ready, closed, ch := sh.ready, sh.closed, sh.changed
		sh.mu.Unlock()
		if ready {
			return sh, nil
		}
		if closed != "" {
			return nil, errors.New("o shell fechou ao abrir: " + closed)
		}
		select {
		case <-ch:
		case <-ctx.Done():
			sh.Close("não respondeu")
			return nil, ErrNotReady
		}
	}
}

// bump avisa quem espera (poll) que algo mudou. Com mu travado.
func (sh *Shell) bump() {
	sh.ver++
	close(sh.changed)
	sh.changed = make(chan struct{})
}

func (sh *Shell) keepalive() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		if !sh.Alive() {
			return
		}
		if _, _, err := sh.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			sh.Close("a conexão caiu")
			return
		}
	}
}

func (sh *Shell) read(r io.Reader) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sh.mu.Lock()
			sh.feed(buf[:n])
			sh.bump()
			sh.mu.Unlock()
		}
		if err == io.EOF {
			sh.Close("o shell terminou, por exemplo com exit")
			return
		}
		if err != nil {
			sh.Close("a conexão caiu")
			return
		}
	}
}

// feed separa a saída dos marcadores (que podem vir partidos entre leituras). Com mu travado.
func (sh *Shell) feed(data []byte) {
	buf := append(sh.pend, data...)
	sh.pend = nil
	for len(buf) > 0 {
		i := bytes.Index(buf, markStart)
		if i < 0 {
			k := partialPrefix(buf, markStart)
			sh.emit(buf[:len(buf)-k])
			if k > 0 {
				sh.pend = append([]byte{}, buf[len(buf)-k:]...)
			}
			return
		}
		sh.emit(buf[:i])
		j := bytes.IndexByte(buf[i:], '\a')
		if j < 0 {
			if len(buf)-i > 4096 { // não era marcador
				sh.emit(buf[i:])
				return
			}
			sh.pend = append([]byte{}, buf[i:]...)
			return
		}
		sh.marker(string(buf[i+len(markStart) : i+j]))
		buf = buf[i+j+1:]
	}
}

// partialPrefix: quantos bytes do fim de b são o começo de m.
func partialPrefix(b, m []byte) int {
	for k := len(m) - 1; k > 0; k-- {
		if len(b) >= k && bytes.Equal(b[len(b)-k:], m[:k]) {
			return k
		}
	}
	return 0
}

// emit põe saída do programa no bloco que está rodando (ou no último). Com mu travado.
func (sh *Shell) emit(b []byte) {
	if len(b) == 0 || !sh.ready || len(sh.blocks) == 0 {
		return // antes do primeiro prompt: o que o bash e o rc imprimem ao abrir
	}
	if bytes.IndexByte(b, inOpen) >= 0 || bytes.IndexByte(b, inClose) >= 0 {
		k := make([]byte, 0, len(b))
		for _, c := range b {
			if c != inOpen && c != inClose {
				k = append(k, c)
			}
		}
		b = k
	}
	sh.blocks[len(sh.blocks)-1].add(b)
}

func (b *Block) add(p []byte) {
	b.out = append(b.out, p...)
	if len(b.out) > maxOut+maxOut/4 {
		drop := len(b.out) - maxOut
		for k := 0; k < 3 && drop < len(b.out) && !utf8.RuneStart(b.out[drop]); k++ {
			drop++
		}
		b.base += int64(drop)
		b.out = append([]byte(nil), b.out[drop:]...)
	}
}

// marker trata "código;uid;pasta-em-base64". Com mu travado.
func (sh *Shell) marker(p string) {
	parts := strings.SplitN(p, ";", 3)
	if len(parts) != 3 {
		return
	}
	code, err1 := strconv.Atoi(parts[0])
	uid, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return
	}
	sh.uid = uid
	if cwd, err := base64.StdEncoding.DecodeString(parts[2]); err == nil && len(cwd) > 0 {
		sh.cwd = strings.TrimRight(string(cwd), "\n")
	}
	if !sh.ready {
		sh.ready = true
		return
	}
	if sh.busy && len(sh.blocks) > 0 {
		b := sh.blocks[len(sh.blocks)-1]
		b.End, b.Code = sh.svc.now().Unix(), &code
		sh.busy = false
		sh.svc.log.add(Entry{By: b.By, Kind: "end", Sess: sh.ID, Block: b.ID, Code: &code})
	}
}

// clean tira caracteres de controle (menos tab e quebra de linha).
func clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

func (sh *Shell) write(p string) error {
	sh.wmu.Lock()
	defer sh.wmu.Unlock()
	_, err := io.WriteString(sh.stdin, p)
	return err
}

// Run manda um comando (só com o shell livre).
func (sh *Shell) Run(by, ip, cmd string) (Block, error) {
	cmd = strings.TrimRight(clean(cmd), " \t\n")
	cmd = strings.TrimLeft(cmd, "\n")
	if strings.TrimSpace(cmd) == "" {
		return Block{}, ErrEmpty
	}
	if len(cmd) > maxCmd {
		return Block{}, ErrLong
	}
	send, note := cmd, ""
	if rootShell.MatchString(strings.Join(strings.Fields(cmd), " ")) {
		send, note = "sudo -H bash --noediting --rcfile "+quote(sh.rcPath)+" -i", "modo sudo"
	} else if strings.Contains(cmd, "\n") {
		send = "{\n" + cmd + "\n}"
	}
	sh.mu.Lock()
	switch {
	case sh.closed != "":
		sh.mu.Unlock()
		return Block{}, ErrClosed
	case !sh.ready:
		sh.mu.Unlock()
		return Block{}, ErrNotReady
	case sh.busy:
		sh.mu.Unlock()
		return Block{}, ErrBusy
	}
	now := sh.svc.now()
	b := &Block{ID: sh.nextID, Cmd: cmd, Note: note, By: by, Start: now.Unix(), UID: sh.uid, Cwd: sh.cwd}
	sh.nextID++
	sh.blocks = append(sh.blocks, b)
	if len(sh.blocks) > maxBlocks {
		sh.blocks = append([]*Block(nil), sh.blocks[len(sh.blocks)-maxBlocks:]...)
	}
	sh.busy, sh.last = true, now
	uid, cwd := sh.uid, sh.cwd
	sh.bump()
	view := *b
	sh.mu.Unlock()
	sh.svc.log.add(Entry{By: by, IP: ip, Kind: "cmd", Text: cmd, Sess: sh.ID, Block: b.ID, UID: &uid, Cwd: cwd})
	sh.svc.remember(sh.owner, cmd)
	if err := sh.write(send + "\n"); err != nil {
		sh.Close("a conexão caiu")
		return Block{}, ErrClosed
	}
	return view, nil
}

// Input manda uma linha para o programa que está rodando. Senha (secret)
// aparece como pontinhos e não vai para o registro.
func (sh *Shell) Input(by, ip, text string, secret bool) error {
	text = strings.ReplaceAll(clean(text), "\n", " ")
	if len(text) > maxCmd {
		return ErrLong
	}
	sh.mu.Lock()
	if sh.closed != "" {
		sh.mu.Unlock()
		return ErrClosed
	}
	if !sh.busy || len(sh.blocks) == 0 {
		sh.mu.Unlock()
		return ErrIdle
	}
	shown := text
	if secret {
		shown = "••••••"
	}
	b := sh.blocks[len(sh.blocks)-1]
	b.add([]byte(string(inOpen) + shown + string(inClose)))
	sh.last = sh.svc.now()
	id := b.ID
	sh.bump()
	sh.mu.Unlock()
	if secret {
		sh.svc.log.add(Entry{By: by, IP: ip, Kind: "input", Text: "(senha)", Sess: sh.ID, Block: id})
	} else {
		sh.svc.log.add(Entry{By: by, IP: ip, Kind: "input", Text: text, Sess: sh.ID, Block: id})
	}
	if err := sh.write(text + "\n"); err != nil {
		sh.Close("a conexão caiu")
		return ErrClosed
	}
	return nil
}

var keys = map[string]string{"ctrl-c": "\x03", "ctrl-d": "\x04", "esc": "\x1b"}

// Key manda uma tecla especial. Ctrl+D e Esc só com algo rodando (Ctrl+D no
// shell livre fecharia a sessão).
func (sh *Shell) Key(name string) error {
	seq, ok := keys[name]
	if !ok {
		return errors.New("tecla desconhecida")
	}
	sh.mu.Lock()
	if sh.closed != "" {
		sh.mu.Unlock()
		return ErrClosed
	}
	if !sh.busy && name != "ctrl-c" {
		sh.mu.Unlock()
		return ErrIdle
	}
	sh.last = sh.svc.now()
	sh.mu.Unlock()
	return sh.write(seq)
}

// Close fecha a sessão (o comando que rodava fica sem código).
func (sh *Shell) Close(reason string) {
	sh.mu.Lock()
	if sh.closed != "" {
		sh.mu.Unlock()
		return
	}
	sh.closed = reason
	sh.busy = false
	sh.bump()
	sh.mu.Unlock()
	sh.client.Close()
}

func (sh *Shell) Alive() bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.closed == ""
}

func (sh *Shell) usage() (last time.Time, busy bool, opened time.Time) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.last, sh.busy, sh.opened
}

// Touch conta como uso (a tela aberta com um comando rodando não deixa a sessão vencer).
func (sh *Shell) Touch() {
	sh.mu.Lock()
	if sh.busy {
		sh.last = sh.svc.now()
	}
	sh.mu.Unlock()
}

// --- o que a tela busca (long-poll) --------------------------------------------------------

type State struct {
	Session string `json:"session"`
	Ver     int64  `json:"ver"`
	Ready   bool   `json:"ready"`
	Busy    bool   `json:"busy"`
	UID     int    `json:"uid"`
	Cwd     string `json:"cwd"`
	Closed  string `json:"closed,omitempty"`
}

type BlockView struct {
	Block
	Base int64  `json:"base"` // bytes cortados do começo da saída
	From int64  `json:"from"` // de onde vai o Out
	To   int64  `json:"to"`   // até onde (o próximo pedido continua daqui)
	Out  string `json:"out"`
}

// Wait espera algo mudar depois de ver (ou ctx acabar).
func (sh *Shell) Wait(ctx context.Context, ver int64) {
	sh.mu.Lock()
	if sh.ver != ver {
		sh.mu.Unlock()
		return
	}
	ch := sh.changed
	sh.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// Snapshot devolve o estado e os blocos a partir do bloco b (desse, a saída a partir de o).
func (sh *Shell) Snapshot(b int, o int64) (State, []BlockView) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	st := State{Session: sh.ID, Ver: sh.ver, Ready: sh.ready, Busy: sh.busy, UID: sh.uid, Cwd: sh.cwd, Closed: sh.closed}
	out := []BlockView{}
	for i, bl := range sh.blocks {
		if bl.ID < b {
			continue
		}
		running := sh.busy && i == len(sh.blocks)-1
		from := bl.base
		if bl.ID == b && o > from {
			from = o
		}
		end := len(bl.out)
		if running {
			end = completeUTF8(bl.out)
		}
		start := int(from - bl.base)
		if start > end {
			start = end
		}
		out = append(out, BlockView{Block: *bl, Base: bl.base, From: bl.base + int64(start), To: bl.base + int64(end),
			Out: string(bl.out[start:end])})
	}
	return st, out
}

// completeUTF8: até onde b termina sem um caractere partido no fim.
func completeUTF8(b []byte) int {
	i := len(b) - 1
	for i >= 0 && len(b)-i < 4 && !utf8.RuneStart(b[i]) {
		i--
	}
	if i < 0 || utf8.FullRune(b[i:]) {
		return len(b)
	}
	return i
}

// Complete sugere o fim da palavra: comandos (cmd) ou arquivos e pastas da pasta atual.
func (sh *Shell) Complete(ctx context.Context, word, mode string) ([]string, error) {
	sh.mu.Lock()
	cwd, closed := sh.cwd, sh.closed
	sh.mu.Unlock()
	if closed != "" {
		return nil, ErrClosed
	}
	word = clean(strings.ReplaceAll(word, "\n", ""))
	if len(word) > 300 {
		return []string{}, nil
	}
	script := "cd -- " + quote(cwd) + " 2>/dev/null\n"
	if mode == "cmd" {
		script += "compgen -c -- " + quote(word) + " 2>/dev/null | sort -u | head -n 80\n"
	} else {
		script += "compgen -f -- " + quote(word) + ` 2>/dev/null | sort | head -n 80 | while IFS= read -r f; do if [ -d "$f" ]; then printf '%s/\n' "$f"; else printf '%s\n' "$f"; fi; done` + "\n"
	}
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := run(sh.client, "bash -s", []byte(script))
		ch <- res{out, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.out == "" {
			return []string{}, nil
		}
		items := []string{}
		for _, l := range strings.Split(r.out, "\n") {
			if l = strings.TrimRight(l, "\r"); l != "" {
				items = append(items, l)
			}
		}
		return items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
