package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
)

// Configuração da IA feita pela tela (Configurações → IA, ou o passo 2 do
// primeiro acesso). Fica em <data>/settings.json (0600) e vale mais que as
// variáveis DEEPSEEK_* do .env; removendo pela tela, volta a valer o .env.
// A chave nunca volta para o navegador: só aparece mascarada.

type aiSettings struct {
	Key      string `json:"key"`
	Host     string `json:"host,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Model    string `json:"model,omitempty"`
	Updated  int64  `json:"updated"`
}

type settingsFile struct {
	DeepSeek *aiSettings `json:"deepseek,omitempty"`
}

type aiState struct {
	mu     sync.RWMutex
	client *ai.Client
	cfg    ai.Config
	source string // panel | env | "" (desligada)
	env    ai.Config
	path   string
}

func newAIState(env ai.Config, path string) *aiState {
	s := &aiState{env: env, path: path}
	if f, ok := s.load(); ok && f.DeepSeek != nil && f.DeepSeek.Key != "" {
		s.set(s.fromPanel(*f.DeepSeek), "panel")
	} else if env.Enabled() {
		s.set(env, "env")
	}
	return s
}

func (s *aiState) fromPanel(p aiSettings) ai.Config {
	c := ai.Config{APIKey: p.Key, Host: p.Host, Endpoint: p.Endpoint, Model: p.Model}
	if c.Host == "" {
		c.Host = s.env.Host
	}
	if c.Endpoint == "" {
		c.Endpoint = s.env.Endpoint
	}
	if c.Model == "" {
		c.Model = s.env.Model
	}
	return c
}

func (s *aiState) set(c ai.Config, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.Enabled() {
		s.client, s.cfg, s.source = nil, ai.Config{}, ""
		return
	}
	s.client, s.cfg, s.source = ai.NewClient(c), c, source
}

func (s *aiState) get() *ai.Client {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

func (s *aiState) load() (settingsFile, bool) {
	var f settingsFile
	if s.path == "" {
		return f, false
	}
	b, err := os.ReadFile(s.path)
	if err != nil || json.Unmarshal(b, &f) != nil {
		return f, false
	}
	return f, true
}

func (s *aiState) save(f settingsFile) error {
	if s.path == "" {
		return errors.New("sem pasta de dados")
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := filepath.Join(filepath.Dir(s.path), ".settings.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func mask(key string) string {
	if len(key) <= 8 {
		return "••••"
	}
	return key[:3] + "…" + key[len(key)-4:]
}

type aiView struct {
	Enabled      bool   `json:"enabled"`
	Source       string `json:"source"` // panel | env | ""
	Model        string `json:"model"`
	Host         string `json:"host"`
	Endpoint     string `json:"endpoint"`
	Key          string `json:"key"` // mascarada
	EnvAvailable bool   `json:"envAvailable"`
}

func (s *aiState) view() aiView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := aiView{Enabled: s.client != nil, Source: s.source, EnvAvailable: s.env.Enabled(),
		Model: s.env.Model, Host: s.env.Host, Endpoint: s.env.Endpoint}
	if s.client != nil {
		v.Model, v.Host, v.Endpoint, v.Key = s.cfg.Model, s.cfg.Host, s.cfg.Endpoint, mask(s.cfg.APIKey)
	}
	return v
}

// --- HTTP ------------------------------------------------------------------------------

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ai": s.ai.view()})
}

// settingsAI salva (testando antes), testa ou remove a configuração da IA.
func (s *Server) settingsAI(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "bad_origin", "Requisição recusada.")
		return
	}
	var body struct {
		Action   string `json:"action"` // save | test | remove
		Key      string `json:"key"`
		Model    string `json:"model"`
		Host     string `json:"host"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Dados inválidos.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	st := s.ai

	switch body.Action {
	case "remove":
		f, _ := st.load()
		f.DeepSeek = nil
		if err := st.save(f); err != nil {
			apiError(w, http.StatusInternalServerError, "store_failed", "Não consegui gravar a configuração.")
			return
		}
		if st.env.Enabled() {
			st.set(st.env, "env")
		} else {
			st.set(ai.Config{}, "")
		}
		slog.Info("IA: configuração da tela removida")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ai": st.view()})

	case "test":
		cli := st.get()
		if cli == nil {
			apiError(w, http.StatusBadRequest, "ai_disabled", "A IA não está configurada.")
			return
		}
		if err := cli.Check(ctx); err != nil {
			apiError(w, http.StatusBadGateway, "ai_check_failed", aiErrMessage(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ai": st.view()})

	case "save":
		key := strings.TrimSpace(body.Key)
		st.mu.RLock()
		cur, curSource := st.cfg, st.source
		st.mu.RUnlock()
		if key == "" && curSource == "panel" {
			key = cur.APIKey // só trocou o modelo: mantém a chave já salva
		}
		if key == "" || strings.ContainsAny(key, " \n\t") || len(key) > 200 {
			apiError(w, http.StatusBadRequest, "bad_key", "Cole a chave da DeepSeek (começa com sk-).")
			return
		}
		p := aiSettings{Key: key, Host: strings.TrimSpace(body.Host), Endpoint: strings.TrimSpace(body.Endpoint),
			Model: strings.TrimSpace(body.Model), Updated: time.Now().Unix()}
		cfg := st.fromPanel(p)
		if err := ai.NewClient(cfg).Check(ctx); err != nil {
			apiError(w, http.StatusBadRequest, "ai_check_failed", aiErrMessage(err))
			return
		}
		f, _ := st.load()
		f.DeepSeek = &p
		if err := st.save(f); err != nil {
			apiError(w, http.StatusInternalServerError, "store_failed", "Não consegui gravar a configuração.")
			return
		}
		st.set(cfg, "panel")
		slog.Info("IA configurada pela tela", "modelo", cfg.Model)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ai": st.view()})

	default:
		apiError(w, http.StatusBadRequest, "bad_request", "Ação inválida.")
	}
}

func aiErrMessage(err error) string {
	var ae *ai.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "Não consegui falar com a DeepSeek: " + err.Error()
}
