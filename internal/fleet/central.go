package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

// O lado do central: os tokens que ele gerou (um por servidor conectado) e o
// último resumo de cada um. Fica em <data>/fleet-central.json (0600), com o
// token só como hash.

const (
	tokenPrefix   = "vps_"
	offlineAfter  = 3 * time.Minute // sem resumo há mais que isso: "sem notícias"
	minReportGap  = 15 * time.Second
	relayPerHour  = 30 // avisos emprestados por hora, por token
	maxTokens     = 50
	saveEvery     = 5 * time.Minute
	tokenNameSize = 40
)

var (
	ErrBadName   = errors.New("dê um nome de 1 a 40 caracteres ao servidor")
	ErrNoToken   = errors.New("token não encontrado")
	ErrTooMany   = fmt.Errorf("no máximo %d servidores conectados", maxTokens)
	ErrTooSoon   = errors.New("resumo cedo demais")
	ErrNoRelay   = errors.New("este token não pode usar o WhatsApp do painel central")
	ErrRelayRate = fmt.Errorf("passou de %d avisos por hora por este token", relayPerHour)
)

type Token struct {
	ID        string  `json:"id"` // curto, aparece na tela
	Name      string  `json:"name"`
	Hash      string  `json:"hash"`
	WhatsApp  bool    `json:"whatsapp"` // pode mandar avisos pelo WhatsApp do central
	Created   int64   `json:"created"`
	CreatedBy string  `json:"createdBy,omitempty"`
	LastSeen  int64   `json:"lastSeen,omitempty"`
	LastIP    string  `json:"lastIp,omitempty"`
	Report    *Report `json:"report,omitempty"`

	relays []int64 // horários dos avisos emprestados (limite por hora)
}

// TokenView é o que a tela vê (sem o hash).
type TokenView struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	WhatsApp  bool    `json:"whatsapp"`
	Created   int64   `json:"created"`
	CreatedBy string  `json:"createdBy,omitempty"`
	LastSeen  int64   `json:"lastSeen"`
	LastIP    string  `json:"lastIp,omitempty"`
	Online    bool    `json:"online"`
	Viewable  bool    `json:"viewable"` // compartilhou e está escutando: dá para ver aqui
	Logs      bool    `json:"logs"`     // compartilhou os logs
	Control   bool    `json:"control"`  // liberou o controle total (ações)
	Report    *Report `json:"report,omitempty"`
}

type Central struct {
	path  string
	now   func() time.Time
	views *views

	mu     sync.Mutex
	tokens []*Token
	dirty  bool
	saved  time.Time
}

func NewCentral(dataDir string) *Central {
	c := &Central{now: time.Now}
	c.views = newViews(func() time.Time { return c.now() })
	if dataDir != "" {
		c.path = filepath.Join(dataDir, "fleet-central.json")
		if b, err := os.ReadFile(c.path); err == nil {
			json.Unmarshal(b, &c.tokens)
		}
	}
	return c
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte("vpmon-fleet\x00" + strings.TrimSpace(t)))
	return hex.EncodeToString(sum[:])
}

func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (t *Token) view(now time.Time, listening bool) TokenView {
	v := TokenView{ID: t.ID, Name: t.Name, WhatsApp: t.WhatsApp, Created: t.Created, CreatedBy: t.CreatedBy,
		LastSeen: t.LastSeen, LastIP: t.LastIP, Report: t.Report,
		Online: t.LastSeen > 0 && now.Sub(time.Unix(t.LastSeen, 0)) < offlineAfter}
	if t.Report != nil && v.Online {
		sh := t.Report.share()
		v.Viewable, v.Logs, v.Control = sh.View && listening, sh.View && (sh.Logs || sh.Control), sh.View && sh.Control
	}
	return v
}

func (c *Central) listening(id string) bool {
	c.views.mu.Lock()
	defer c.views.mu.Unlock()
	return c.views.listening(id)
}

// Create gera um token novo (o texto só existe nesta resposta).
func (c *Central) Create(name, by string, whatsapp bool) (TokenView, string, error) {
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n == 0 || n > tokenNameSize {
		return TokenView{}, "", ErrBadName
	}
	plain := tokenPrefix + randomID(32)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tokens) >= maxTokens {
		return TokenView{}, "", ErrTooMany
	}
	t := &Token{ID: randomID(6), Name: name, Hash: hashToken(plain), WhatsApp: whatsapp, Created: c.now().Unix(), CreatedBy: by}
	c.tokens = append(c.tokens, t)
	c.dirty = true
	err := c.saveLocked(true)
	return t.view(c.now(), false), plain, err
}

