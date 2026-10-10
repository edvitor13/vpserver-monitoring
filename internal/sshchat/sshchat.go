// Package sshchat é o SSH do servidor pela tela, em forma de chat: o painel
// entra por SSH de verdade (chave própria, do contêiner para o host) num
// usuário próprio do servidor e mantém um shell por pessoa do painel. Cada
// mensagem do chat é um comando; a saída volta aos pedaços.
//
// Desligado por padrão. Quem decide quem usa (administrador com 2FA, código
// pedido de novo para abrir a sessão) é a camada web; aqui ficam a chave, a
// conexão, o shell e o registro dos comandos.
package sshchat

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/ssh"
)

const (
	DefaultUser = "vpserver-ssh"
	DefaultHost = "host.docker.internal" // o host visto de dentro do contêiner (extra_hosts: host-gateway)
	DefaultPort = 22
	keyComment  = "vpserver-monitoring"

	dialTimeout = 8 * time.Second
	IdleAfter   = 30 * time.Minute // sessão parada fecha (e pede o código de novo)
	MaxSession  = 12 * time.Hour   // nem rodando um comando sem fim a sessão passa disso
	maxShells   = 3
	histKeep    = 200 // comandos lembrados por pessoa (autocompletar)
)

var (
	ErrDisabled   = errors.New("o SSH está desligado neste painel")
	ErrEnabled    = errors.New("desligue o SSH antes de mudar isso")
	ErrHostKey    = errors.New("a chave do servidor SSH mudou")
	ErrTooMany    = errors.New("há sessões de SSH demais abertas; feche uma")
	ErrNotReady   = errors.New("o shell não respondeu")
	errProbeAbort = errors.New("probe")
)

// fileData é o <data>/ssh.json (0600): a chave privada do painel nunca sai daqui.
type fileData struct {
	Enabled   bool                `json:"enabled"`
	User      string              `json:"user"`
	Host      string              `json:"host"`
	Port      int                 `json:"port"`
	Key       string              `json:"key"`               // chave privada do painel (PEM OpenSSH)
	HostKey   string              `json:"hostKey,omitempty"` // chave do servidor aceita ao ativar
	EnabledBy string              `json:"enabledBy,omitempty"`
	EnabledAt int64               `json:"enabledAt,omitempty"`
	History   map[string][]string `json:"history,omitempty"` // por pessoa do painel: últimos comandos
}

// Service guarda a configuração, os shells abertos e o registro.
type Service struct {
	path string
	log  *auditLog
	now  func() time.Time
	dial func(ctx context.Context, addr string) (net.Conn, error)
	nets func() []string // redes do contêiner (o from= da chave)

	mu     sync.Mutex
	f      fileData
	signer ssh.Signer
	shells map[string]*Shell // por pessoa do painel
}

// New carrega (ou cria) a configuração em dir. A chave do painel nasce aqui.
func New(dir string) (*Service, error) {
	s := &Service{path: filepath.Join(dir, "ssh.json"), log: newAuditLog(filepath.Join(dir, "ssh-log.jsonl")),
		now: time.Now, nets: containerNets, shells: map[string]*Shell{}}
	s.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: dialTimeout}
		return d.DialContext(ctx, "tcp", addr)
	}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.f); err != nil {
			return nil, fmt.Errorf("ssh.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.f.User == "" {
		s.f.User = DefaultUser
	}
	if s.f.Host == "" {
		s.f.Host = DefaultHost
	}
	if s.f.Port == 0 {
		s.f.Port = DefaultPort
	}
	if s.f.Key != "" {
		sg, err := ssh.ParsePrivateKey([]byte(s.f.Key))
		if err != nil {
			return nil, fmt.Errorf("ssh.json: chave do painel: %w", err)
		}
		s.signer = sg
	} else if err := s.newKeyLocked(); err != nil {
		return nil, err
	}
	return s, s.saveLocked()
}

func (s *Service) newKeyLocked() error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	blk, err := ssh.MarshalPrivateKey(priv, keyComment)
	if err != nil {
		return err
	}
	sg, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}
	s.f.Key, s.signer, s.f.HostKey = string(pem.EncodeToMemory(blk)), sg, ""
	return nil
}

