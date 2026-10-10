package sshchat

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Registro do SSH em <data>/ssh-log.jsonl (0600, uma linha por evento; passando
// de 2 MB vira .1): quem ligou, abriu sessão, cada comando com o código de
// saída e cada entrada digitada (senha nunca: só "(senha)"). A saída dos
// comandos não entra.

const logRotate = 2 << 20

type Entry struct {
	T     int64  `json:"t"`
	By    string `json:"by"`
	IP    string `json:"ip,omitempty"`
	Kind  string `json:"kind"` // enable, disable, open, close, fail, cmd, input, end
	Text  string `json:"text,omitempty"`
	Sess  string `json:"sess,omitempty"`
	Block int    `json:"block,omitempty"`
	Code  *int   `json:"code,omitempty"`
	UID   *int   `json:"uid,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
}

type auditLog struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

func newAuditLog(path string) *auditLog { return &auditLog{path: path, now: time.Now} }

func (l *auditLog) add(e Entry) {
	if e.T == 0 {
		e.T = l.now().Unix()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, err := os.Stat(l.path); err == nil && st.Size() > logRotate {
		os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// last devolve os n eventos mais novos (primeiro o mais novo); o código de
// saída ("end") vai junto do comando.
func (l *auditLog) last(n int) []Entry {
	l.mu.Lock()
	var all []Entry
	for _, p := range []string{l.path + ".1", l.path} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				all = append(all, e)
			}
		}
		f.Close()
	}
	l.mu.Unlock()
	type key struct {
		sess  string
		block int
	}
	codes := map[key]*int{}
	for _, e := range all {
		if e.Kind == "end" {
			codes[key{e.Sess, e.Block}] = e.Code
		}
	}
	out := []Entry{}
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		e := all[i]
		if e.Kind == "end" {
			continue
		}
		if e.Kind == "cmd" {
			e.Code = codes[key{e.Sess, e.Block}]
		}
		out = append(out, e)
	}
	return out
}
