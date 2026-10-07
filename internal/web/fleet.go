package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
	"github.com/edvitor13/vpserver-monitoring/internal/notify"
)

// Vários servidores: este painel como central (tokens e resumos que chegam)
// e como servidor conectado a outro central.

func (s *Server) fleetOff(w http.ResponseWriter) bool {
	if s.fl == nil || s.fl.Central == nil || s.fl.Client == nil {
		apiError(w, http.StatusServiceUnavailable, "fleet_disabled", "A conexão entre painéis não está disponível aqui.")
		return true
	}
	return false
}

// ownReport é o card deste painel.
func (s *Server) ownReport() fleet.Report {
	if s.mon == nil {
		return fleet.Report{Name: "este painel", Version: s.version}
	}
	panel, wa := "", "off"
	if s.nt != nil {
		panel, wa = s.nt.PanelURL(), s.nt.WhatsAppMode()
	}
	return fleet.FromOverview(s.mon.Overview(), panel, wa)
}

func (s *Server) ownName() string {
	if s.mon == nil {
		return "VPServer"
	}
	return s.mon.Overview().Server.Name
}

// --- chamadas de outros servidores (com o token do central, sem cookie) ---------------------

func (s *Server) fleetToken(w http.ResponseWriter, r *http.Request) (fleet.TokenView, bool) {
	if s.fleetOff(w) {
		return fleet.TokenView{}, false
	}
	tok, ok := s.fl.Central.Auth(r.Header.Get("Authorization"))
	if !ok {
		time.Sleep(300 * time.Millisecond)
		apiError(w, http.StatusUnauthorized, "bad_token", "Token inválido ou revogado.")
		return tok, false
	}
	return tok, true
}

func (s *Server) fleetReport(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.fleetToken(w, r)
	if !ok {
		return
	}
	var rep fleet.Report
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&rep); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Resumo inválido.")
		return
	}
	switch err := s.fl.Central.Accept(tok.ID, rep, clientIP(r, s.trustCF)); {
	case errors.Is(err, fleet.ErrTooSoon):
		apiError(w, http.StatusTooManyRequests, "too_soon", "Resumo cedo demais (um por minuto basta).")
		return
	case err != nil:
		apiError(w, http.StatusUnauthorized, "bad_token", "Token inválido ou revogado.")
		return
	}
	reply := map[string]any{"central": s.ownName(), "version": s.version,
		"whatsapp": map[string]bool{"allowed": tok.WhatsApp, "ready": tok.WhatsApp && s.nt != nil && s.nt.RelayReady()}}
	writeJSON(w, http.StatusOK, reply)
}

// fleetBye: o servidor conectado se desconectou de propósito.
func (s *Server) fleetBye(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.fleetToken(w, r)
	if !ok {
		return
	}
	if err := s.fl.Central.Bye(tok.ID); err != nil {
		apiError(w, http.StatusUnauthorized, "bad_token", "Token inválido ou revogado.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// fleetNotify manda, pelo WhatsApp daqui, um aviso de um servidor conectado.
func (s *Server) fleetNotify(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.fleetToken(w, r)
	if !ok {
		return
	}
	var b struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&b); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Aviso inválido.")
		return
	}
	if s.nt == nil {
		apiError(w, http.StatusServiceUnavailable, "relay_not_ready", notify.ErrRelayNotReady.Error())
		return
	}
	switch err := s.fl.Central.AllowRelay(tok.ID); {
	case errors.Is(err, fleet.ErrNoRelay):
		apiError(w, http.StatusForbidden, "relay_forbidden", "Este token não pode usar o WhatsApp do painel central.")
		return
	case errors.Is(err, fleet.ErrRelayRate):
		apiError(w, http.StatusTooManyRequests, "relay_rate", err.Error())
		return
	case err != nil:
		apiError(w, http.StatusUnauthorized, "bad_token", "Token inválido ou revogado.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.nt.Relay(ctx, tok.Name, b.Text); err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, notify.ErrRelayNotReady) {
			code = http.StatusServiceUnavailable
		}
		apiError(w, code, "relay_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// fleetUseWhatsApp liga ou desliga, neste servidor, o WhatsApp do central.
func (s *Server) fleetUseWhatsApp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !s.fleetBody(w, r, &b) {
		return
	}
	st, err := s.fl.Client.SetUseWhatsApp(b.On)
	if err != nil {
		apiError(w, http.StatusBadRequest, "relay_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "client": st})
}

// --- tela: cards de todos os servidores -----------------------------------------------------

func (s *Server) fleetServers(w http.ResponseWriter, r *http.Request) {
	if s.fleetOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"self": s.ownReport(), "servers": s.fl.Central.List(),
		"client": s.fl.Client.Status()})
}

// --- configurações (administradores) ----------------------------------------------------------

func (s *Server) fleetGet(w http.ResponseWriter, r *http.Request) {
	if s.fleetOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": s.fl.Central.List(), "client": s.fl.Client.Status(), "name": s.ownName()})
}

func (s *Server) fleetBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return false
	}
	if s.fleetOff(w) {
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return false
	}
	return true
}

func (s *Server) fleetTokenCreate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name     string `json:"name"`
		WhatsApp bool   `json:"whatsapp"`
	}
	if !s.fleetBody(w, r, &b) {
		return
	}
	v, plain, err := s.fl.Central.Create(b.Name, userOf(r).Name, b.WhatsApp)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_token_request", err.Error())
		return
	}
	s.audit(r, "Token de servidor criado", "Para conectar o servidor "+v.Name+" a este painel"+map[bool]string{true: " (pode usar o WhatsApp daqui).", false: "."}[v.WhatsApp])
	writeJSON(w, http.StatusOK, map[string]any{"token": v, "secret": plain})
}

func (s *Server) fleetTokenRevoke(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID string `json:"id"`
	}
	if !s.fleetBody(w, r, &b) {
		return
	}
	if err := s.fl.Central.Revoke(b.ID); err != nil {
		apiError(w, http.StatusNotFound, "not_found", "Token não encontrado.")
		return
	}
	s.audit(r, "Token de servidor revogado", "O servidor desse token não fala mais com este painel.")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) fleetConnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if !s.fleetBody(w, r, &b) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	st, err := s.fl.Client.Connect(ctx, b.URL, b.Token)
	if err != nil {
		apiError(w, http.StatusBadRequest, "connect_failed", err.Error())
		return
	}
	slog.Info("conectado a um painel central", "central", st.URL, "por", userOf(r).Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "client": st})
}

func (s *Server) fleetDisconnect(w http.ResponseWriter, r *http.Request) {
	var b struct{}
	if !s.fleetBody(w, r, &b) {
		return
	}
	if err := s.fl.Client.Disconnect(r.Context()); err != nil {
		apiError(w, http.StatusInternalServerError, "store_failed", "Não consegui gravar a mudança.")
		return
	}
	slog.Info("desconectado do painel central", "por", userOf(r).Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
