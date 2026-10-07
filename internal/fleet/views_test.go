package fleet

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRemoteAllowed(t *testing.T) {
	view, logs, ctl := Share{View: true}, Share{View: true, Logs: true}, Share{View: true, Control: true}
	cases := []struct {
		method, path string
		sh           Share
		want         bool
	}{
		{"GET", "/api/overview", view, true},
		{"GET", "/api/overview", Share{Logs: true, Control: true}, false}, // sem "ver", nada
		{"GET", "/api/logs", view, false},
		{"GET", "/api/logs", logs, true},
		{"GET", "/api/logs", ctl, true}, // controle total inclui os logs
		{"GET", "/api/users", ctl, false},
		{"GET", "/api/settings", ctl, false},
		{"POST", "/api/apps/pause", logs, false},
		{"POST", "/api/apps/pause", ctl, true},
		{"POST", "/api/cleanup/run", ctl, true},
		{"POST", "/api/users", ctl, false},
		{"POST", "/api/password", ctl, false},
		{"POST", "/api/fleet/disconnect", ctl, false},
		{"DELETE", "/api/overview", ctl, false},
	}
	for _, c := range cases {
		if got := RemoteAllowed(c.method, c.path, c.sh); got != c.want {
			t.Errorf("%s %s %+v: %v", c.method, c.path, c.sh, got)
		}
	}
}

func TestAskPollReply(t *testing.T) {
	pollWait, askWait = 300*time.Millisecond, 300*time.Millisecond
	defer func() { pollWait, askWait = 25*time.Second, 20*time.Second }()
	c, _, _ := newCentral(t)
	a, _, _ := c.Create("loja", "chefe", false)
	b, _, _ := c.Create("blog", "chefe", false)
	c.Accept(a.ID, Report{Name: "loja"}, "")
	c.Accept(b.ID, Report{Name: "blog"}, "")
	ctx := context.Background()

	if _, err := c.Ask(ctx, a.ID, "GET", "/api/overview", "", nil, Actor{}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("sem compartilhar: %v", err)
	}
	if got := c.Poll(ctx, a.ID, Share{View: true}); len(got) != 0 {
		t.Fatal("ninguém pediu nada: o Poll volta vazio no fim da espera")
	}
	if !c.List()[1].Viewable || c.List()[1].Control {
		t.Fatalf("depois do Poll, dá para ver (sem controle): %+v", c.List()[1])
	}
	if _, err := c.Ask(ctx, a.ID, "POST", "/api/apps/pause", "", nil, Actor{}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ação sem controle total: %v", err)
	}

	// o servidor conectado escuta e responde (e o blog não consegue responder pela loja)
	done := make(chan []ViewRequest, 1)
	go func() { done <- c.Poll(ctx, a.ID, Share{View: true}) }()
	time.Sleep(50 * time.Millisecond)
	type res struct {
		r   ViewReply
		err error
	}
	got := make(chan res, 1)
	go func() {
		r, err := c.Ask(ctx, a.ID, "GET", "/api/overview", "x=1", nil, Actor{Name: "ana"})
		got <- res{r, err}
	}()
	reqs := <-done
	if len(reqs) != 1 || reqs[0].Path != "/api/overview" || reqs[0].Query != "x=1" || reqs[0].Method != "GET" || reqs[0].Actor.Name != "ana" {
		t.Fatalf("pedido entregue: %+v", reqs)
	}
	if c.Reply(b.ID, ViewReply{ID: reqs[0].ID, Status: 200, Body: []byte("{}")}) {
		t.Fatal("outro servidor não responde pelo pedido da loja")
	}
	if !c.Reply(a.ID, ViewReply{ID: reqs[0].ID, Status: 200, Type: "text/html", Body: []byte(`{"ok":1}`)}) {
		t.Fatal("a loja responde o próprio pedido")
	}
	r := <-got
	if r.err != nil || r.r.Status != 200 || string(r.r.Body) != `{"ok":1}` || r.r.Type != "application/json" {
		t.Fatalf("resposta: %+v %v", r.r, r.err)
	}
	if c.Reply(a.ID, ViewReply{ID: reqs[0].ID}) {
		t.Fatal("responder duas vezes não vale")
	}

	// sem resposta: o pedido expira
	go c.Poll(ctx, a.ID, Share{View: true})
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Ask(ctx, a.ID, "GET", "/api/overview", "", nil, Actor{}); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("sem resposta: %v", err)
	}

	// desligou o compartilhamento: para na hora
	c.Poll(ctx, a.ID, Share{})
	if c.List()[1].Viewable {
		t.Fatal("desligou: não dá mais para ver")
	}
	if _, err := c.Ask(ctx, a.ID, "GET", "/api/overview", "", nil, Actor{}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("desligado: %v", err)
	}

	// controle total e revogação no meio de um pedido
	go c.Poll(ctx, a.ID, Share{View: true, Control: true})
	time.Sleep(20 * time.Millisecond)
	if !c.List()[1].Control || !c.List()[1].Logs {
		t.Fatalf("controle total (com logs): %+v", c.List()[1])
	}
	go func() {
		_, err := c.Ask(ctx, a.ID, "POST", "/api/apps/pause", "", []byte(`{}`), Actor{Name: "ana", Actions: true})
		got <- res{err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	c.Revoke(a.ID)
	if r := <-got; !errors.Is(r.err, ErrNotListening) && !errors.Is(r.err, ErrNoAnswer) {
		t.Fatalf("revogado no meio: %v", r.err)
	}
}
