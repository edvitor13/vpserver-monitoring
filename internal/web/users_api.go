package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Rotas de usuários (Configurações → Usuários) e o controle de permissão das rotas.

// need exige uma permissão do usuário logado (use dentro de private).
func need(ok func(User) bool, msg string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ok(userOf(r)) {
			apiError(w, http.StatusForbidden, "forbidden", msg)
			return
		}
		h(w, r)
	}
}

func isAdmin(u User) bool { return u.Admin }

const (
	msgAdminOnly  = "Só administradores mexem nisso."
	msgNoActions  = "Seu usuário não pode pausar nem retomar aplicações."
	msgNoManage   = "Seu usuário não pode gerenciar usuários."
	msgUsersBusy  = "Muitas mudanças em pouco tempo. Espere alguns minutos."
	userBodyLimit = 2048
)

// userErr traduz os erros do armazenamento para a API.
func userErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		apiError(w, http.StatusForbidden, "forbidden", capitalize(err.Error())+".")
	case errors.Is(err, ErrSelf):
		apiError(w, http.StatusBadRequest, "self", capitalize(err.Error())+".")
	case errors.Is(err, ErrLastAdmin):
		apiError(w, http.StatusBadRequest, "last_admin", capitalize(err.Error())+".")
	case errors.Is(err, ErrUserExists):
		apiError(w, http.StatusConflict, "user_exists", capitalize(err.Error())+".")
	case errors.Is(err, ErrBadUser):
		apiError(w, http.StatusBadRequest, "bad_user", capitalize(err.Error())+".")
	case errors.Is(err, ErrNoUser):
		apiError(w, http.StatusNotFound, "not_found", "Usuário não encontrado.")
	default:
		slog.Error("usuários: não consegui gravar", "err", err)
		apiError(w, http.StatusInternalServerError, "store_failed", "Não consegui gravar a mudança.")
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

type userBody struct {
	Name    string `json:"name"`
	Admin   bool   `json:"admin"`
	Actions bool   `json:"actions"`
	Manage  bool   `json:"manage"`
	Clean   bool   `json:"clean"`
}

func (s *Server) readUserBody(w http.ResponseWriter, r *http.Request) (userBody, bool) {
	var b userBody
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return b, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&b); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return b, false
	}
	if !s.usersLim.allow() {
		apiError(w, http.StatusTooManyRequests, "too_many_actions", msgUsersBusy)
		return b, false
	}
	return b, true
}

func permsText(v UserView) string {
	if v.Admin {
		return "administrador"
	}
	var p []string
	if v.Actions {
		p = append(p, "ações nas apps")
	}
	if v.Manage {
		p = append(p, "gestão de usuários")
	}
	if v.Clean {
		p = append(p, "limpeza do disco")
	}
	if len(p) == 0 {
		return "só leitura"
	}
	return strings.Join(p, ", ")
}

// audit registra a mudança no log e avisa no WhatsApp ("Segurança do painel").
func (s *Server) audit(r *http.Request, title, text string) {
	slog.Info(title, "por", userOf(r).Name, "ip", clientIP(r, s.trustCF))
	if s.nt != nil {
		s.nt.Audit("security", title, fmt.Sprintf("👤 *%s*\n%s\nPor %s (IP %s).", title, text, userOf(r).Name, clientIP(r, s.trustCF)))
	}
}

func (s *Server) usersList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"users": s.auth.Users(), "me": userOf(r).Name})
}

func (s *Server) usersCreate(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUserBody(w, r)
	if !ok {
		return
	}
	v, pass, err := s.auth.CreateUser(userOf(r), b.Name, Perms{Admin: b.Admin, Actions: b.Actions, Manage: b.Manage, Clean: b.Clean})
	if err != nil {
		userErr(w, err)
		return
	}
	s.audit(r, "Usuário criado", fmt.Sprintf("%s, com %s.", v.Name, permsText(v)))
	writeJSON(w, http.StatusOK, map[string]any{"user": v, "password": pass})
}

func (s *Server) usersUpdate(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUserBody(w, r)
	if !ok {
		return
	}
	v, err := s.auth.UpdateUser(userOf(r), b.Name, Perms{Admin: b.Admin, Actions: b.Actions, Manage: b.Manage, Clean: b.Clean})
	if err != nil {
		userErr(w, err)
		return
	}
	s.audit(r, "Permissões alteradas", fmt.Sprintf("%s agora tem %s.", v.Name, permsText(v)))
	writeJSON(w, http.StatusOK, map[string]any{"user": v})
}

func (s *Server) usersReset(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUserBody(w, r)
	if !ok {
		return
	}
	pass, err := s.auth.ResetUser(userOf(r), b.Name)
	if err != nil {
		userErr(w, err)
		return
	}
	s.audit(r, "Senha provisória gerada", b.Name+" vai precisar criar uma senha nova no próximo acesso.")
	writeJSON(w, http.StatusOK, map[string]any{"password": pass})
}

func (s *Server) usersDelete(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUserBody(w, r)
	if !ok {
		return
	}
	if err := s.auth.DeleteUser(userOf(r), b.Name); err != nil {
		userErr(w, err)
		return
	}
	s.audit(r, "Usuário removido", b.Name+" não acessa mais o painel.")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
