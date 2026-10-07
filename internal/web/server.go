// Package web serve a interface (arquivos embutidos no binário) e a API JSON.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
	"github.com/edvitor13/vpserver-monitoring/internal/notify"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	mon       *monitor.Monitor
	auth      *Auth
	trustCF   bool
	assets    map[string]asset
	ai        *aiState // IA: configurada pela tela ou pelo .env (DEEPSEEK_*)
	chatLimit chatLimiter
	nt        *notify.Service // notificações pelo WhatsApp (nil = sem)
	sendLimit chatLimiter     // "enviar agora" da aba Notificações
	pauseLim  chatLimiter     // pausar/retomar app
}

type asset struct {
	body  []byte
	ctype string
	etag  string
}

// New monta o servidor. envAI vem das variáveis DEEPSEEK_*; settingsPath é o
// <data>/settings.json onde fica o que for configurado pela tela; nt são as
// notificações (pode ser nil).
func New(mon *monitor.Monitor, auth *Auth, trustCF bool, envAI ai.Config, settingsPath string, nt *notify.Service) *Server {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	s := &Server{mon: mon, auth: auth, trustCF: trustCF, assets: map[string]asset{}, ai: newAIState(envAI, settingsPath), nt: nt}
	if nt != nil {
		nt.SetAI(s.ai.get) // as análises usam a mesma IA da tela
	}
	sub, _ := fs.Sub(staticFS, "static")
	fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(sub, p)
		sum := sha256.Sum256(b)
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		s.assets["/"+p] = asset{body: b, ctype: ct, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", s.static)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/me", s.private(s.me))
	mux.HandleFunc("POST /api/password", s.private(s.changePassword))
	mux.HandleFunc("GET /api/overview", s.private(s.overview))
	mux.HandleFunc("GET /api/history/host", s.private(s.hostHistory))
	mux.HandleFunc("GET /api/history/apps", s.private(s.appsHistory))
	mux.HandleFunc("GET /api/history/unit", s.private(s.unitHistory))
	mux.HandleFunc("GET /api/traffic", s.private(s.traffic))
	mux.HandleFunc("GET /api/logs/targets", s.private(s.logTargets))
	mux.HandleFunc("GET /api/logs", s.private(s.logs))
	mux.HandleFunc("GET /api/system", s.private(s.system))
	mux.HandleFunc("GET /api/chat/status", s.private(s.chatStatus))
	mux.HandleFunc("POST /api/chat", s.private(s.chat))
	mux.HandleFunc("GET /api/settings", s.private(s.settingsGet))
	mux.HandleFunc("POST /api/settings/ai", s.private(s.settingsAI))
	mux.HandleFunc("POST /api/apps/pause", s.private(s.pauseApp))
	mux.HandleFunc("GET /api/notify", s.private(s.notifyGet))
	mux.HandleFunc("POST /api/notify/config", s.private(s.notifyConfig))
	mux.HandleFunc("POST /api/notify/connect", s.private(s.notifyConnect))
	mux.HandleFunc("POST /api/notify/logout", s.private(s.notifyLogout))
	mux.HandleFunc("GET /api/notify/groups", s.private(s.notifyGroups))
	mux.HandleFunc("POST /api/notify/send", s.private(s.notifySend))
	return secureHeaders(withGzip(mux))
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; "+
			"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" {
		p = "/index.html"
	}
	a, ok := s.assets[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.ctype)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", a.etag)
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(a.body)
}

// --- helpers -------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

type ctxKey struct{}

