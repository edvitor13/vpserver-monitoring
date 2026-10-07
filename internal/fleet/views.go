package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// Ver um servidor conectado dentro do painel central.
//
// O central nunca abre conexão com o servidor conectado: quem tem o
// compartilhamento ligado deixa um pedido aberto no central (Poll, até 25 s).
// Quando a tela do central quer dados dele (Ask), o pedido entra na fila desse
// servidor e sai pelo próximo Poll; ele executa localmente (só leitura, lista
// fechada) e devolve com Reply.

const (
	listenWithin = 40 * time.Second // fez Poll há pouco: está escutando
	queueSize    = 32
	pollBatch    = 8
	MaxReply     = 8 << 20
)

// Esperas (variáveis só para os testes encurtarem).
var (
	pollWait = 25 * time.Second
	askWait  = 20 * time.Second
)

var (
	ErrNotListening = errors.New("o servidor não está escutando o painel central agora")
	ErrQueueFull    = errors.New("muitos pedidos para este servidor: tente de novo")
	ErrNoAnswer     = errors.New("o servidor não respondeu a tempo")
	ErrNotAllowed   = errors.New("o servidor não compartilhou isso com o painel central")
)

// Actor é quem pediu, no painel central, e o que pode lá (o servidor conectado
// só aproveita as permissões se tiver liberado o controle total).
type Actor struct {
	Name    string `json:"name"`
	Actions bool   `json:"actions"`
	Clean   bool   `json:"clean"`
}

// ViewRequest é um pedido que desce para o servidor conectado.
type ViewRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"` // GET (ver) ou POST (controle total)
	Path   string `json:"path"`   // ex.: /api/overview
	Query  string `json:"query"`  // sem o "?"
	Body   []byte `json:"body,omitempty"`
	Actor  Actor  `json:"actor"`
}

// ViewReply é a resposta que sobe (Body vai em base64 no JSON).
type ViewReply struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Type   string `json:"type"`
	Body   []byte `json:"body"`
}

// RemotePaths são as leituras que dá para fazer à distância (a lista vale
// nos dois lados). Os de log só com "Incluir os logs".
var RemotePaths = map[string]bool{
	"/api/overview": false, "/api/history/host": false, "/api/history/apps": false, "/api/history/unit": false,
	"/api/traffic": false, "/api/system": false, "/api/cleanup": false,
	"/api/logs/targets": true, "/api/logs": true,
}

// RemoteWrites são as ações que dá para fazer à distância com "Controle total".
// Usuários, senhas/2FA, IA, WhatsApp e a conexão ficam só no painel de lá.
var RemoteWrites = map[string]bool{"/api/apps/pause": true, "/api/cleanup/run": true, "/api/cleanup/auto": true}

// Share é o que o servidor conectado liberou para o central.
type Share struct {
	View, Logs, Control bool
}

// RemoteAllowed diz se o pedido pode ser feito à distância com o que foi liberado.
func RemoteAllowed(method, path string, sh Share) bool {
	if !sh.View {
		return false
	}
	if method == "POST" {
		return sh.Control && RemoteWrites[path]
	}
	isLog, ok := RemotePaths[path]
	return method == "GET" && ok && (!isLog || sh.Logs || sh.Control)
}

type waiter struct {
	token string
	ch    chan ViewReply
}

type views struct {
	mu      sync.Mutex
	queues  map[string]chan ViewRequest
	waiting map[string]waiter
	polled  map[string]time.Time
	now     func() time.Time
}

func newViews(now func() time.Time) *views {
	return &views{queues: map[string]chan ViewRequest{}, waiting: map[string]waiter{}, polled: map[string]time.Time{}, now: now}
}

func (v *views) queue(token string) chan ViewRequest {
	q := v.queues[token]
	if q == nil {
		q = make(chan ViewRequest, queueSize)
		v.queues[token] = q
	}
	return q
}

func (v *views) listeningLocked(token string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.listening(token)
}

func (v *views) listening(token string) bool {
	t, ok := v.polled[token]
	return ok && v.now().Sub(t) < listenWithin
}

// drop esquece um servidor (revogado ou desconectado): quem esperava recebe erro.
func (v *views) drop(token string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.queues, token)
	delete(v.polled, token)
	for id, w := range v.waiting {
		if w.token == token {
			close(w.ch)
			delete(v.waiting, id)
		}
	}
}

// Ask manda um pedido para o servidor e espera a resposta.
func (c *Central) Ask(ctx context.Context, token, method, path, query string, body []byte, actor Actor) (ViewReply, error) {
	c.mu.Lock()
	t := c.find(token)
	ok := t != nil && t.Report != nil && RemoteAllowed(method, path, t.Report.share())
	c.mu.Unlock()
	if !ok {
		return ViewReply{}, ErrNotAllowed
	}
	v := c.views
	b := make([]byte, 8)
	rand.Read(b)
	req := ViewRequest{ID: hex.EncodeToString(b), Method: method, Path: path, Query: query, Body: body, Actor: actor}
	ch := make(chan ViewReply, 1)
	v.mu.Lock()
	if !v.listening(token) {
		v.mu.Unlock()
		return ViewReply{}, ErrNotListening
	}
	select {
	case v.queue(token) <- req:
	default:
		v.mu.Unlock()
		return ViewReply{}, ErrQueueFull
	}
	v.waiting[req.ID] = waiter{token: token, ch: ch}
	v.mu.Unlock()

	timer := time.NewTimer(askWait)
	defer timer.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return ViewReply{}, ErrNotListening
		}
		return r, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	v.mu.Lock()
	delete(v.waiting, req.ID)
	v.mu.Unlock()
	return ViewReply{}, ErrNoAnswer
}

// Poll entrega ao servidor conectado os pedidos dele (espera até 25 s por um).
// O pedido diz o que ele compartilha agora (vale na hora, sem esperar o
// resumo); com o compartilhamento desligado, volta na hora e para de escutar.
func (c *Central) Poll(ctx context.Context, token string, sh Share) []ViewRequest {
	c.mu.Lock()
	if t := c.find(token); t != nil && t.Report != nil {
		t.Report.setShare(sh)
	}
	c.mu.Unlock()
	v := c.views
	if !sh.View {
		v.drop(token)
		return []ViewRequest{}
	}
	v.mu.Lock()
	v.polled[token] = v.now()
	q := v.queue(token)
	v.mu.Unlock()
	out := []ViewRequest{}
	timer := time.NewTimer(pollWait)
	defer timer.Stop()
	select {
	case r := <-q:
		out = append(out, r)
	case <-timer.C:
		return out
	case <-ctx.Done():
		return out
	}
	for len(out) < pollBatch {
		select {
		case r := <-q:
			out = append(out, r)
		default:
			return out
		}
	}
	return out
}

// Reply entrega a resposta de um pedido (só o servidor dono do pedido responde).
func (c *Central) Reply(token string, r ViewReply) bool {
	v := c.views
	v.mu.Lock()
	defer v.mu.Unlock()
	w, ok := v.waiting[r.ID]
	if !ok || w.token != token {
		return false
	}
	delete(v.waiting, r.ID)
	if r.Status < 100 || r.Status > 599 {
		r.Status = 502
	}
	if !strings.HasPrefix(r.Type, "application/json") {
		r.Type = "application/json"
	}
	w.ch <- r
	return true
}
