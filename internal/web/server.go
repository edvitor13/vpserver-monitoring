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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/backup"
	"github.com/edvitor13/vpserver-monitoring/internal/cleanup"
	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
	"github.com/edvitor13/vpserver-monitoring/internal/notify"
	"github.com/edvitor13/vpserver-monitoring/internal/sshchat"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	mon        *monitor.Monitor
	auth       *Auth
	trustCF    bool
	assets     map[string]asset
	ai         *aiState // IA: configurada pela tela ou pelo .env (DEEPSEEK_*)
	chatLimit  chatLimiter
	nt         *notify.Service // notificações pelo WhatsApp (nil = sem)
	version    string          // versão do painel: vai no index.html e no X-VPMon-Version
	commit     string          // commit e data da versão (rodapé)
	built      string
	rawIndex   []byte           // index.html sem carimbo
	fl         *fleet.Fleet     // vários servidores: central e/ou conectado a um central (nil = sem)
	cl         *cleanup.Service // tela Limpeza (nil = sem)
	bk         *backup.Service  // aba Backups (nil = sem)
	ssh        *sshchat.Service // aba SSH (nil = sem)
	sshMu      sync.Mutex
	sshUnlocks map[string]sshUnlock // navegadores que digitaram o código (veja ssh.go)
	sendLimit  chatLimiter          // "enviar agora" da aba Notificações
	pauseLim   chatLimiter          // pausar/retomar app
	usersLim   chatLimiter          // criar/editar/remover usuários
}

type asset struct {
	body  []byte
	ctype string
	etag  string
}