// private exige sessão válida e põe o usuário no contexto (veja userOf).
func (s *Server) private(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.auth.Session(r)
		if !ok {
			apiError(w, http.StatusUnauthorized, "login_required", "Entre de novo para continuar.")
			return
		}
		// senha inicial ou provisória: nada além de trocar a senha
		if u.MustChange && r.URL.Path != "/api/me" && r.URL.Path != "/api/password" {
			apiError(w, http.StatusForbidden, "password_change_required", "Troque a senha provisória para continuar.")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

// userOf é o usuário logado (só dentro de rotas private).
func userOf(r *http.Request) User {
	u, _ := r.Context().Value(ctxKey{}).(User)
	return u
}

// origin é o endereço por onde o navegador abriu o painel. Atrás da Cloudflare
// (túnel), o Host é o domínio público e o esquema vem no X-Forwarded-Proto.
func (s *Server) origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || (s.trustCF && (r.Header.Get("X-Forwarded-Proto") == "https" || strings.Contains(r.Header.Get("Cf-Visitor"), `"https"`))) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// security avisa as notificações (senhas erradas, login, troca de senha).
func (s *Server) security(kind, ip string) {
	if s.nt != nil {
		s.nt.Security(kind, ip)
	}
}

// sameOrigin barra POST de outro site (além do cookie SameSite=Strict).
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") != "vpmon" {
		return false
	}
	o := r.Header.Get("Origin")
	return o == "" || strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://") == r.Host
}

func rangeParam(r *http.Request) string {
	if v := r.URL.Query().Get("range"); monitor.RangeOK(v) {
		return v
	}
	return "1h"
}

// --- login -----------------------------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	ip := clientIP(r, s.trustCF)
	if blocked, wait := s.auth.Blocked(ip); blocked {
		mins := int(wait.Minutes()) + 1
		apiError(w, http.StatusTooManyRequests, "too_many_attempts",
			"Muitas tentativas erradas. Tente de novo em "+strconv.Itoa(mins)+" min.")
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	u, ok := s.auth.Login(body.User, body.Password)
	if !ok {
		s.auth.Fail(ip)
		slog.Warn("login falhou", "ip", ip)
		s.security("login_fail", ip)
		time.Sleep(600 * time.Millisecond)
		apiError(w, http.StatusUnauthorized, "bad_credentials", "Usuário ou senha incorretos.")
		return
	}
	s.auth.Reset(ip)
	s.auth.Issue(w, u.Name)
	s.auth.MarkLogin(u.Name)
	slog.Info("login", "ip", ip, "usuario", u.Name)
	s.security("login", ip)
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Name, "mustChange": u.MustChange})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	s.auth.Clear(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if s.nt != nil {
		s.nt.SeenOrigin(s.origin(r))
	}
	u := userOf(r)
	src, changed := s.auth.PasswordInfo(u)
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Name, "passwordSource": src,
		"passwordChanged": changed, "mustChange": u.MustChange,
		"admin": u.Admin, "actions": u.CanAct(), "manage": u.CanManage()})
}

