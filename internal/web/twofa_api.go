package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Rotas do 2FA de quem está logado (Configurações → Minha conta, primeiro
// acesso e a recomendação depois do login) e a do admin para outra pessoa.

func (s *Server) issuer() string {
	name := ""
	if s.mon != nil {
		name = strings.ReplaceAll(s.mon.Overview().Server.Name, ":", " ")
	}
	if name == "" {
		return "VPServer"
	}
	return "VPServer (" + name + ")"
}

// twofaBody lê o corpo de uma rota do 2FA com o mesmo freio de tentativas do login.
func (s *Server) twofaBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return false
	}
	if blocked, wait := s.auth.Blocked(clientIP(r, s.trustCF)); blocked {
		apiError(w, http.StatusTooManyRequests, "too_many_attempts",
			"Muitas tentativas erradas. Tente de novo em "+strconv.Itoa(int(wait.Minutes())+1)+" min.")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return false
	}
	return true
}

// twofaErr responde os erros de código e senha (que contam como tentativa errada).
func (s *Server) twofaErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrBadCode):
		s.auth.Fail(clientIP(r, s.trustCF))
		apiError(w, http.StatusBadRequest, "bad_code", "Código inválido ou vencido. Confira o relógio do celular e use o código que está na tela agora.")
	case errors.Is(err, ErrBadCurrent):
		s.auth.Fail(clientIP(r, s.trustCF))
		apiError(w, http.StatusBadRequest, "bad_current", "A senha não confere.")
	case errors.Is(err, ErrNoPending):
		apiError(w, http.StatusBadRequest, "no_pending", "O QR code venceu. Gere de novo.")
	case errors.Is(err, ErrNo2FA):
		apiError(w, http.StatusBadRequest, "no_2fa", "A verificação em duas etapas não está ligada.")
	default:
		userErr(w, err)
	}
}

func (s *Server) twofaSetup(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !s.twofaBody(w, r, &body) {
		return
	}
	u := userOf(r)
	if u.TOTP != nil {
		apiError(w, http.StatusBadRequest, "already_on", "A verificação em duas etapas já está ligada.")
		return
	}
	secret, err := s.auth.StartTOTP(u.Name)
	if err != nil {
		s.twofaErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": groupSecret(secret), "uri": otpauthURI(s.issuer(), u.Name, secret)})
}

func (s *Server) twofaEnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !s.twofaBody(w, r, &body) {
		return
	}
	u := userOf(r)
	codes, err := s.auth.EnableTOTP(u.Name, body.Code)
	if err != nil {
		s.twofaErr(w, r, err)
		return
	}
	s.auth.Issue(w, u.Name) // as outras sessões caíram; esta continua com um cookie novo
	s.security("2fa_on", clientIP(r, s.trustCF), u.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "codes": codes})
}

func (s *Server) twofaDisable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !s.twofaBody(w, r, &body) {
		return
	}
	u := userOf(r)
	if err := s.auth.DisableTOTP(u.Name, body.Password, body.Code); err != nil {
		s.twofaErr(w, r, err)
		return
	}
	s.auth.Issue(w, u.Name)
	s.security("2fa_off", clientIP(r, s.trustCF), u.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) twofaRecovery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !s.twofaBody(w, r, &body) {
		return
	}
	codes, err := s.auth.NewRecoveryCodes(userOf(r).Name, body.Code)
	if err != nil {
		s.twofaErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "codes": codes})
}

// twofaDismiss anota que a pessoa viu a recomendação e escolheu "agora não".
func (s *Server) twofaDismiss(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	if err := s.auth.MarkAsked(userOf(r).Name); err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// usersTwoFAOff desliga o 2FA de outra pessoa (celular perdido).
func (s *Server) usersTwoFAOff(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUserBody(w, r)
	if !ok {
		return
	}
	if err := s.auth.DisableTOTPFor(userOf(r), b.Name); err != nil {
		s.twofaErr(w, r, err)
		return
	}
	s.audit(r, "Verificação em duas etapas desligada", b.Name+" entra só com a senha até ligar de novo.")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
