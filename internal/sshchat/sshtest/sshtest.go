// Package sshtest é um servidor SSH falso para os testes: entra com a chave
// autorizada e imita o bash com o rc do chat de SSH (marcadores no lugar do
// prompt), com alguns comandos de mentira (echo, cd, sleep, askpass, sudo...).
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/xcrypto/ssh"
)

// Server é o servidor falso.
type Server struct {
	ln       net.Listener
	hostKey  ssh.Signer
	mu       sync.Mutex
	allowed  ssh.PublicKey // a chave que entra (nil = nenhuma)
	rc       string        // o que o painel gravou em ~/.vpmon-rc
	commands []string      // exec recebidos
	attempts int           // tentativas de entrar com chave
}

// Attempts conta as tentativas de entrar (com qualquer chave).
func (f *Server) Attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// New sobe o servidor falso numa porta qualquer (fecha no fim do teste).
func New(t testing.TB) *Server {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewSignerFromKey(priv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &Server{ln: ln, hostKey: hk}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

// Port é a porta em 127.0.0.1.
func (f *Server) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// Allow faz a chave k entrar.
func (f *Server) Allow(k ssh.PublicKey) {
	f.mu.Lock()
	f.allowed = k
	f.mu.Unlock()
}

func (f *Server) serve() {
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attempts++
		if f.allowed != nil && bytes.Equal(k.Marshal(), f.allowed.Marshal()) {
			return &ssh.Permissions{}, nil
		}
		return nil, fmt.Errorf("chave recusada")
	}}
	cfg.AddHostKey(f.hostKey)
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			_, chans, reqs, err := ssh.NewServerConn(c, cfg)
			if err != nil {
				c.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			for nc := range chans {
				if nc.ChannelType() != "session" {
					nc.Reject(ssh.UnknownChannelType, "")
					continue
				}
				ch, creqs, err := nc.Accept()
				if err != nil {
					continue
				}
				go f.session(ch, creqs)
			}
		}()
	}
}

func exit(ch ssh.Channel, code int) {
	ch.CloseWrite() // como o sshd: EOF, exit-status e só então fecha
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
	ch.Close()
}

func (f *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			req.Reply(true, nil)
		case "exec":
			var p struct{ Command string }
			ssh.Unmarshal(req.Payload, &p)
			req.Reply(true, nil)
			f.mu.Lock()
			f.commands = append(f.commands, p.Command)
			f.mu.Unlock()
			go f.exec(ch, p.Command)
		default:
			req.Reply(false, nil)
		}
	}
}

func (f *Server) exec(ch ssh.Channel, cmd string) {
	switch {
	case cmd == `printf %s "$HOME"`:
		io.WriteString(ch, "/home/tester")
		exit(ch, 0)
	case cmd == "umask 077 && cat > ~/.vpmon-rc":
		b, _ := io.ReadAll(ch)
		f.mu.Lock()
		f.rc = string(b)
		f.mu.Unlock()
		exit(ch, 0)
	case cmd == "id -Gn":
		io.WriteString(ch, "tester sudo docker\n")
		exit(ch, 0)
	case cmd == "bash -s":
		b, _ := io.ReadAll(ch)
		if strings.Contains(string(b), "compgen -c") {
			io.WriteString(ch, "docker\ndocker-compose\n")
		} else {
			io.WriteString(ch, "app/\napp.log\n")
		}
		exit(ch, 0)
	case strings.HasPrefix(cmd, "exec bash --noediting --rcfile '/home/tester/.vpmon-rc' -i"):
		fakeBash(ch)
	default:
		exit(ch, 127)
	}
}

// Mark é o marcador que o rc do chat imprime no lugar do prompt.
func Mark(code, uid int, cwd string) string {
	return fmt.Sprintf("\x1b]777;vpmon;%d;%d;%s\a", code, uid, base64.StdEncoding.EncodeToString([]byte(cwd+"\n")))
}