func (s *Service) saveLocked() error {
	b, err := json.MarshalIndent(s.f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// --- o que a tela vê -------------------------------------------------------------------

type View struct {
	Enabled   bool     `json:"enabled"`
	User      string   `json:"user"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	PublicKey string   `json:"publicKey"` // a chave pública do painel (sem opções)
	AuthLine  string   `json:"authLine"`  // a linha do authorized_keys (com from= e restrict)
	Setup     string   `json:"setup"`     // comando para rodar uma vez no servidor
	Revoke    string   `json:"revoke"`    // comando que corta o acesso na hora
	Nets      []string `json:"nets"`
	HostKey   string   `json:"hostKey,omitempty"` // impressão digital aceita ao ativar
	EnabledBy string   `json:"enabledBy,omitempty"`
	EnabledAt int64    `json:"enabledAt,omitempty"`
	Shells    []string `json:"shells"` // quem está com sessão aberta
}

func (s *Service) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.signer.PublicKey()))) + " " + keyComment
	nets := s.nets()
	line := `from="` + strings.Join(nets, ",") + `",restrict,pty ` + pub
	v := View{Enabled: s.f.Enabled, User: s.f.User, Host: s.f.Host, Port: s.f.Port, PublicKey: pub, AuthLine: line,
		Setup: setupScript(s.f.User, line), Revoke: revokeScript(s.f.User), Nets: nets,
		EnabledBy: s.f.EnabledBy, EnabledAt: s.f.EnabledAt, Shells: []string{}}
	if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.f.HostKey)); err == nil {
		v.HostKey = ssh.FingerprintSHA256(k)
	}
	for who, sh := range s.shells {
		if sh.Alive() {
			v.Shells = append(v.Shells, who)
		}
	}
	sort.Strings(v.Shells)
	return v
}

var safeUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// setupScript cria o usuário (que vira root com sudo e a senha dele) e
// autoriza a chave do painel, só vinda da rede do contêiner, sem redirecionar
// porta, agente ou X11. O passwd fica fora do heredoc: precisa do terminal.
func setupScript(user, line string) string {
	return `sudo bash -s <<'VPMON'
set -e
U=` + user + `
id "$U" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash "$U"
G=sudo; getent group sudo >/dev/null || G=wheel
usermod -aG "$G" "$U"
H=$(getent passwd "$U" | cut -d: -f6)
install -d -m 700 -o "$U" -g "$U" "$H/.ssh"
K='` + line + `'
touch "$H/.ssh/authorized_keys"
sed -i '/ ` + keyComment + `$/d' "$H/.ssh/authorized_keys"
printf '%s\n' "$K" >> "$H/.ssh/authorized_keys"
chown "$U:$U" "$H/.ssh/authorized_keys"; chmod 600 "$H/.ssh/authorized_keys"
if sshd -T 2>/dev/null | grep -qiE '^(allowusers|allowgroups) '; then echo "Atenção: o sshd tem AllowUsers/AllowGroups; inclua $U lá."; fi
echo "Pronto. Agora defina a senha do sudo de $U:"
VPMON
sudo passwd ` + user
}

func revokeScript(user string) string {
	return `sudo sed -i '/ ` + keyComment + `$/d' ~` + user + `/.ssh/authorized_keys`
}

// containerNets são as redes IPv4 do contêiner (o from= da chave): o servidor
// só aceita a chave vinda delas.
func containerNets() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
			continue
		}
		nw := &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
		if s := nw.String(); !contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{"172.16.0.0/12", "192.168.0.0/16"}
	}
	sort.Strings(out)
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// SetTarget muda usuário, endereço e porta (só com o SSH desligado).
func (s *Service) SetTarget(user, host string, port int) error {
	user, host = strings.TrimSpace(user), strings.TrimSpace(host)
	if !safeUser.MatchString(user) {
		return errors.New("usuário inválido (letras minúsculas, números, - e _)")
	}
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " /@'\"") {
		return errors.New("endereço inválido")
	}
	if port < 1 || port > 65535 {
		return errors.New("porta inválida")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.Enabled {
		return ErrEnabled
	}
	if s.f.User != user || s.f.Host != host || s.f.Port != port {
		s.f.HostKey = ""
	}
	s.f.User, s.f.Host, s.f.Port = user, host, port
	return s.saveLocked()
}

// NewKey troca a chave do painel (só desligado): o comando de preparo muda.
func (s *Service) NewKey() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.Enabled {
		return ErrEnabled
	}
	if err := s.newKeyLocked(); err != nil {
		return err
	}
	return s.saveLocked()
}

// --- conexão -------------------------------------------------------------------------------

type target struct {
	user, addr string
	signer     ssh.Signer
	pinned     ssh.PublicKey // nil = aceita (e devolve) a que vier
	noLogin    bool          // só confere se o sshd responde: para antes de tentar entrar
}

func (s *Service) target(pin bool) target {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := target{user: s.f.User, addr: net.JoinHostPort(s.f.Host, strconv.Itoa(s.f.Port)), signer: s.signer}
	if pin {
		if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.f.HostKey)); err == nil {
			t.pinned = k
		}
	}
	return t
}

// connect entra no servidor. Com chave fixada, recusa outra.
func (s *Service) connect(ctx context.Context, t target) (*ssh.Client, ssh.PublicKey, error) {
	conn, err := s.dial(ctx, t.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("não consegui chegar em %s: %w", t.addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(20 * time.Second))
	}
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: t.user, Auth: []ssh.AuthMethod{ssh.PublicKeys(t.signer)}, Timeout: dialTimeout,
		ClientVersion: "SSH-2.0-vpserver-monitoring",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			got = k
			if t.pinned != nil && !bytes.Equal(k.Marshal(), t.pinned.Marshal()) {
				return ErrHostKey
			}
			if t.noLogin {
				return errProbeAbort
			}
			return nil
		},
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr, cfg)
	if err != nil {
		conn.Close()
		if errors.Is(err, ErrHostKey) {
			return nil, got, fmt.Errorf("%w (esperava %s, veio %s): se o servidor foi reinstalado, desligue e ative de novo",
				ErrHostKey, ssh.FingerprintSHA256(t.pinned), ssh.FingerprintSHA256(got))
		}
		return nil, got, err
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), got, nil
}

// Probe é o que a tela de ativação confere, passo a passo.
type Probe struct {
	Reachable  bool   `json:"reachable"`  // o sshd respondeu
	HostKey    string `json:"hostKey"`    // impressão digital do servidor
	HostKeyNew bool   `json:"hostKeyNew"` // difere da aceita antes
	Authorized bool   `json:"authorized"` // a chave do painel entrou
	Sudo       bool   `json:"sudo"`       // o usuário está no grupo do sudo
	Groups     string `json:"groups,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Probe confere o servidor. Sem login, só a troca de chaves (o sshd responde e
// mostra a chave dele), sem tentar entrar: abrir a tela várias vezes antes do
// preparo não vira tentativas de login falhas (fail2ban). Com login, entra com
// a chave do painel e lê os grupos do usuário.
func (s *Service) Probe(ctx context.Context, login bool) Probe {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	t := s.target(false)
	t.noLogin = !login
	pinned := s.target(true).pinned
	var p Probe
	c, hk, err := s.connect(ctx, t)
	if hk != nil {
		p.Reachable, p.HostKey = true, ssh.FingerprintSHA256(hk)
		p.HostKeyNew = pinned != nil && !bytes.Equal(hk.Marshal(), pinned.Marshal())
	}
	if !login {
		if hk == nil && err != nil {
			p.Error = short(err.Error())
		}
		return p
	}
	if err != nil {
		if hk != nil {
			p.Error = "O servidor SSH respondeu, mas não aceitou a chave do painel: rode o comando do passo 1."
		} else {
			p.Error = short(err.Error())
		}
		return p
	}
	defer c.Close()
	p.Authorized = true
	out, err := run(c, "id -Gn", nil)
	if err != nil {
		p.Error = "Entrou, mas não consegui ler os grupos do usuário: " + short(err.Error())
		return p
	}
	p.Groups = strings.TrimSpace(out)
	for _, g := range strings.Fields(p.Groups) {
		if g == "sudo" || g == "wheel" || g == "admin" {
			p.Sudo = true
		}
	}
	return p
}

// run roda um comando avulso (sem pty) e devolve a saída.
func run(c *ssh.Client, cmd string, stdin []byte) (string, error) {
	se, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer se.Close()
	if stdin != nil {
		se.Stdin = bytes.NewReader(stdin)
	}
	var out lockedBuffer // stdout e stderr chegam em goroutines separadas
	se.Stdout, se.Stderr = &out, &out
	err = se.Run(cmd)
	return out.String(), err
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Enable liga o SSH: a chave do painel tem de entrar, e a chave do servidor
// vista agora passa a ser a única aceita.
func (s *Service) Enable(ctx context.Context, by string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, hk, err := s.connect(ctx, s.target(false))
	if err != nil {
		if hk != nil {
			return errors.New("o servidor SSH respondeu, mas não aceitou a chave do painel: rode o comando do passo 1")
		}
		return err
	}
	c.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Enabled, s.f.EnabledBy, s.f.EnabledAt = true, by, s.now().Unix()
	s.f.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hk)))
	return s.saveLocked()
}

// Disable desliga e fecha todas as sessões.
func (s *Service) Disable() error {
	s.mu.Lock()
	s.f.Enabled = false
	shells := s.shells
	s.shells = map[string]*Shell{}
	err := s.saveLocked()
	s.mu.Unlock()
	for _, sh := range shells {
		sh.Close("o SSH foi desligado")
	}
	return err
}

func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Enabled
}

