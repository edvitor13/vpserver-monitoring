package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/backup"
	"github.com/edvitor13/vpserver-monitoring/internal/s3"
)

// Aba Backups: só administradores (todas as rotas passam por admin()).

// WithBackup liga a aba Backups (antes do Handler).
func (s *Server) WithBackup(b *backup.Service) *Server {
	s.bk = b
	return s
}

func (s *Server) backupOff(w http.ResponseWriter) bool {
	if s.bk == nil {
		apiError(w, http.StatusServiceUnavailable, "backup_disabled", "Os backups não estão disponíveis aqui.")
		return true
	}
	return false
}

func (s *Server) backupBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return false
	}
	if s.backupOff(w) {
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return false
	}
	return true
}

func (s *Server) backupGet(w http.ResponseWriter, r *http.Request) {
	if s.backupOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup": s.bk.View()})
}

func (s *Server) backupStorage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		s3.Config
		Prefix string `json:"prefix"`
	}
	if !s.backupBody(w, r, &b) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	v, err := s.bk.SaveStorage(ctx, b.Config, b.Prefix)
	if err != nil {
		apiError(w, http.StatusBadRequest, "storage_failed", err.Error())
		return
	}
	s.audit(r, "Armazenamento dos backups configurado", "Bucket "+v.Bucket+" em "+v.Endpoint+" (testado: gravar, conferir, listar e apagar).")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "storage": v})
}

func (s *Server) backupKey(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Generate  bool   `json:"generate"`
		PublicKey string `json:"publicKey"`
		Confirm   bool   `json:"confirm"`
	}
	if !s.backupBody(w, r, &b) {
		return
	}
	if s.bk.View().PublicKey != "" && !b.Confirm {
		apiError(w, http.StatusBadRequest, "confirm_required", "Confirme a troca da chave na tela.")
		return
	}
	if b.Generate {
		pub, priv, err := s.bk.GenerateKey()
		if err != nil {
			apiError(w, http.StatusInternalServerError, "key_failed", "Não consegui gerar a chave.")
			return
		}
		s.audit(r, "Chave dos backups gerada", "Os próximos backups são cifrados para a chave pública "+pub+". A privada foi mostrada uma vez na tela.")
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "publicKey": pub, "privateKey": priv})
		return
	}
	pub, err := s.bk.SetPublicKey(b.PublicKey)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_key", err.Error())
		return
	}
	s.audit(r, "Chave dos backups trocada", "Os próximos backups são cifrados para a chave pública "+pub+".")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "publicKey": pub})
}

func (s *Server) backupRetention(w http.ResponseWriter, r *http.Request) {
	var b backup.Retention
	if !s.backupBody(w, r, &b) {
		return
	}
	v, err := s.bk.SaveRetention(b)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_retention", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "retention": v})
}

func (s *Server) backupTarget(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID       string `json:"id"`
		Enabled  bool   `json:"enabled"`
		Every    string `json:"every"`
		Database string `json:"database"`
		Confirm  bool   `json:"confirm"`
	}
	if !s.backupBody(w, r, &b) {
		return
	}
	if b.Enabled && !b.Confirm {
		apiError(w, http.StatusBadRequest, "confirm_required", "Confirme na tela.")
		return
	}
	t, err := s.bk.SetTarget(b.ID, b.Enabled, b.Every, b.Database)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_target", err.Error())
		return
	}
	what := "desligado"
	if t.Enabled {
		what = "ligado (" + t.Every + ")"
	}
	s.audit(r, "Backup de banco "+what, "Banco "+t.ID+".")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": t})
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
	}
	if !s.backupBody(w, r, &b) {
		return
	}
	if !b.Confirm {
		apiError(w, http.StatusBadRequest, "confirm_required", "Confirme na tela.")
		return
	}
	if err := s.bk.RunNow(b.ID, userOf(r).Name); err != nil {
		apiError(w, http.StatusConflict, "backup_refused", err.Error())
		return
	}
	slog.Info("backup pedido pela tela", "banco", b.ID, "por", userOf(r).Name)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) backupObjects(w http.ResponseWriter, r *http.Request) {
	if s.backupOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	objs, err := s.bk.Objects(ctx, r.URL.Query().Get("id"))
	if err != nil {
		apiError(w, http.StatusBadGateway, "storage_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": objs})
}

// backupDownload entrega o arquivo como está no bucket (cifrado: só abre com a chave privada).
func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	if s.backupOff(w) {
		return
	}
	key := r.URL.Query().Get("key")
	rc, size, err := s.bk.Download(r.Context(), key)
	if err != nil {
		apiError(w, http.StatusBadRequest, "download_failed", err.Error())
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(key)+`"`)
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("Cache-Control", "no-store")
	slog.Info("backup baixado pela tela", "arquivo", key, "por", userOf(r).Name)
	io.Copy(w, rc)
}