// fakeBash entende alguns comandos e responde como o bash com o rc do chat.
func fakeBash(ch ssh.Channel) {
	in := make(chan string, 64) // linhas; "\x03"/"\x04" chegam sozinhos
	go func() {
		defer close(in)
		var line []byte
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			for _, c := range buf[:n] {
				switch c {
				case 3, 4:
					in <- string(c)
				case '\n':
					in <- string(line)
					line = nil
				default:
					line = append(line, c)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	cwd, uids := "/home/tester", []int{1000}
	uid := func() int { return uids[len(uids)-1] }
	io.WriteString(ch, "Welcome to Fake Linux\nbash-5.2$ ")
	io.WriteString(ch, Mark(0, uid(), cwd))
	var block []string
	for l := range in {
		if block != nil { // { ... } de várias linhas
			if l == "}" {
				for _, b := range block {
					io.WriteString(ch, strings.TrimPrefix(b, "echo ")+"\r\n")
				}
				block = nil
				io.WriteString(ch, Mark(0, uid(), cwd))
			} else {
				block = append(block, l)
			}
			continue
		}
		switch {
		case l == "\x03":
			io.WriteString(ch, Mark(130, uid(), cwd))
		case l == "\x04":
		case l == "{":
			block = []string{}
		case strings.HasPrefix(l, "echo "):
			io.WriteString(ch, l[5:]+"\r\n"+Mark(0, uid(), cwd))
		case strings.HasPrefix(l, "cd "):
			cwd = l[3:]
			io.WriteString(ch, Mark(0, uid(), cwd))
		case l == "false":
			io.WriteString(ch, Mark(1, uid(), cwd))
		case l == "sleep":
			for k := range in {
				if k == "\x03" {
					io.WriteString(ch, "^C"+Mark(130, uid(), cwd))
					break
				}
			}
		case l == "askpass":
			io.WriteString(ch, "Password: ")
			pw := <-in
			io.WriteString(ch, "\r\ngot "+strconv.Itoa(len(pw))+"\r\n"+Mark(0, uid(), cwd))
		case l == "sudo -H bash --noediting --rcfile '/home/tester/.vpmon-rc' -i":
			io.WriteString(ch, "[sudo] password for tester: ")
			if pw := <-in; pw == "certa" {
				uids = append(uids, 0)
				io.WriteString(ch, "\r\n"+Mark(0, uid(), "/root"))
				cwd = "/root"
			} else {
				io.WriteString(ch, "\r\nsudo: 1 incorrect password attempt\r\n"+Mark(1, uid(), cwd))
			}
		case l == "exit":
			if len(uids) > 1 {
				uids = uids[:len(uids)-1]
				cwd = "/home/tester"
				io.WriteString(ch, "exit\r\n"+Mark(0, uid(), cwd))
			} else {
				exit(ch, 0)
				return
			}
		case l == "big":
			var b strings.Builder
			for i := 0; b.Len() < 200<<10; i++ {
				fmt.Fprintf(&b, "linha %d — ação\n", i)
			}
			io.WriteString(ch, b.String()+Mark(0, uid(), cwd))
		case l == "split":
			m := Mark(0, uid(), cwd)
			io.WriteString(ch, "abc"+m[:5])
			time.Sleep(30 * time.Millisecond)
			io.WriteString(ch, m[5:12])
			time.Sleep(30 * time.Millisecond)
			io.WriteString(ch, m[12:])
		case l == "noise":
			io.WriteString(ch, "a\x1eb\x1fc\r\n"+Mark(0, uid(), cwd))
		default:
			io.WriteString(ch, "bash: "+l+": command not found\r\n"+Mark(127, uid(), cwd))
		}
	}
}

// HostKey é a chave do servidor falso.
func (f *Server) HostKey() ssh.PublicKey { return f.hostKey.PublicKey() }

// RC é o que o painel gravou em ~/.vpmon-rc.
func (f *Server) RC() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rc
}