// New monta o servidor. envAI vem das variáveis DEEPSEEK_*; settingsPath é o
// <data>/settings.json onde fica o que for configurado pela tela; nt são as
// notificações (pode ser nil).
func New(mon *monitor.Monitor, auth *Auth, trustCF bool, envAI ai.Config, settingsPath string, nt *notify.Service, fl *fleet.Fleet) *Server {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	s := &Server{mon: mon, auth: auth, trustCF: trustCF, assets: map[string]asset{}, ai: newAIState(envAI, settingsPath), nt: nt,
		version: "dev", fl: fl}
	if mon != nil && mon.Version() != "" {
		s.version = safeVersion.ReplaceAllString(mon.Version(), "")
	}
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
	if a, ok := s.assets["/index.html"]; ok {
		s.rawIndex = a.body
	}
	s.stampIndex()
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", s.static)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/login/2fa", s.login2FA)
	// outros painéis conectados a este (token do central, sem cookie)
	mux.HandleFunc("POST /api/fleet/report", s.fleetReport)
	mux.HandleFunc("POST /api/fleet/notify", s.fleetNotify)
	mux.HandleFunc("POST /api/fleet/bye", s.fleetBye)
	mux.HandleFunc("POST /api/fleet/poll", s.fleetPoll)
	mux.HandleFunc("POST /api/fleet/reply", s.fleetReplyView)
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
	// configurações do painel (IA e WhatsApp): só administradores
	admin := func(h http.HandlerFunc) http.HandlerFunc { return s.private(need(isAdmin, msgAdminOnly, h)) }
	mux.HandleFunc("GET /api/settings", admin(s.settingsGet))
	mux.HandleFunc("POST /api/settings/ai", admin(s.settingsAI))
	mux.HandleFunc("GET /api/notify", admin(s.notifyGet))
	mux.HandleFunc("POST /api/notify/config", admin(s.notifyConfig))
	mux.HandleFunc("POST /api/notify/connect", admin(s.notifyConnect))
	mux.HandleFunc("POST /api/notify/logout", admin(s.notifyLogout))
	mux.HandleFunc("GET /api/notify/groups", admin(s.notifyGroups))
	mux.HandleFunc("POST /api/notify/send", admin(s.notifySend))
	mux.HandleFunc("GET /api/fleet", admin(s.fleetGet))
	// backups: só administradores
	mux.HandleFunc("GET /api/backup", admin(s.backupGet))
	mux.HandleFunc("POST /api/backup/storage", admin(s.backupStorage))
	mux.HandleFunc("POST /api/backup/key", admin(s.backupKey))
	mux.HandleFunc("POST /api/backup/retention", admin(s.backupRetention))
	mux.HandleFunc("POST /api/backup/target", admin(s.backupTarget))
	mux.HandleFunc("POST /api/backup/run", admin(s.backupRun))
	mux.HandleFunc("GET /api/backup/objects", admin(s.backupObjects))
	mux.HandleFunc("GET /api/backup/download", admin(s.backupDownload))
	// SSH pela tela: só administradores com 2FA (o resto das travas em ssh.go)
	mux.HandleFunc("GET /api/ssh", admin(s.sshGet))
	mux.HandleFunc("GET /api/ssh/probe", admin(s.sshProbe))
	mux.HandleFunc("POST /api/ssh/target", admin(s.sshTarget))
	mux.HandleFunc("POST /api/ssh/newkey", admin(s.sshNewKey))
	mux.HandleFunc("POST /api/ssh/enable", admin(s.sshEnable))
	mux.HandleFunc("POST /api/ssh/disable", admin(s.sshDisable))
	mux.HandleFunc("POST /api/ssh/unlock", admin(s.sshUnlockRoute))
	mux.HandleFunc("POST /api/ssh/open", admin(s.sshOpen))
	mux.HandleFunc("POST /api/ssh/lock", admin(s.sshLock))
	mux.HandleFunc("POST /api/ssh/run", admin(s.sshRun))
	mux.HandleFunc("POST /api/ssh/input", admin(s.sshInput))
	mux.HandleFunc("GET /api/ssh/poll", admin(s.sshPoll))
	mux.HandleFunc("GET /api/ssh/complete", admin(s.sshComplete))
	mux.HandleFunc("GET /api/ssh/log", admin(s.sshLog))
	mux.HandleFunc("POST /api/ssh/ai", admin(s.sshAI))
	mux.HandleFunc("POST /api/fleet/tokens", admin(s.fleetTokenCreate))
	mux.HandleFunc("POST /api/fleet/tokens/revoke", admin(s.fleetTokenRevoke))
	mux.HandleFunc("POST /api/fleet/tokens/update", admin(s.fleetTokenUpdate))
	mux.HandleFunc("POST /api/fleet/connect", admin(s.fleetConnect))
	mux.HandleFunc("POST /api/fleet/disconnect", admin(s.fleetDisconnect))
	mux.HandleFunc("POST /api/fleet/whatsapp", admin(s.fleetUseWhatsApp))
	mux.HandleFunc("GET /api/fleet/servers", s.private(s.fleetServers))
	mux.HandleFunc("GET /api/fleet/view/{id}/{rest...}", s.private(s.fleetView))
	mux.HandleFunc("POST /api/fleet/view/{id}/{rest...}", s.private(s.fleetView))
	mux.HandleFunc("POST /api/fleet/share", admin(s.fleetShare))
	// ações nas aplicações e gestão de usuários: por permissão
	mux.HandleFunc("POST /api/apps/pause", s.private(need(User.CanAct, msgNoActions, s.pauseApp)))
	mux.HandleFunc("GET /api/cleanup", s.private(s.cleanupGet))
	mux.HandleFunc("POST /api/cleanup/run", s.private(need(User.CanClean, msgNoClean, s.cleanupRun)))
	mux.HandleFunc("POST /api/cleanup/auto", s.private(need(User.CanClean, msgNoClean, s.cleanupAuto)))
	manage := func(h http.HandlerFunc) http.HandlerFunc { return s.private(need(User.CanManage, msgNoManage, h)) }
	mux.HandleFunc("GET /api/users", manage(s.usersList))
	mux.HandleFunc("POST /api/users", manage(s.usersCreate))
	mux.HandleFunc("POST /api/users/update", manage(s.usersUpdate))
	mux.HandleFunc("POST /api/users/reset", manage(s.usersReset))
	mux.HandleFunc("POST /api/users/delete", manage(s.usersDelete))
	mux.HandleFunc("POST /api/users/2fa-off", manage(s.usersTwoFAOff))
	// verificação em duas etapas de quem está logado
	mux.HandleFunc("POST /api/2fa/setup", s.private(s.twofaSetup))
	mux.HandleFunc("POST /api/2fa/enable", s.private(s.twofaEnable))
	mux.HandleFunc("POST /api/2fa/disable", s.private(s.twofaDisable))
	mux.HandleFunc("POST /api/2fa/recovery", s.private(s.twofaRecovery))
	mux.HandleFunc("POST /api/2fa/dismiss", s.private(s.twofaDismiss))
	return secureHeaders(s.versionHeader(withGzip(mux)))
}

var safeVersion = regexp.MustCompile(`[^A-Za-z0-9._-]`)

