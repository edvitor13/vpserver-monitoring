package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
)

// Ver um servidor conectado dentro do painel central (só leitura).
//
// No central: a tela pede /api/fleet/view/<id>/api/...; o pedido desce pelo
// pedido aberto do servidor conectado (/api/fleet/poll) e a resposta sobe por
// /api/fleet/reply. No servidor conectado: LocalView executa a leitura aqui,
// com um usuário sem nenhuma permissão e só nos caminhos da lista fechada.

func refuse(status int, code, msg string) (int, string, []byte) {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	return status, "application/json", b
}

// LocalView executa aqui um pedido do painel central: leituras da lista fechada
// e, com o controle total, as ações (pausar/retomar, limpeza). Quem pediu vira
// um usuário sem administração, só com as permissões que trouxe (o Client já
// as zerou se o controle total não estiver liberado).
func (s *Server) LocalView(r fleet.ViewRequest) (status int, ctype string, body []byte) {
	// roda fora do servidor HTTP (que protegeria de um pânico): um erro aqui
	// não pode derrubar o painel
	defer func() {
		if p := recover(); p != nil {
			slog.Error("pedido do painel central falhou", "caminho", r.Path, "erro", p)
			status, ctype, body = refuse(http.StatusInternalServerError, "internal", "Erro ao atender o pedido do painel central.")
		}
	}()
	reads := map[string]http.HandlerFunc{
		"/api/overview": s.overview, "/api/history/host": s.hostHistory, "/api/history/apps": s.appsHistory,
		"/api/history/unit": s.unitHistory, "/api/traffic": s.traffic, "/api/system": s.system,
		"/api/cleanup": s.cleanupGet, "/api/logs/targets": s.logTargets, "/api/logs": s.logs,
	}
	writes := map[string]http.HandlerFunc{"/api/apps/pause": s.pauseApp, "/api/cleanup/run": s.cleanupRun, "/api/cleanup/auto": s.cleanupAuto}
	name := strings.TrimSpace(r.Actor.Name)
	if name == "" || len(name) > 40 {
		name = "alguém"
	}
	u := User{Name: name + " (pelo painel central)", Actions: r.Actor.Actions, Clean: r.Actor.Clean}
	var h http.HandlerFunc
	switch r.Method {
	case http.MethodGet:
		if _, listed := fleet.RemotePaths[r.Path]; listed {
			h = reads[r.Path]
		}
	case http.MethodPost:
		if fleet.RemoteWrites[r.Path] {
			h = writes[r.Path]
		}
		if r.Path == "/api/apps/pause" && !u.CanAct() {
			return refuse(http.StatusForbidden, "forbidden", "Seu usuário no painel central não pode pausar nem retomar aplicações.")
		}
		if r.Path != "/api/apps/pause" && !u.CanClean() {
			return refuse(http.StatusForbidden, "forbidden", "Seu usuário no painel central não pode limpar o disco.")
		}
	}
	if h == nil {
		return refuse(http.StatusNotFound, "not_found", "Não dá para fazer isso à distância.")
	}
	target := r.Path
	if r.Query != "" {
		target += "?" + r.Query
	}
	req := httptest.NewRequest(r.Method, target, bytes.NewReader(r.Body))
	req.Header.Set("X-Requested-With", "vpmon") // veio de dentro: a origem foi conferida no central
	req.RemoteAddr = "painel-central:0"
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, u))
	rec := httptest.NewRecorder()
	h(rec, req)
	ctype = rec.Header().Get("Content-Type")
	if ctype == "" {
		ctype = "application/json"
	}
	return rec.Code, ctype, rec.Body.Bytes()
}

// fleetPoll: o servidor conectado espera aqui (até 25 s) pelos pedidos dele.
func (s *Server) fleetPoll(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.fleetToken(w, r)
	if !ok {
		return
	}
	var b struct {
		View    bool `json:"shareView"`
		Logs    bool `json:"shareLogs"`
		Control bool `json:"shareControl"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b)
	sh := fleet.Share{View: b.View, Logs: b.Logs, Control: b.Control}
	writeJSON(w, http.StatusOK, map[string]any{"requests": s.fl.Central.Poll(r.Context(), tok.ID, sh)})
}

// fleetReplyView: a resposta de um pedido sobe por aqui.
func (s *Server) fleetReplyView(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.fleetToken(w, r)
	if !ok {
		return
	}
	var rep fleet.ViewReply
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, fleet.MaxReply*4/3+4096)).Decode(&rep); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Resposta inválida ou grande demais.")
		return
	}
	if !s.fl.Central.Reply(tok.ID, rep) {
		apiError(w, http.StatusNotFound, "no_request", "Ninguém está esperando essa resposta.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// fleetView: a tela do central lê (GET) ou, com o controle total, age (POST)
// num servidor conectado. Valem as permissões de quem está logado aqui.
func (s *Server) fleetView(w http.ResponseWriter, r *http.Request) {
	if s.fleetOff(w) {
		return
	}
	var body []byte
	if r.Method == http.MethodPost {
		if !sameOrigin(r) {
			apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
			return
		}
		body = b
	}
	u := userOf(r)
	actor := fleet.Actor{Name: u.Name, Actions: u.CanAct(), Clean: u.CanClean()}
	path := "/" + strings.TrimPrefix(r.PathValue("rest"), "/")
	rep, err := s.fl.Central.Ask(r.Context(), r.PathValue("id"), r.Method, path, r.URL.RawQuery, body, actor)
	switch {
	case errors.Is(err, fleet.ErrNotAllowed):
		apiError(w, http.StatusForbidden, "not_shared", "Esse servidor não liberou isso para este painel.")
		return
	case errors.Is(err, fleet.ErrNotListening):
		apiError(w, http.StatusServiceUnavailable, "remote_offline", "Esse servidor não está conectado agora.")
		return
	case errors.Is(err, fleet.ErrQueueFull):
		apiError(w, http.StatusTooManyRequests, "remote_busy", err.Error())
		return
	case err != nil:
		apiError(w, http.StatusGatewayTimeout, "remote_timeout", "Esse servidor demorou para responder.")
		return
	}
	w.Header().Set("Content-Type", rep.Type)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(rep.Status)
	w.Write(rep.Body)
}

// fleetShare: no servidor conectado, o que o central pode ver daqui.
func (s *Server) fleetShare(w http.ResponseWriter, r *http.Request) {
	var b struct {
		View    bool `json:"view"`
		Logs    bool `json:"logs"`
		Control bool `json:"control"`
	}
	if !s.fleetBody(w, r, &b) {
		return
	}
	st, err := s.fl.Client.SetShare(fleet.Share{View: b.View, Logs: b.Logs, Control: b.Control})
	if err != nil {
		apiError(w, http.StatusBadRequest, "share_unavailable", err.Error())
		return
	}
	what := "nada"
	switch {
	case st.ShareControl:
		what = "este servidor inteiro e fazer as ações (pausar/retomar apps e limpeza) — controle total"
	case st.ShareView && st.ShareLogs:
		what = "este servidor, inclusive os logs (só leitura)"
	case st.ShareView:
		what = "este servidor, sem os logs (só leitura)"
	}
	slog.Info("compartilhamento com o painel central", "por", userOf(r).Name, "ver", st.ShareView, "logs", st.ShareLogs, "controle", st.ShareControl)
	s.audit(r, "Compartilhamento com o painel central", "O painel central "+st.Central+" agora pode ver: "+what+".")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "client": st})
}
