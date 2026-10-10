package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/i18n"
)

const (
	chatMaxMessages = 30
	chatMaxChars    = 12000 // por mensagem
	chatMaxTotal    = 80000 // conversa inteira
	chatTimeout     = 4 * time.Minute
	chatWindow      = 10 * time.Minute
	chatPerWindow   = 30 // perguntas por janela (o painel tem um usuário só; isso só segura abuso e custo)
)

type chatLimiter struct {
	mu   sync.Mutex
	hits []time.Time
}

func (l *chatLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	k := 0
	for _, t := range l.hits {
		if now.Sub(t) < chatWindow {
			l.hits[k] = t
			k++
		}
	}
	l.hits = l.hits[:k]
	if len(l.hits) >= chatPerWindow {
		return false
	}
	l.hits = append(l.hits, now)
	return true
}

func (s *Server) chatStatus(w http.ResponseWriter, r *http.Request) {
	cli := s.ai.get()
	st := map[string]any{"enabled": cli != nil}
	if cli != nil {
		st["model"] = cli.Model()
	}
	writeJSON(w, http.StatusOK, st)
}

// chat responde em text/event-stream: "tool" (a IA consultou algo), "delta"
// (pedaço da resposta), "done" (fim, com tokens) ou "error".
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	cli := s.ai.get()
	if cli == nil {
		apiError(w, http.StatusServiceUnavailable, "ai_disabled", "A IA não está configurada. Ponha a chave da DeepSeek em Configurações → IA.")
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	msgs := body.Messages
	if len(msgs) > chatMaxMessages {
		msgs = msgs[len(msgs)-chatMaxMessages:]
	}
	var history []ai.Message
	total := 0
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		if len(c) > chatMaxChars {
			c = c[:chatMaxChars]
		}
		total += len(c)
		history = append(history, ai.Text(m.Role, c))
	}
	for total > chatMaxTotal && len(history) > 1 { // conversa longa: some o começo
		total -= len(*history[0].Content)
		history = history[1:]
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		apiError(w, http.StatusBadRequest, "bad_request", "Faltou a pergunta.")
		return
	}
	if !s.chatLimit.allow() {
		apiError(w, http.StatusTooManyRequests, "too_many_questions", "Muitas perguntas em pouco tempo. Espere alguns minutos.")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // a resposta pode passar do WriteTimeout do servidor
	w.WriteHeader(http.StatusOK)

	var wmu sync.Mutex
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		rc.Flush()
		wmu.Unlock()
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	// ping a cada 15 s: a Cloudflare corta a conexão depois de 100 s sem nada
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				wmu.Lock()
				fmt.Fprint(w, ": ping\n\n")
				rc.Flush()
				wmu.Unlock()
			}
		}
	}()

	start := time.Now()
	lang := reqLang(r)
	usage, err := ai.Converse(ctx, cli, s.mon.AIContext()+i18n.AINote(lang), history, s.mon.AI(), func(e ai.Event) {
		e.Label = i18n.Tr(lang, e.Label) // "Histórico do servidor · CPU · 24 h": o rótulo da consulta vai para a tela
		send(e.Type, e)
	})
	if err != nil {
		msg := "A IA não conseguiu responder. Tente de novo."
		var ae *ai.Error
		switch {
		case errors.As(err, &ae):
			msg = ae.Message
		case errors.Is(err, context.DeadlineExceeded):
			msg = "A resposta passou de 4 minutos e foi interrompida."
		case errors.Is(err, context.Canceled):
			return // o navegador desistiu
		}
		slog.Warn("chat falhou", "err", err)
		send("error", map[string]string{"message": i18n.Tr(lang, msg)})
		return
	}
	slog.Info("chat", "segundos", int(time.Since(start).Seconds()), "tokens_entrada", usage.PromptTokens,
		"tokens_saida", usage.CompletionTokens, "cache", usage.CacheHitTokens)
	send("done", map[string]any{"model": cli.Model(), "usage": usage})
}