var (
	safeCommit = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	safeDate   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9]{2}:[0-9]{2})$`)
)

// WithBuild informa o commit e a data da versão (vão para o rodapé da tela).
// Valores fora do formato são ignorados.
func (s *Server) WithBuild(commit, built string) *Server {
	if safeCommit.MatchString(commit) {
		s.commit = commit
	}
	if safeDate.MatchString(built) {
		s.built = built
	}
	s.stampIndex()
	return s
}

// stampIndex põe a versão no index.html: nas metas vpmon-version (a tela
// compara com a do servidor), vpmon-commit e vpmon-built (rodapé) e nos
// endereços dos arquivos (?v=), para o navegador nunca juntar uma página nova
// com um app.js velho.
func (s *Server) stampIndex() {
	a, ok := s.assets["/index.html"]
	if !ok || s.rawIndex == nil {
		return
	}
	html := string(s.rawIndex)
	for _, f := range []string{"app.js", "app.css", "theme.js", "uPlot.iife.min.js", "uPlot.min.css", "manifest.webmanifest"} {
		html = strings.ReplaceAll(html, `"`+f+`"`, `"`+f+"?v="+s.version+`"`)
	}
	meta := `<meta name="vpmon-version" content="` + s.version + `">`
	if s.commit != "" {
		meta += "\n" + `<meta name="vpmon-commit" content="` + s.commit + `">`
	}
	if s.built != "" {
		meta += "\n" + `<meta name="vpmon-built" content="` + s.built + `">`
	}
	html = strings.Replace(html, `<meta charset="utf-8">`, `<meta charset="utf-8">`+"\n"+meta, 1)
	sum := sha256.Sum256([]byte(html))
	s.assets["/index.html"] = asset{body: []byte(html), ctype: a.ctype, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// versionHeader manda a versão em toda resposta: uma tela aberta há tempos
// percebe que o painel foi atualizado e se recarrega.
func (s *Server) versionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-VPMon-Version", s.version)
		next.ServeHTTP(w, r)
	})
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
func (s *Server) security(kind, ip, user string) {
	if s.nt != nil {
		s.nt.Security(kind, ip, user)
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
		s.security("login_fail", ip, "")
		time.Sleep(600 * time.Millisecond)
		apiError(w, http.StatusUnauthorized, "bad_credentials", "Usuário ou senha incorretos.")
		return
	}
	if u.TOTP != nil && !s.auth.Trusted(r, u) {
		// senha certa; falta o código do app (o bilhete liga uma coisa à outra por 5 min)
		writeJSON(w, http.StatusOK, map[string]any{"need2fa": true, "ticket": s.auth.Ticket(u.Name), "user": u.Name})
		return
	}
	s.auth.Reset(ip)
	s.auth.Issue(w, u.Name)
	s.auth.MarkLogin(u.Name)
	slog.Info("login", "ip", ip, "usuario", u.Name)
	s.security("login", ip, u.Name)
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Name, "mustChange": u.MustChange})
}

// login2FA é o segundo passo: o código do app (ou um de recuperação).
func (s *Server) login2FA(w http.ResponseWriter, r *http.Request) {
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
		Ticket   string `json:"ticket"`
		Code     string `json:"code"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	u, ok := s.auth.CheckTicket(body.Ticket)
	if !ok {
		apiError(w, http.StatusUnauthorized, "ticket_expired", "O tempo para digitar o código acabou. Entre de novo.")
		return
	}
	used, left, err := s.auth.VerifySecond(u.Name, body.Code)
	if err != nil {
		s.auth.Fail(ip)
		slog.Warn("código do 2FA errado", "ip", ip, "usuario", u.Name)
		s.security("login_fail", ip, "")
		time.Sleep(600 * time.Millisecond)
		apiError(w, http.StatusUnauthorized, "bad_code", "Código inválido ou vencido.")
		return
	}
	s.auth.Reset(ip)
	s.auth.Issue(w, u.Name)
	if body.Remember {
		s.auth.IssueTrust(w, u.Name)
	}
	s.auth.MarkLogin(u.Name)
	slog.Info("login com 2FA", "ip", ip, "usuario", u.Name, "com", used)
	s.security("login", ip, u.Name)
	if used == "recovery" {
		s.security("recovery_used", ip, u.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Name, "mustChange": u.MustChange,
		"recoveryUsed": used == "recovery", "recoveryLeft": left})
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
		"admin": u.Admin, "actions": u.CanAct(), "manage": u.CanManage(), "clean": u.CanClean(), "twoFA": u.TwoFA()})
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
	s.security("password", ip, u.Name)
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
	who := userOf(r).Name
	slog.Info("app "+verb+" pela tela", "app", body.App, "conteineres", strings.Join(names, ","), "por", who, "ip", ip)
	if s.nt != nil && len(names) > 0 {
		emoji := map[bool]string{true: "⏸️", false: "▶️"}[body.Pause]
		s.nt.Audit("pauses", fmt.Sprintf("%s %s pela tela", body.App, verb), fmt.Sprintf("%s *%s %s pelo painel · %s*\nContêineres: %s.\nPor %s (IP %s).",
			emoji, body.App, verb, s.mon.Overview().Server.Name, strings.Join(names, ", "), who, ip))
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
