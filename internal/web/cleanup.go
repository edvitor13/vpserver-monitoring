package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/edvitor13/vpserver-monitoring/internal/cleanup"
)

// Tela Limpeza: ver é para todos; limpar e configurar a automática exigem a
// permissão "Limpar o disco" (ou administrador).

const msgNoClean = "Seu usuário não pode limpar o disco. Peça a permissão a um administrador."

// WithCleanup liga a tela Limpeza (antes do Handler).
func (s *Server) WithCleanup(c *cleanup.Service) *Server {
	s.cl = c
	return s
}

func (s *Server) cleanupOff(w http.ResponseWriter) bool {
	if s.cl == nil {
		apiError(w, http.StatusServiceUnavailable, "cleanup_disabled", "A limpeza não está disponível aqui.")
		return true
	}
	return false
}

func (s *Server) cleanupGet(w http.ResponseWriter, r *http.Request) {
	if s.cleanupOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleanup": s.cl.View(), "canClean": userOf(r).CanClean()})
}

func (s *Server) cleanupBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return false
	}
	if s.cleanupOff(w) {
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return false
	}
	return true
}

func (s *Server) cleanupRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		cleanup.Request
		Confirm bool `json:"confirm"` // a tela só manda depois da confirmação
	}
	if !s.cleanupBody(w, r, &b) {
		return
	}
	if !b.Confirm {
		apiError(w, http.StatusBadRequest, "confirm_required", "Confirme a limpeza na tela.")
		return
	}
	u := userOf(r)
	err := s.cl.Start(u.Name, b.Request)
	switch {
	case errors.Is(err, cleanup.ErrBusy):
		apiError(w, http.StatusConflict, "cleanup_busy", err.Error())
		return
	case err != nil:
		apiError(w, http.StatusBadRequest, "cleanup_refused", err.Error())
		return
	}
	slog.Info("limpeza pedida pela tela", "por", u.Name, "cache", b.BuildCache, "imagens", b.Dangling, "logs", len(b.Logs))
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true, "running": true})
}

func (s *Server) cleanupAuto(w http.ResponseWriter, r *http.Request) {
	var a cleanup.Auto
	if !s.cleanupBody(w, r, &a) {
		return
	}
	saved, err := s.cl.SaveAuto(a)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_auto", err.Error())
		return
	}
	slog.Info("limpeza automática configurada", "por", userOf(r).Name, "ligada", saved.Enabled, "limite", saved.Threshold)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auto": saved})
}
