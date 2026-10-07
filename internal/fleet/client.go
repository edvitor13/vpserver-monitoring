package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// O lado do servidor conectado: endereço e token do central, em
// <data>/fleet-remote.json (0600). A cada minuto manda o resumo; com o
// WhatsApp emprestado ligado, os avisos daqui saem pelo central.

const (
	reportEvery = time.Minute
	httpTimeout = 20 * time.Second
	maxBody     = 64 << 10
)

// Fleet junta os dois lados (um painel pode ser central e estar conectado a outro).
type Fleet struct {
	Central *Central
	Client  *Client
}

type clientConfig struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	UseWhatsApp bool   `json:"useWhatsApp"`
	ShareView   bool   `json:"shareView"` // o central pode ver este servidor (só leitura)
	ShareLogs   bool   `json:"shareLogs"`
	ShareCtl    bool   `json:"shareControl"`
	Since       int64  `json:"since"`
}

// ClientStatus é o que a tela vê da conexão com o central.
type ClientStatus struct {
	Connected       bool   `json:"connected"` // há central configurado
	URL             string `json:"url,omitempty"`
	Since           int64  `json:"since,omitempty"`
	OK              bool   `json:"ok"` // o último resumo foi aceito
	Error           string `json:"error,omitempty"`
	LastAt          int64  `json:"lastAt,omitempty"`
	Central         string `json:"central,omitempty"` // nome do painel central
	CanWhatsApp     bool   `json:"canWhatsApp"`       // o token pode usar o WhatsApp do central
	CentralWhatsApp bool   `json:"centralWhatsApp"`   // o WhatsApp do central está pronto (conectado e com destinos)
	UseWhatsApp     bool   `json:"useWhatsApp"`       // os avisos daqui saem pelo central
	ShareView       bool   `json:"shareView"`         // o central pode ver este servidor
	ShareLogs       bool   `json:"shareLogs"`         // ... inclusive os logs
	ShareControl    bool   `json:"shareControl"`      // ... e fazer as ações
	Listening       bool   `json:"listening"`         // o pedido aberto no central está de pé
	Version         string `json:"version,omitempty"` // do central
}

// reportReply é a resposta do central a um resumo.
type reportReply struct {
	Central  string `json:"central"`
	Version  string `json:"version"`
	WhatsApp struct {
		Allowed bool `json:"allowed"`
		Ready   bool `json:"ready"`
	} `json:"whatsapp"`
}

type Client struct {
	path    string
	build   func() Report
	hc      *http.Client
	pollHC  *http.Client // pedido aberto no central: espera até 25 s
	now     func() time.Time
	version string
	local   func(ViewRequest) (int, string, []byte) // executa aqui um pedido do central
	wake    chan struct{}                           // compartilhamento mudou: acorda o laço
	cancel  context.CancelFunc                      // cancela o pedido aberto (para valer o compartilhamento novo)

	mu  sync.Mutex
	cfg clientConfig
	st  ClientStatus
}

// NewClient carrega a conexão salva. build monta o resumo deste painel.
func NewClient(dataDir, version string, build func() Report) *Client {
	c := &Client{build: build, hc: &http.Client{Timeout: httpTimeout}, pollHC: &http.Client{Timeout: pollWait + 15*time.Second},
		now: time.Now, version: version, wake: make(chan struct{}, 1)}
	if dataDir != "" {
		c.path = filepath.Join(dataDir, "fleet-remote.json")
		if b, err := os.ReadFile(c.path); err == nil {
			json.Unmarshal(b, &c.cfg)
		}
	}
	c.syncStatusLocked()
	return c
}

func (c *Client) syncStatusLocked() {
	c.st.Connected, c.st.URL, c.st.Since, c.st.UseWhatsApp = c.cfg.URL != "", c.cfg.URL, c.cfg.Since, c.cfg.UseWhatsApp
	c.st.ShareView, c.st.ShareLogs, c.st.ShareControl = c.cfg.ShareView, c.cfg.ShareView && c.cfg.ShareLogs, c.cfg.ShareView && c.cfg.ShareCtl
}

// NormalizeURL aceita "painel.exemplo.com", "https://painel.exemplo.com/qualquer"
// ou "203.0.113.5:8080" e devolve só esquema + endereço (sem caminho).
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("informe o endereço do painel central")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", errors.New("endereço inválido (ex.: https://painel.exemplo.com)")
	}
	host := u.Hostname()
	if strings.ContainsAny(host, " /\\") || (net.ParseIP(host) == nil && !strings.Contains(host, ".") && host != "localhost") {
		return "", errors.New("endereço inválido (ex.: https://painel.exemplo.com)")
	}
	return u.Scheme + "://" + u.Host, nil
}

// report é o resumo daqui com o que está compartilhado.
func (c *Client) report(cfg clientConfig) Report {
	r := c.build()
	r.setShare(cfg.share())
	return r
}

