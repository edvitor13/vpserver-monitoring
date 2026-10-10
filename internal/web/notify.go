package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/notify"
)

// Aba Notificações: WhatsApp (QR code), para quem mandar e o que avisar.

func (s *Server) notifyOff(w http.ResponseWriter) bool {
	if s.nt == nil {
		apiError(w, http.StatusServiceUnavailable, "notify_disabled", "As notificações não estão disponíveis neste painel.")
		return true
	}
	return false
}

func (s *Server) notifyGet(w http.ResponseWriter, r *http.Request) {
	if s.notifyOff(w) {
		return
	}
	v := s.nt.View(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"notify": v, "ai": s.ai.get() != nil})
}

func (s *Server) notifyConfig(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	if s.notifyOff(w) {
		return
	}
	var c notify.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&c); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	if c.Lang == "" && s.nt.Lang() == "" {
		c.Lang = reqLang(r) // padrão: o idioma de quem configurou
	}
	saved, err := s.nt.SaveConfig(c)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_config", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": saved})
}

func (s *Server) notifyConnect(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	if s.notifyOff(w) {
		return
	}
	var body struct {
		Number string `json:"number"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body)
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	qr, err := s.nt.Connect(ctx, body.Number)
	if err != nil {
		apiError(w, http.StatusBadGateway, "wa_connect_failed", "Não consegui gerar o QR code: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, qr)
}

func (s *Server) notifyLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	if s.notifyOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.nt.Logout(ctx); err != nil {
		apiError(w, http.StatusBadGateway, "wa_logout_failed", "Não consegui desconectar: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) notifyGroups(w http.ResponseWriter, r *http.Request) {
	if s.notifyOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := s.nt.Groups(ctx)
	if err != nil {
		apiError(w, http.StatusBadGateway, "wa_groups_failed", "Não consegui listar os grupos: "+err.Error())
		return
	}
	if list == nil {
		list = []notify.Group{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": list})
}

func (s *Server) notifySend(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	if s.notifyOff(w) {
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	if !s.sendLimit.allow() {
		apiError(w, http.StatusTooManyRequests, "too_many_sends", "Muitos envios em pouco tempo. Espere alguns minutos.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	msg, err := s.nt.SendNow(ctx, body.Kind)
	if err != nil {
		apiError(w, http.StatusBadRequest, "send_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}