// --- shells ----------------------------------------------------------------------------------

// Shell devolve o shell aberto de alguém (nil = nenhum).
func (s *Service) Shell(owner string) *Shell {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh := s.shells[owner]; sh != nil && sh.Alive() {
		return sh
	}
	return nil
}

// Open devolve o shell de alguém, abrindo um novo se preciso.
func (s *Service) Open(ctx context.Context, owner string) (*Shell, error) {
	s.mu.Lock()
	if !s.f.Enabled {
		s.mu.Unlock()
		return nil, ErrDisabled
	}
	if sh := s.shells[owner]; sh != nil && sh.Alive() {
		s.mu.Unlock()
		return sh, nil
	}
	n := 0
	for _, sh := range s.shells {
		if sh.Alive() {
			n++
		}
	}
	s.mu.Unlock()
	if n >= maxShells {
		return nil, ErrTooMany
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	c, _, err := s.connect(ctx, s.target(true))
	if err != nil {
		return nil, err
	}
	sh, err := startShell(ctx, c, owner, s)
	if err != nil {
		c.Close()
		return nil, err
	}
	s.mu.Lock()
	if old := s.shells[owner]; old != nil {
		old.Close("outra sessão abriu")
	}
	s.shells[owner] = sh
	s.mu.Unlock()
	return sh, nil
}

// CloseShell fecha o shell de alguém (botão Encerrar ou fim da sessão).
func (s *Service) CloseShell(owner, reason string) {
	s.mu.Lock()
	sh := s.shells[owner]
	delete(s.shells, owner)
	s.mu.Unlock()
	if sh != nil {
		sh.Close(reason)
	}
}

// Reap fecha shells parados há mais de IdleAfter (ou abertos há mais de MaxSession).
func (s *Service) Reap() {
	now := s.now()
	s.mu.Lock()
	var stale []*Shell
	for who, sh := range s.shells {
		if !sh.Alive() {
			delete(s.shells, who)
			continue
		}
		last, busy, opened := sh.usage()
		if (!busy && now.Sub(last) > IdleAfter) || now.Sub(opened) > MaxSession {
			delete(s.shells, who)
			stale = append(stale, sh)
		}
	}
	s.mu.Unlock()
	for _, sh := range stale {
		sh.Close("fechada depois de 30 min parada")
		s.log.add(Entry{By: sh.owner, Kind: "close", Text: "Sessão fechada por inatividade", Sess: sh.ID})
	}
}

// Run fecha sessões paradas a cada minuto, até ctx acabar.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reap()
		}
	}
}

// --- histórico (autocompletar) e registro -------------------------------------------------

func (s *Service) remember(owner, cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || len(cmd) > 500 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.History == nil {
		s.f.History = map[string][]string{}
	}
	h := s.f.History[owner]
	for i, c := range h {
		if c == cmd {
			h = append(h[:i], h[i+1:]...)
			break
		}
	}
	h = append(h, cmd)
	if len(h) > histKeep {
		h = h[len(h)-histKeep:]
	}
	s.f.History[owner] = h
	s.saveLocked()
}

// History são os últimos comandos de alguém (mais novo por último).
func (s *Service) History(owner string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.f.History[owner]...)
}

// Log devolve o registro (mais novo primeiro).
func (s *Service) Log(n int) []Entry { return s.log.last(n) }

// Note grava algo no registro (ligar, desligar, abrir sessão, código errado).
func (s *Service) Note(e Entry) { s.log.add(e) }

func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