// post manda JSON ao central com o token e lê a resposta (ou o erro da API).
func (c *Client) post(ctx context.Context, base, token, path string, body, out any) error {
	return c.postWith(c.hc, ctx, base, token, path, body, out)
}

func (c *Client) postWith(hc *http.Client, ctx context.Context, base, token, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "vpserver-monitoring/"+c.version)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("o painel central não respondeu: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(raw, &e)
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return errors.New("o painel central não aceitou o token (revogado ou digitado errado)")
		case resp.StatusCode == http.StatusNotFound:
			return errors.New("o endereço não é de um painel VPServer atualizado")
		case e.Error.Message != "":
			return errors.New(e.Error.Message)
		}
		return fmt.Errorf("o painel central respondeu %d", resp.StatusCode)
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return errors.New("resposta estranha do painel central")
	}
	return nil
}

// Connect testa endereço + token mandando um resumo e, se der certo, grava.
func (c *Client) Connect(ctx context.Context, rawURL, token string) (ClientStatus, error) {
	base, err := NormalizeURL(rawURL)
	if err != nil {
		return ClientStatus{}, err
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, tokenPrefix) {
		return ClientStatus{}, errors.New("cole o token gerado no painel central (começa com vps_)")
	}
	var rep reportReply
	if err := c.post(ctx, base, token, "/api/fleet/report", c.report(clientConfig{}), &rep); err != nil && !strings.Contains(err.Error(), "cedo demais") {
		return ClientStatus{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = clientConfig{URL: base, Token: token, Since: c.now().Unix()}
	if err := c.saveLocked(); err != nil {
		return ClientStatus{}, err
	}
	c.syncStatusLocked()
	c.applyLocked(rep, nil)
	return c.st, nil
}

// Disconnect avisa o central (para ele não acusar silêncio) e esquece a
// conexão. O token continua valendo lá até ser revogado.
func (c *Client) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg.URL != "" {
		bc, cancel := context.WithTimeout(ctx, 8*time.Second)
		c.post(bc, cfg.URL, cfg.Token, "/api/fleet/bye", map[string]any{}, nil) // sem resposta: o central acusa silêncio
		cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg, c.st = clientConfig{}, ClientStatus{}
	if c.path == "" {
		return nil
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SetUseWhatsApp liga ou desliga o WhatsApp emprestado do central.
func (c *Client) SetUseWhatsApp(on bool) (ClientStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.URL == "" {
		return c.st, errors.New("conecte a um painel central primeiro")
	}
	if on && !c.st.CanWhatsApp {
		return c.st, errors.New("o token deste servidor não pode usar o WhatsApp do central (gere outro, com essa opção marcada)")
	}
	c.cfg.UseWhatsApp = on
	if err := c.saveLocked(); err != nil {
		return c.st, err
	}
	c.syncStatusLocked()
	return c.st, nil
}

func (c *Client) Status() ClientStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *Client) applyLocked(rep reportReply, err error) {
	c.st.LastAt = c.now().Unix()
	if err != nil {
		c.st.OK, c.st.Error = false, err.Error()
		return
	}
	c.st.OK, c.st.Error, c.st.Central, c.st.Version = true, "", rep.Central, rep.Version
	c.st.CanWhatsApp, c.st.CentralWhatsApp = rep.WhatsApp.Allowed, rep.WhatsApp.Ready
}

// Report manda o resumo agora (o Run chama a cada minuto).
func (c *Client) Report(ctx context.Context) {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg.URL == "" {
		return
	}
	var rep reportReply
	err := c.post(ctx, cfg.URL, cfg.Token, "/api/fleet/report", c.report(cfg), &rep)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.URL != cfg.URL { // desconectou no meio
		return
	}
	if err != nil && strings.Contains(err.Error(), "cedo demais") {
		return // o central já tem um resumo recente (ex.: o da conexão): não é erro
	}
	if err != nil && c.st.OK {
		slog.Warn("painel central: resumo não foi", "central", cfg.URL, "err", err)
	}
	c.applyLocked(rep, err)
}

// Run manda o resumo a cada minuto (o primeiro logo depois de subir).
func (c *Client) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	for {
		rc, cancel := context.WithTimeout(ctx, httpTimeout)
		c.Report(rc)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- ver este servidor no central (só leitura) --------------------------------------------

func (cfg clientConfig) share() Share {
	return Share{View: cfg.ShareView, Logs: cfg.ShareView && cfg.ShareLogs, Control: cfg.ShareView && cfg.ShareCtl}
}

// SetLocal liga a execução dos pedidos do central (a tela daqui).
func (c *Client) SetLocal(fn func(ViewRequest) (int, string, []byte)) { c.local = fn }

// SetShare muda o que o central pode ver (e fazer) neste servidor.
func (c *Client) SetShare(sh Share) (ClientStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.URL == "" {
		return c.st, errors.New("conecte a um painel central primeiro")
	}
	sh = Share{View: sh.View, Logs: sh.View && sh.Logs, Control: sh.View && sh.Control}
	c.cfg.ShareView, c.cfg.ShareLogs, c.cfg.ShareCtl = sh.View, sh.Logs, sh.Control
	if err := c.saveLocked(); err != nil {
		return c.st, err
	}
	c.syncStatusLocked()
	select { // acorda o laço: liga o pedido aberto ou avisa o central que desligou
	case c.wake <- struct{}{}:
	default:
	}
	if c.cancel != nil {
		c.cancel() // o pedido aberto ainda leva o compartilhamento antigo: refaz com o novo
	}
	return c.st, nil
}

// pollReply é o que o central devolve num Poll.
type pollReply struct {
	Requests []ViewRequest `json:"requests"`
}

// RunViews deixa um pedido aberto no central enquanto o compartilhamento está
// ligado, executa aqui as leituras que chegam (lista fechada) e devolve.
func (c *Client) RunViews(ctx context.Context) {
	fails := 0
	for {
		c.mu.Lock()
		cfg := c.cfg
		c.mu.Unlock()
		if cfg.URL == "" || !cfg.ShareView || c.local == nil {
			if c.Status().Listening && cfg.URL != "" { // acabou de desligar: avisa o central na hora
				sc, cancel := context.WithTimeout(ctx, httpTimeout)
				c.post(sc, cfg.URL, cfg.Token, "/api/fleet/poll", map[string]bool{"shareView": false}, nil)
				cancel()
			}
			c.setListening(false)
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
			case <-time.After(10 * time.Second):
			}
			continue
		}
		var pr pollReply
		pctx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.cancel = cancel
		c.mu.Unlock()
		err := c.postWith(c.pollHC, pctx, cfg.URL, cfg.Token, "/api/fleet/poll",
			map[string]bool{"shareView": true, "shareLogs": cfg.ShareLogs, "shareControl": cfg.ShareCtl}, &pr)
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
		cancelled := pctx.Err() != nil
		cancel()
		if ctx.Err() != nil {
			return
		}
		if cancelled { // compartilhamento mudou no meio: refaz já com o novo
			continue
		}
		if err != nil {
			c.setListening(false)
			fails++
			wait := time.Duration(min(fails, 6)) * 10 * time.Second // até 1 min entre tentativas
			if fails == 1 {
				slog.Warn("painel central: pedido aberto falhou", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		fails = 0
		c.setListening(true)
		for _, r := range pr.Requests {
			go c.answer(ctx, r)
		}
	}
}

func (c *Client) setListening(on bool) {
	c.mu.Lock()
	c.st.Listening = on
	c.mu.Unlock()
}

// answer executa uma leitura aqui e devolve ao central.
func (c *Client) answer(ctx context.Context, r ViewRequest) {
	c.mu.Lock()
	cfg := c.cfg // o que vale agora (pode ter mudado durante o pedido aberto)
	c.mu.Unlock()
	if cfg.URL == "" {
		return
	}
	rep := ViewReply{ID: r.ID, Status: 403, Type: "application/json",
		Body: []byte(`{"error":{"code":"not_shared","message":"Este servidor não compartilhou isso com o painel central."}}`)}
	sh := cfg.share()
	if !sh.Control { // as permissões de quem pediu só valem com o controle total
		r.Actor.Actions, r.Actor.Clean = false, false
	}
	if RemoteAllowed(r.Method, r.Path, sh) {
		rep.Status, rep.Type, rep.Body = c.local(r)
	}
	if len(rep.Body) > MaxReply {
		rep.Status, rep.Body = 502, []byte(`{"error":{"code":"too_big","message":"Resposta grande demais para mandar pelo painel central."}}`)
	}
	rc, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	c.post(rc, cfg.URL, cfg.Token, "/api/fleet/reply", rep, nil)
}

// --- WhatsApp emprestado (o notify usa o Client como Relay) -----------------------------------

// Active: os avisos daqui saem pelo WhatsApp do central (ligado aqui, o token
// pode e o central disse no último resumo que está pronto).
func (c *Client) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.URL != "" && c.cfg.UseWhatsApp && c.st.CanWhatsApp && c.st.CentralWhatsApp
}

func (c *Client) Central() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.Central
}

// Send pede ao central para mandar o texto (aos destinos dele).
func (c *Client) Send(ctx context.Context, text string) error {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg.URL == "" {
		return errors.New("não está conectado a um painel central")
	}
	return c.post(ctx, cfg.URL, cfg.Token, "/api/fleet/notify", map[string]string{"text": text}, nil)
}

func (c *Client) saveLocked() error {
	if c.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(c.cfg, "", " ")
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