// Revoke apaga um token: o servidor dele para de conseguir falar com o central.
func (c *Central) Revoke(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range c.tokens {
		if t.ID == id {
			c.tokens = append(c.tokens[:i], c.tokens[i+1:]...)
			c.dirty = true
			c.views.drop(id)
			return c.saveLocked(true)
		}
	}
	return ErrNoToken
}

// Auth acha o token de um "Authorization: Bearer ..." (comparação em tempo constante).
func (c *Central) Auth(header string) (TokenView, bool) {
	plain, ok := strings.CutPrefix(strings.TrimSpace(header), "Bearer ")
	if !ok || !strings.HasPrefix(plain, tokenPrefix) {
		return TokenView{}, false
	}
	h := hashToken(plain)
	c.mu.Lock()
	defer c.mu.Unlock()
	var found *Token
	for _, t := range c.tokens {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 {
			found = t
		}
	}
	if found == nil {
		return TokenView{}, false
	}
	return found.view(c.now(), c.listening(found.ID)), true
}

// Accept guarda o resumo que chegou de um servidor conectado.
func (c *Central) Accept(id string, r Report, ip string) error {
	r.clean()
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.find(id)
	if t == nil {
		return ErrNoToken
	}
	now := c.now()
	if t.LastSeen > 0 && now.Sub(time.Unix(t.LastSeen, 0)) < minReportGap {
		return ErrTooSoon
	}
	r.At = now.Unix() // vale o relógio do central
	t.Report, t.LastSeen, t.LastIP = &r, now.Unix(), ip
	c.dirty = true
	c.saveLocked(false)
	return nil
}

// Bye: o servidor se desconectou de propósito. O card volta a "aguardando
// conexão" e não vira alerta de silêncio (o token continua valendo).
func (c *Central) Bye(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.find(id)
	if t == nil {
		return ErrNoToken
	}
	t.LastSeen, t.Report = 0, nil
	c.dirty = true
	c.views.drop(id)
	return c.saveLocked(true)
}

// AllowRelay confere se o token pode mandar um aviso pelo WhatsApp agora.
func (c *Central) AllowRelay(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.find(id)
	if t == nil {
		return ErrNoToken
	}
	if !t.WhatsApp {
		return ErrNoRelay
	}
	now := c.now()
	keep := t.relays[:0]
	for _, x := range t.relays {
		if now.Sub(time.Unix(x, 0)) < time.Hour {
			keep = append(keep, x)
		}
	}
	t.relays = keep
	if len(t.relays) >= relayPerHour {
		return ErrRelayRate
	}
	t.relays = append(t.relays, now.Unix())
	return nil
}

func (c *Central) find(id string) *Token {
	for _, t := range c.tokens {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// List devolve os tokens (por nome) para a tela.
func (c *Central) List() []TokenView {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	out := make([]TokenView, 0, len(c.tokens))
	for _, t := range c.tokens {
		out = append(out, t.view(now, c.listening(t.ID)))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Alerts avisa (Infos e WhatsApp do central) de servidor conectado que parou
// de mandar resumo: é quando ele mesmo não consegue avisar ninguém. Roda com o
// monitor travado: só lê o que está em memória.
func (c *Central) Alerts() []monitor.Alert {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var out []monitor.Alert
	for _, t := range c.tokens {
		if t.LastSeen == 0 {
			continue // ainda não conectou: não é queda
		}
		if d := now.Sub(time.Unix(t.LastSeen, 0)); d >= offlineAfter {
			out = append(out, monitor.Alert{Key: "fleet.offline:" + t.ID, Level: "crit", Area: "monitor",
				Title:  fmt.Sprintf("O servidor %s parou de mandar notícias há %d min", t.Name, int(d.Minutes())),
				Detail: "Ele pode ter caído, perdido a internet ou ficado sem o painel. Confira o servidor (ou o painel dele, se abrir)."})
		}
	}
	return out
}

// Save grava se algo mudou (resumos: no máximo a cada 5 min).
func (c *Central) Save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveLocked(true)
}

func (c *Central) saveLocked(force bool) error {
	if c.path == "" || !c.dirty || (!force && time.Since(c.saved) < saveEvery) {
		return nil
	}
	b, _ := json.MarshalIndent(c.tokens, "", " ")
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.dirty, c.saved = false, time.Now()
	return nil
}
