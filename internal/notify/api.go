package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// O que a tela (aba Notificações) usa.

type View struct {
	Installed bool       `json:"installed"` // há Evolution configurada (VPMON_WA_KEY)
	Status    Status     `json:"status"`
	Config    Config     `json:"config"`
	Catalog   []Kind     `json:"catalog"`
	Log       []LogEntry `json:"log"`
	TZ        string     `json:"tz"`
	Held      int        `json:"held"`    // mensagens seguradas pelo silêncio
	Pending   int        `json:"pending"` // alertas valendo que ainda não foram avisados
	Running   []string   `json:"running"` // análises da IA em andamento
	// Relay: os avisos daqui saem pelo WhatsApp do painel central (nome dele)
	Relay string `json:"relay,omitempty"`
}

// View devolve a configuração e a situação (consultando o WhatsApp agora).
func (s *Service) View(ctx context.Context) View {
	var st Status
	if s.wa.Configured() {
		c, cancel := context.WithTimeout(ctx, 6*time.Second)
		st = s.wa.Status(c)
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wa.Configured() {
		s.status, s.statusAt = st, s.now()
	}
	v := View{Installed: s.wa.Configured(), Status: st, Config: s.cfg, Catalog: Catalog, TZ: s.loc.String(),
		Held: len(s.st.Held), Log: make([]LogEntry, 0, len(s.st.Log)), Running: []string{}}
	if v.Config.Recipients == nil {
		v.Config.Recipients = []Recipient{}
	}
	for i := len(s.st.Log) - 1; i >= 0; i-- { // mais recente primeiro
		v.Log = append(v.Log, s.st.Log[i])
	}
	for _, t := range s.st.Active {
		if !t.Sent {
			v.Pending++
		}
	}
	for k := range s.running {
		v.Running = append(v.Running, k)
	}
	if s.relayActiveLocked() {
		v.Relay = s.relay.Central()
		if v.Relay == "" {
			v.Relay = "painel central"
		}
	}
	return v
}

var (
	reGroup  = regexp.MustCompile(`^[0-9-]{10,40}@g\.us$`)
	reDigits = regexp.MustCompile(`\D`)
)

// NormalizeNumber deixa só os dígitos e confere o tamanho (com DDI: 8 a 15 dígitos).
func NormalizeNumber(v string) (string, error) {
	v = strings.TrimSpace(v)
	if reGroup.MatchString(v) {
		return v, nil
	}
	d := strings.TrimLeft(reDigits.ReplaceAllString(v, ""), "0")
	if len(d) < 8 || len(d) > 15 {
		return "", fmt.Errorf("número inválido: %q (use o DDI, ex.: +55 11 91234-5678)", v)
	}
	return d, nil
}

// SaveConfig valida e grava a configuração da tela.
func (s *Service) SaveConfig(c Config) (Config, error) {
	if len(c.Recipients) > maxRecipients {
		return Config{}, fmt.Errorf("no máximo %d destinos", maxRecipients)
	}
	seen := map[string]bool{}
	var rs []Recipient
	for _, r := range c.Recipients {
		id, err := NormalizeNumber(r.ID)
		if err != nil {
			return Config{}, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(r.Name)
		if r := []rune(name); len(r) > 40 {
			name = string(r[:40])
		}
		rs = append(rs, Recipient{ID: id, Name: name})
	}
	c.Recipients = rs
	ev := DefaultEvents()
	for k := range ev {
		if v, ok := c.Events[k]; ok {
			ev[k] = v
		}
	}
	c.Events = ev
	for _, v := range []string{c.DailyAt, c.QuietFrom, c.QuietTo} {
		if !hhmm.MatchString(v) {
			return Config{}, fmt.Errorf("horário inválido: %q (use HH:MM)", v)
		}
	}
	c.PanelURL = strings.TrimRight(strings.TrimSpace(c.PanelURL), "/")
	if c.PanelURL != "" && (len(c.PanelURL) > 200 || strings.ContainsAny(c.PanelURL, " \n\t\"'<>") ||
		!(strings.HasPrefix(c.PanelURL, "https://") || strings.HasPrefix(c.PanelURL, "http://"))) {
		return Config{}, errors.New("endereço do painel inválido")
	}
	s.mu.Lock()
	if !PublicOrigin(c.PanelURL) && PublicOrigin(s.cfg.PanelURL) {
		c.PanelURL = s.cfg.PanelURL // salvou entrando pelo túnel SSH (localhost): mantém o endereço público
	}
	s.cfg = c
	s.dirty = true
	s.mu.Unlock()
	s.save(true)
	return c, nil
}

func (s *Service) needWA() error {
	if !s.wa.Configured() {
		return errors.New("o WhatsApp não está instalado neste servidor (falta o contêiner vpserver-whatsapp)")
	}
	return nil
}

// Connect gera o QR (ou, com número, o código de pareamento).
func (s *Service) Connect(ctx context.Context, number string) (QR, error) {
	if err := s.needWA(); err != nil {
		return QR{}, err
	}
	if number != "" {
		n, err := NormalizeNumber(number)
		if err != nil || strings.Contains(n, "@") {
			return QR{}, errors.New("número inválido para o código de pareamento (use o DDI)")
		}
		number = n
	}
	return s.wa.Connect(ctx, number)
}

func (s *Service) Logout(ctx context.Context) error {
	if err := s.needWA(); err != nil {
		return err
	}
	err := s.wa.Logout(ctx)
	s.mu.Lock()
	s.status.State = "close"
	s.mu.Unlock()
	return err
}

func (s *Service) Groups(ctx context.Context) ([]Group, error) {
	if err := s.needWA(); err != nil {
		return nil, err
	}
	return s.wa.Groups(ctx)
}

// SendNow manda na hora, pela tela: "test" ou um resumo/análise do catálogo.
// O teste espera o envio; as análises da IA rodam em segundo plano.
func (s *Service) SendNow(ctx context.Context, kind string) (string, error) {
	s.mu.Lock()
	cfg, relay := s.cfg, s.relayActiveLocked()
	s.mu.Unlock()
	if relay { // pelo WhatsApp do painel central: lá ele confere conexão e destinos
		return s.sendNowKind(ctx, kind, cfg)
	}
	if err := s.needWA(); err != nil {
		return "", err
	}
	if len(cfg.Recipients) == 0 {
		return "", errors.New("adicione alguém em \"Para quem enviar\" e salve")
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	st := s.wa.Status(c)
	cancel()
	s.mu.Lock()
	s.status, s.statusAt = st, s.now()
	s.mu.Unlock()
	if !st.Connected() {
		return "", errors.New("o WhatsApp está desconectado: conecte pelo QR code primeiro")
	}
	return s.sendNowKind(ctx, kind, cfg)
}

func (s *Service) sendNowKind(ctx context.Context, kind string, cfg Config) (string, error) {
	now := s.now()
	server := s.src.Overview().Server.Name
	s.mu.Lock()
	relay := s.relayActiveLocked()
	s.mu.Unlock()
	switch kind {
	case "test":
		text := fmt.Sprintf("✅ *Teste do VPServer · %s*\nAs notificações deste painel vão chegar aqui.%s", server, s.link("notify"))
		okN, errs := s.sendVia(ctx, relay, cfg.Recipients, text)
		e := LogEntry{T: now.Unix(), Kind: "test", Title: "Mensagem de teste", Text: text, Status: "sent"}
		if len(errs) > 0 {
			e.Status, e.Error = "partial", strings.Join(errs, "; ")
			if okN == 0 {
				e.Status = "failed"
			}
		}
		s.logEntry(e)
		if okN == 0 {
			return "", errors.New(strings.Join(errs, "; "))
		}
		if len(errs) > 0 {
			return fmt.Sprintf("Enviado para %d de %d destinos. Falhou: %s", okN, okN+len(errs), strings.Join(errs, "; ")), nil
		}
		return "Mensagem de teste enviada.", nil
	case "daily", "weekly", "monthly", "ai_daily", "ai_weekly", "ai_logs":
		k, _ := kindOf(kind)
		if k.AI && s.aiClient() == nil {
			return "", errors.New("configure a IA em Configurações → IA para usar as análises")
		}
		async := k.AI || (kind == "daily" && s.isOn("ai_daily") && s.aiClient() != nil)
		s.runScheduled(ctx, kind, now, true)
		if async {
			return "A IA está montando a análise; a mensagem chega em até 2 minutos.", nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if n := len(s.st.Log); n > 0 && s.st.Log[n-1].Kind == kind && s.st.Log[n-1].Status == "failed" {
			return "", errors.New(s.st.Log[n-1].Error)
		}
		return "Enviado.", nil
	}
	return "", errors.New("tipo desconhecido")
}

// PublicOrigin diz se o endereço serve de link no celular: https e um nome de
// domínio (não localhost nem IP, como o acesso pelo túnel SSH).
func PublicOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return false
	}
	host := u.Hostname()
	return host != "localhost" && !strings.HasSuffix(host, ".localhost") && net.ParseIP(host) == nil && strings.Contains(host, ".")
}

// SeenOrigin recebe o endereço por onde alguém logado abriu o painel. Se for
// público e diferente do salvo, vira o link das mensagens: acompanha troca de
// domínio e o endereço do Quick Tunnel, que muda a cada reinício.
func (s *Service) SeenOrigin(origin string) {
	if !PublicOrigin(origin) {
		return
	}
	s.mu.Lock()
	changed := s.cfg.PanelURL != origin
	if changed {
		s.cfg.PanelURL = origin
		s.dirty = true
	}
	s.mu.Unlock()
	if changed {
		s.save(true)
	}
}

// PanelURL é o endereço público deste painel (o que vai nos links das mensagens).
func (s *Service) PanelURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.PanelURL
}

// WhatsAppMode diz por onde saem os avisos: "own" (WhatsApp deste servidor),
// "central" (emprestado do painel central) ou "off".
func (s *Service) WhatsAppMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.relayActiveLocked():
		return "central"
	case s.wa.Configured() && s.status.Connected():
		return "own"
	}
	return "off"
}