// changePassword troca a senha (e, se pedido, o usuário) pela tela. Confere a
// atual (com o mesmo freio de tentativas do login), grava o hash e devolve um
// cookie novo para quem trocou — as outras sessões caem.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	ip := clientIP(r, s.trustCF)
	if blocked, wait := s.auth.Blocked(ip); blocked {
		apiError(w, http.StatusTooManyRequests, "too_many_attempts",
			"Muitas tentativas erradas. Tente de novo em "+strconv.Itoa(int(wait.Minutes())+1)+" min.")
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
		User    string `json:"user"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	u, err := s.auth.ChangePassword(userOf(r).Name, body.Current, body.New, body.User)
	switch err {
	case nil:
	case ErrBadCurrent:
		s.auth.Fail(ip)
		time.Sleep(600 * time.Millisecond)
		apiError(w, http.StatusBadRequest, "bad_current", "A senha atual não confere.")
		return
	case ErrWeak:
		apiError(w, http.StatusBadRequest, "weak_password", "A nova senha precisa ter pelo menos 10 caracteres.")
		return
	case ErrSame:
		apiError(w, http.StatusBadRequest, "same_password", "A nova senha é igual à atual.")
		return
	case ErrBadUser, ErrUserExists:
		apiError(w, http.StatusBadRequest, "bad_user", err.Error())
		return
	default:
		slog.Error("troca de senha falhou", "err", err)
		apiError(w, http.StatusInternalServerError, "store_failed", "Não consegui gravar a nova senha.")
		return
	}
	s.auth.Reset(ip)
	s.auth.Issue(w, u.Name)
	slog.Info("senha trocada pela tela", "ip", ip, "usuario", u.Name)
	s.security("password", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u.Name})
}

// --- dados -----------------------------------------------------------------------------

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.Overview())
}

func (s *Server) hostHistory(w http.ResponseWriter, r *http.Request) {
	var fields []string
	if f := r.URL.Query().Get("f"); f != "" {
		fields = strings.Split(f, ",")
	}
	writeJSON(w, http.StatusOK, s.mon.HostHistory(rangeParam(r), fields))
}

var appFields = map[string]bool{"cpu": true, "mem": true, "rx": true, "tx": true, "rd": true, "wr": true}

func (s *Server) appsHistory(w http.ResponseWriter, r *http.Request) {
	f := r.URL.Query().Get("f")
	if !appFields[f] {
		apiError(w, http.StatusBadRequest, "bad_field", "Campo inválido.")
		return
	}
	writeJSON(w, http.StatusOK, s.mon.AppsHistory(f, rangeParam(r)))
}

func (s *Server) unitHistory(w http.ResponseWriter, r *http.Request) {
	res, ok := s.mon.UnitHistory(r.URL.Query().Get("key"), rangeParam(r))
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "Ainda não há histórico para isso.")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) traffic(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.Traffic())
}

func (s *Server) logTargets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.LogTargets())
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("c")
	if name != "*" && !s.mon.ContainerExists(name) {
		apiError(w, http.StatusNotFound, "not_found", "Contêiner não encontrado.")
		return
	}
	tail, _ := strconv.Atoi(q.Get("tail"))
	if tail <= 0 || tail > 2000 {
		tail = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	lines, err := s.mon.Logs(ctx, name, tail, q.Get("errors") == "1")
	if err != nil {
		apiError(w, http.StatusBadGateway, "docker_error", "Não consegui ler os logs: "+err.Error())
		return
	}
	if lines == nil {
		lines = []monitor.LogLineView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.System())
}

// pauseApp congela ou descongela os contêineres de uma app (botão Pausar /
// Retomar da aba Aplicações). O próprio monitor fica de fora.
func (s *Server) pauseApp(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	var body struct {
		App   string `json:"app"`
		Pause bool   `json:"pause"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || body.App == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	if !s.pauseLim.allow() {
		apiError(w, http.StatusTooManyRequests, "too_many_actions", "Muitas ações em pouco tempo. Espere alguns minutos.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	names, err := s.mon.SetPaused(ctx, body.App, body.Pause)
	verb := map[bool]string{true: "pausada", false: "retomada"}[body.Pause]
	ip := clientIP(r, s.trustCF)
	switch {
	case errors.Is(err, monitor.ErrSelfPause):
		apiError(w, http.StatusBadRequest, "self_pause", "O próprio monitor não pode ser pausado.")
		return
	case errors.Is(err, monitor.ErrNoApp):
		apiError(w, http.StatusNotFound, "not_found", "Aplicação não encontrada.")
		return
	case err != nil && len(names) == 0:
		slog.Warn("pausar/retomar falhou", "app", body.App, "err", err)
		apiError(w, http.StatusBadGateway, "docker_error", "O Docker recusou: "+err.Error())
		return
	}
	slog.Info("app "+verb+" pela tela", "app", body.App, "conteineres", strings.Join(names, ","), "ip", ip)
	if s.nt != nil && len(names) > 0 {
		emoji := map[bool]string{true: "⏸️", false: "▶️"}[body.Pause]
		s.nt.Audit("pauses", fmt.Sprintf("%s %s pela tela", body.App, verb), fmt.Sprintf("%s *%s %s pelo painel · %s*\nContêineres: %s (IP %s).",
			emoji, body.App, verb, s.mon.Overview().Server.Name, strings.Join(names, ", "), ip))
	}
	res := map[string]any{"ok": true, "containers": names}
	if err != nil {
		res["warning"] = err.Error()
	}
	if names == nil {
		res["containers"] = []string{}
	}
	writeJSON(w, http.StatusOK, res)
}
