// Package notify manda avisos e resumos do servidor pelo WhatsApp, por uma
// Evolution API (contêiner vpserver-whatsapp) na rede interna do painel. Tudo
// se configura pela tela: conexão (QR code), para quem mandar e o que avisar.
//
// Os alertas são os mesmos da tela (monitor.Alert, pela chave estável): um
// alerta vira mensagem quando começa (depois de durar um pouco), quando piora
// e quando resolve. Resumos e análises da IA saem no horário escolhido.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

const (
	maxPerHour     = 30 // mensagens por hora (fora os testes): segura rajada e risco de bloqueio
	maxRecipients  = 10
	maxLog         = 60
	aiPerDay       = 6 // diagnósticos de incidente por dia
	resolveAfter   = 3 * time.Minute
	remindAfter    = 12 * time.Hour
	scheduleWindow = 3 * time.Hour // resumo atrasado (painel fora do ar no horário) ainda sai até 3 h depois
	aiTimeout      = 4 * time.Minute
)

// Source é o que o serviço precisa do monitor.
type Source interface {
	Overview() monitor.Overview
	Summary(kind string, now time.Time) string
	PeriodOf(kind string, now time.Time) monitor.Period
	AIContext() string
	AI() ai.Executor
}

// WhatsApp é a Evolution (trocável nos testes).
type WhatsApp interface {
	Configured() bool
	Status(ctx context.Context) Status
	Connect(ctx context.Context, number string) (QR, error)
	Logout(ctx context.Context) error
	Send(ctx context.Context, to, text string) error
	Groups(ctx context.Context) ([]Group, error)
}

type Recipient struct {
	ID   string `json:"id"` // número só com dígitos, com DDI (5511...), ou grupo (...@g.us)
	Name string `json:"name"`
}

type Config struct {
	Enabled    bool            `json:"enabled"`
	Recipients []Recipient     `json:"recipients"`
	Events     map[string]bool `json:"events"`
	DailyAt    string          `json:"dailyAt"`   // "08:00": resumos e análises agendadas
	Quiet      bool            `json:"quiet"`     // no silêncio, só o urgente sai na hora
	QuietFrom  string          `json:"quietFrom"` // "22:00"
	QuietTo    string          `json:"quietTo"`   // "07:00"
	PanelURL   string          `json:"panelUrl"`  // link nas mensagens (a tela manda o endereço dela)
}

func defaultConfig() Config {
	return Config{Enabled: true, Events: DefaultEvents(), DailyAt: "08:00", Quiet: true, QuietFrom: "22:00", QuietTo: "07:00"}
}

type LogEntry struct {
	T      int64  `json:"t"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Text   string `json:"text"`
	Status string `json:"status"` // sent | partial | failed | held | skipped
	Error  string `json:"error,omitempty"`
}

type tracked struct {
	Key       string `json:"key"`
	Cat       string `json:"cat"`
	Level     string `json:"level"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	Target    string `json:"target,omitempty"`
	First     int64  `json:"first"`
	Last      int64  `json:"last"`
	Sent      bool   `json:"sent"`
	SentLevel string `json:"sentLevel,omitempty"`
	SentAt    int64  `json:"sentAt,omitempty"`
}

type appSeen struct {
	Name     string            `json:"name"`
	First    int64             `json:"first"`
	Last     int64             `json:"last"`
	Notified bool              `json:"notified"` // "app nova" já avisada (ou já existia quando o painel começou)
	Images   map[string]string `json:"images"`   // serviço → imagem (para perceber deploy)
}

type heldMsg struct {
	T     int64  `json:"t"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type state struct {
	Active   map[string]*tracked `json:"active"`
	Done     map[string]string   `json:"done"` // tipo → último período enviado
	Apps     map[string]*appSeen `json:"apps"`
	Seeded   bool                `json:"seeded"`
	Log      []LogEntry          `json:"log"`
	Sends    []int64             `json:"sends"`
	Held     []heldMsg           `json:"held"`
	AIDay    string              `json:"aiDay"`
	AICount  int                 `json:"aiCount"`
	Fails    []int64             `json:"fails"`
	FailSent int64               `json:"failSent"`
}

type fileData struct {
	Config Config `json:"config"`
	State  state  `json:"state"`
}

// message é uma mensagem pronta para sair.
type message struct {
	kind   string
	title  string
	text   string
	urgent bool     // sai mesmo no horário de silêncio
	manual bool     // pedido pela tela (teste, "enviar agora"): ignora silêncio, limite e o desligado
	keys   []string // alertas que ela avisa (marcados como avisados se sair)
	apps   []string // apps novas que ela avisa
	done   string   // "tipo=período" marcado se sair
	after  func()   // roda depois de sair (ex.: diagnóstico da IA)
}

type Service struct {
	src  Source
	wa   WhatsApp
	loc  *time.Location
	path string
	self string // projeto do próprio painel (fora do aviso de deploy)
	now  func() time.Time

	mu       sync.Mutex
	cfg      Config
	st       state
	status   Status
	statusAt time.Time
	failLog  map[string]int64 // motivo de falha → quando foi registrado (não repete toda volta)
	running  map[string]bool  // análises da IA em andamento
	dirty    bool
	saved    time.Time
	aiFn     func() *ai.Client
	sendMu   sync.Mutex // um envio por vez
}

// New carrega a configuração de <dataDir>/notify.json.
func New(src Source, wa WhatsApp, loc *time.Location, dataDir, selfProject string) *Service {
	s := &Service{src: src, wa: wa, loc: loc, path: filepath.Join(dataDir, "notify.json"), self: selfProject,
		now: time.Now, cfg: defaultConfig(), failLog: map[string]int64{}, running: map[string]bool{}}
	if b, err := os.ReadFile(s.path); err == nil {
		var f fileData
		if json.Unmarshal(b, &f) == nil {
			s.cfg, s.st = f.Config, f.State
			for k, v := range DefaultEvents() { // tipo novo numa versão nova: entra com o padrão
				if _, ok := s.cfg.Events[k]; !ok {
					if s.cfg.Events == nil {
						s.cfg.Events = map[string]bool{}
					}
					s.cfg.Events[k] = v
				}
			}
		}
	}
	if s.st.Active == nil {
		s.st.Active = map[string]*tracked{}
	}
	if s.st.Done == nil {
		s.st.Done = map[string]string{}
	}
	if s.st.Apps == nil {
		s.st.Apps = map[string]*appSeen{}
	}
	return s
}

// SetAI liga as análises da IA (a IA pode ser trocada pela tela a qualquer momento).
func (s *Service) SetAI(f func() *ai.Client) {
	s.mu.Lock()
	s.aiFn = f
	s.mu.Unlock()
}

func (s *Service) aiClient() *ai.Client {
	s.mu.Lock()
	f := s.aiFn
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	return f()
}

// Run confere tudo a cada minuto (a primeira vez depois de 90 s, quando o
// monitor já tem as médias de 5 min que os alertas usam).
func (s *Service) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			s.save(true)
			return
		case <-t.C:
		}
	}
}

// Tick é uma volta: estado do WhatsApp, alertas, apps, resumos e envio.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	var st Status
	if s.wa.Configured() {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		st = s.wa.Status(c)
		cancel()
	}
	o := s.src.Overview()

	s.mu.Lock()
	s.status, s.statusAt = st, now
	msgs := s.trackAlerts(now, o.Alerts, o.Server.Name)
	msgs = append(msgs, s.trackApps(now, o)...)
	due := s.dueScheduled(now)
	s.mu.Unlock()

	for _, m := range msgs {
		s.deliver(ctx, m)
	}
	for _, kind := range due {
		s.runScheduled(ctx, kind, now, false)
	}
	s.flushHeld(ctx)
	s.save(false)
}

// --- alertas ---------------------------------------------------------------------------

var levelRank = map[string]int{"info": 0, "warn": 1, "crit": 2}

func (s *Service) on(kind string) bool { return s.cfg.Events[kind] }

// trackAlerts atualiza o que está valendo e devolve as mensagens: alertas
// novos (ou que pioraram, ou urgentes que continuam há 12 h) e os resolvidos.
// Roda com s.mu travado.
func (s *Service) trackAlerts(now time.Time, alerts []monitor.Alert, server string) []message {
	ts := now.Unix()
	for _, a := range alerts {
		cat, _ := category(a.Key, a.Level)
		if a.Key == "" || cat == "" {
			continue
		}
		t := s.st.Active[a.Key]
		if t == nil {
			t = &tracked{Key: a.Key, First: ts}
			s.st.Active[a.Key] = t
			s.dirty = true
		}
		t.Cat, t.Level, t.Title, t.Detail, t.Target, t.Last = cat, a.Level, a.Title, a.Detail, a.Target, ts
	}

	var fire, resolved []*tracked
	for k, t := range s.st.Active {
		if now.Sub(time.Unix(t.Last, 0)) >= resolveAfter {
			if t.Sent && s.on("resolved") && s.on(t.Cat) {
				resolved = append(resolved, t)
			}
			delete(s.st.Active, k)
			s.dirty = true
			continue
		}
		if t.Last != ts || !s.on(t.Cat) {
			continue // não está valendo nesta volta, ou o tipo está desligado
		}
		_, delay := category(t.Key, t.Level)
		switch {
		case !t.Sent && now.Sub(time.Unix(t.First, 0)) >= delay:
			fire = append(fire, t)
		case t.Sent && levelRank[t.Level] > levelRank[t.SentLevel]:
			fire = append(fire, t) // piorou
		case t.Sent && t.Level == "crit" && now.Sub(time.Unix(t.SentAt, 0)) >= remindAfter:
			fire = append(fire, t) // continua urgente
		}
	}
	var out []message
	if len(fire) > 0 {
		out = append(out, s.alertMessage(fire, server))
	}
	if len(resolved) > 0 {
		out = append(out, s.resolvedMessage(resolved, server, now))
	}
	return out
}

func emoji(level string) string {
	switch level {
	case "crit":
		return "🔴"
	case "warn":
		return "🟡"
	}
	return "🔵"
}

func (s *Service) link(path string) string {
	if s.cfg.PanelURL == "" {
		return ""
	}
	return "\n\n🔗 " + strings.TrimRight(s.cfg.PanelURL, "/") + "/#/" + path
}

func (s *Service) alertMessage(fire []*tracked, server string) message {
	sort.Slice(fire, func(i, j int) bool {
		if levelRank[fire[i].Level] != levelRank[fire[j].Level] {
			return levelRank[fire[i].Level] > levelRank[fire[j].Level]
		}
		return fire[i].Title < fire[j].Title
	})
	top := fire[0]
	m := message{kind: top.Cat, urgent: top.Level == "crit"}
	note := func(t *tracked) string {
		switch {
		case t.Sent && levelRank[t.Level] > levelRank[t.SentLevel]:
			return " _(piorou)_"
		case t.Sent:
			return " _(continua)_"
		}
		return ""
	}
	var b strings.Builder
	if len(fire) == 1 {
		head := map[string]string{"crit": "Urgente", "warn": "Alerta", "info": "Aviso"}[top.Level]
		fmt.Fprintf(&b, "%s *%s · %s*\n*%s*%s", emoji(top.Level), head, server, top.Title, note(top))
		if top.Detail != "" {
			b.WriteString("\n" + top.Detail)
		}
		m.title = top.Title
	} else {
		fmt.Fprintf(&b, "%s *%d alertas · %s*", emoji(top.Level), len(fire), server)
		for _, t := range fire {
			fmt.Fprintf(&b, "\n\n%s *%s*%s", emoji(t.Level), t.Title, note(t))
			if t.Detail != "" {
				b.WriteString("\n" + t.Detail)
			}
		}
		m.title = fmt.Sprintf("%d alertas: %s…", len(fire), top.Title)
	}
	b.WriteString(s.link("infos"))
	m.text = b.String()
	for _, t := range fire {
		m.keys = append(m.keys, t.Key)
	}
	// diagnóstico da IA para o que é urgente de verdade (app caiu, servidor no limite)
	if top.Level == "crit" && (top.Cat == "app_down" || top.Cat == "resources") && s.on("ai_incident") {
		t := *top
		m.after = func() { s.incident(t, server) }
	}
	return m
}

func (s *Service) resolvedMessage(list []*tracked, server string, now time.Time) message {
	var b strings.Builder
	if len(list) == 1 {
		t := list[0]
		fmt.Fprintf(&b, "✅ *Resolvido · %s*\n%s _(durou %s)_", server, t.Title, human(now.Sub(time.Unix(t.First, 0))))
	} else {
		fmt.Fprintf(&b, "✅ *%d alertas resolvidos · %s*", len(list), server)
		for _, t := range list {
			fmt.Fprintf(&b, "\n• %s _(durou %s)_", t.Title, human(now.Sub(time.Unix(t.First, 0))))
		}
	}
	return message{kind: "resolved", title: "Resolvido: " + list[0].Title, text: b.String()}
}

func human(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", max(1, int(d.Minutes())))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
	return fmt.Sprintf("%d dias", int(d.Hours()/24))
}

// --- apps novas, removidas e atualizadas ---------------------------------------------------

func (s *Service) trackApps(now time.Time, o monitor.Overview) []message {
	ts := now.Unix()
	var out []message
	for _, a := range o.Apps {
		if a.Kind != "compose" && a.Kind != "standalone" {
			continue
		}
		seen := s.st.Apps[a.Key]
		if seen == nil {
			seen = &appSeen{First: ts, Notified: !s.st.Seeded, Images: map[string]string{}}
			s.st.Apps[a.Key] = seen
			s.dirty = true
		}
		seen.Name, seen.Last = a.Name, ts
		var names, changed []string
		for _, u := range a.Units {
			c := u.Container
			if c == nil {
				continue
			}
			names = append(names, c.Name)
			svc := c.Service
			if svc == "" {
				svc = c.Name
			}
			if old, ok := seen.Images[svc]; ok && old != "" && c.ImageID != "" && old != c.ImageID && c.State == "running" {
				changed = append(changed, c.Name)
			}
			if c.ImageID != "" && seen.Images[svc] != c.ImageID {
				seen.Images[svc] = c.ImageID
				s.dirty = true
			}
		}
		if !seen.Notified && now.Sub(time.Unix(seen.First, 0)) >= 5*time.Minute {
			if !s.on("apps") {
				seen.Notified = true // desligado: não vira "nova" meses depois, se ligarem
			} else {
				sort.Strings(names)
				out = append(out, message{kind: "apps", title: "Nova aplicação: " + a.Name, apps: []string{a.Key},
					text: fmt.Sprintf("🆕 *Nova aplicação · %s*\n*%s* apareceu no servidor com %d contêiner(es): %s.%s",
						o.Server.Name, a.Name, len(names), strings.Join(names, ", "), s.link("apps"))})
			}
		}
		if len(changed) > 0 && a.Key != s.self && s.on("deploys") {
			sort.Strings(changed)
			out = append(out, message{kind: "deploys", title: a.Name + " atualizada",
				text: fmt.Sprintf("🚀 *%s atualizada · %s*\nImagem nova em: %s.", a.Name, o.Server.Name, strings.Join(changed, ", "))})
		}
	}
	for k, seen := range s.st.Apps {
		if now.Sub(time.Unix(seen.Last, 0)) < 30*time.Minute {
			continue
		}
		if seen.Notified && s.on("apps") {
			out = append(out, message{kind: "apps", title: "Aplicação removida: " + seen.Name,
				text: fmt.Sprintf("🗑️ *Aplicação removida · %s*\n*%s* não tem mais nenhum contêiner no servidor (há 30 min).", o.Server.Name, seen.Name)})
		}
		delete(s.st.Apps, k)
		s.dirty = true
	}
	if !s.st.Seeded {
		s.st.Seeded = true // o que já existia na primeira volta não é "app nova"
		s.dirty = true
	}
	return out
}

// --- segurança ---------------------------------------------------------------------------

// Security recebe os eventos do login: "login_fail", "login" e "password".
func (s *Service) Security(kind, ip string) {
	now := s.now()
	s.mu.Lock()
	var m *message
	switch kind {
	case "login_fail":
		keep := s.st.Fails[:0]
		for _, t := range s.st.Fails {
			if now.Sub(time.Unix(t, 0)) < 10*time.Minute {
				keep = append(keep, t)
			}
		}
		s.st.Fails = append(keep, now.Unix())
		if len(s.st.Fails) >= 5 && now.Sub(time.Unix(s.st.FailSent, 0)) >= time.Hour && s.on("security") {
			s.st.FailSent = now.Unix()
			m = &message{kind: "security", urgent: true, title: "Senhas erradas no login",
				text: fmt.Sprintf("🔐 *Segurança do painel*\n%d tentativas de login com senha errada em 10 min (último IP: %s). O painel bloqueia o IP por alguns minutos a cada 5 erros.", len(s.st.Fails), ip)}
		}
	case "login":
		if s.on("logins") {
			m = &message{kind: "logins", title: "Login no painel", text: fmt.Sprintf("🔐 Login no painel (IP %s).", ip)}
		}
	case "password":
		if s.on("security") {
			m = &message{kind: "security", urgent: true, title: "Senha do painel trocada",
				text: fmt.Sprintf("🔐 *Segurança do painel*\nA senha do painel foi trocada (IP %s). Se não foi você, troque de novo já.", ip)}
		}
	}
	s.dirty = true
	s.mu.Unlock()
	if m != nil {
		go s.deliver(context.Background(), *m)
	}
}

// Audit registra no WhatsApp uma ação feita pela tela (kind do catálogo, ex.:
// "pauses"), se aquele tipo estiver ligado.
func (s *Service) Audit(kind, title, text string) {
	if s.isOn(kind) {
		go s.deliver(context.Background(), message{kind: kind, title: title, text: text})
	}
}

// --- agendados (resumos e análises) ---------------------------------------------------------

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

func clock(day time.Time, v string) time.Time {
	var h, m int
	fmt.Sscanf(v, "%d:%d", &h, &m)
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
}

// periodID identifica o período de um tipo agendado (para não mandar duas vezes).
func (s *Service) periodID(kind string, now time.Time) string {
	base := map[string]string{"ai_daily": "daily", "ai_logs": "daily", "ai_weekly": "weekly"}[kind]
	if base == "" {
		base = kind
	}
	return s.src.PeriodOf(base, now).From.Format("2006-01-02")
}

// dueScheduled devolve o que já passou da hora e ainda não saiu. Com s.mu travado.
func (s *Service) dueScheduled(now time.Time) []string {
	n := now.In(s.loc)
	at := clock(n, s.cfg.DailyAt)
	if n.Before(at) || n.Sub(at) > scheduleWindow {
		return nil
	}
	var out []string
	for _, kind := range []string{"daily", "ai_daily", "weekly", "ai_weekly", "monthly", "ai_logs"} {
		switch kind {
		case "weekly", "ai_weekly":
			if n.Weekday() != time.Monday {
				continue
			}
		case "monthly":
			if n.Day() != 1 {
				continue
			}
		case "ai_daily":
			if s.on("daily") {
				continue // vai junto do resumo diário
			}
		}
		if !s.on(kind) || s.running[kind] || s.st.Done[kind] == s.periodID(kind, now) {
			continue
		}
		if k, _ := kindOf(kind); k.AI && (s.aiFn == nil || s.aiFn() == nil) {
			continue // sem IA configurada (a tela mostra o tipo bloqueado)
		}
		out = append(out, kind)
	}
	return out
}

// runScheduled monta e manda um resumo ou análise. As análises da IA rodam em
// segundo plano (levam até alguns minutos); manual = botão "enviar agora".
func (s *Service) runScheduled(ctx context.Context, kind string, now time.Time, manual bool) {
	k, _ := kindOf(kind)
	server := s.src.Overview().Server.Name
	done := kind + "=" + s.periodID(kind, now)
	if manual {
		done = ""
	}
	withAI := kind == "daily" && s.isOn("ai_daily")
	if reason := s.blocked(manual); reason != "" && (k.AI || withAI) {
		// não gasta a IA com uma mensagem que não vai sair; desligado = período encerrado
		s.mu.Lock()
		if reason == reasonOff && done != "" {
			s.markDone(done)
		}
		s.mu.Unlock()
		return
	}
	if (k.AI || withAI) && s.aiClient() != nil {
		s.mu.Lock()
		if s.running[kind] {
			s.mu.Unlock()
			return
		}
		s.running[kind] = true
		s.mu.Unlock()
		go func() {
			defer func() { s.mu.Lock(); delete(s.running, kind); s.mu.Unlock() }()
			c, cancel := context.WithTimeout(context.Background(), aiTimeout)
			defer cancel()
			m := s.scheduledMessage(c, kind, now, server, done)
			m.manual = manual
			s.deliver(c, m)
		}()
		return
	}
	if k.AI { // IA desligada: só marca o período para não tentar de novo toda hora
		s.logEntry(LogEntry{T: now.Unix(), Kind: kind, Title: k.Label, Status: "skipped", Error: "a IA não está configurada"})
		s.mu.Lock()
		if done != "" {
			s.markDone(done)
		}
		s.mu.Unlock()
		return
	}
	m := s.scheduledMessage(ctx, kind, now, server, done)
	m.manual = manual
	s.deliver(ctx, m)
}

func (s *Service) isOn(kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on(kind)
}

func (s *Service) markDone(done string) {
	kind, id, _ := strings.Cut(done, "=")
	s.st.Done[kind] = id
	s.dirty = true
}

func (s *Service) scheduledMessage(ctx context.Context, kind string, now time.Time, server, done string) message {
	k, _ := kindOf(kind)
	m := message{kind: kind, title: k.Label, done: done}
	switch kind {
	case "daily", "weekly", "monthly":
		m.text = s.src.Summary(kind, now)
		if kind == "daily" && s.isOn("ai_daily") && s.aiClient() != nil {
			p := s.src.PeriodOf("daily", now)
			txt, err := s.runAI(ctx, fmt.Sprintf(promptDaily, p.Label), 12)
			if err != nil {
				m.text += "\n\n🤖 _A análise da IA falhou: " + err.Error() + "_"
			} else if txt != "" {
				m.text += "\n\n🤖 *Análise da IA*\n" + txt
			}
		}
		m.text += s.link("")
	case "ai_daily", "ai_weekly", "ai_logs":
		prompt, title, lines := promptDaily, "Análise de ontem", 12
		switch kind {
		case "ai_daily":
			prompt = fmt.Sprintf(promptDaily, s.src.PeriodOf("daily", now).Label)
		case "ai_weekly":
			prompt, title, lines = promptWeekly, "Relatório semanal de otimização", 18
		case "ai_logs":
			prompt, title, lines = promptLogs, "Erros dos logs nas últimas 24 h", 14
		}
		txt, err := s.runAI(ctx, prompt, lines)
		if err != nil {
			txt = "_A análise falhou: " + err.Error() + "_"
		}
		m.text = fmt.Sprintf("🤖 *%s · %s*\n\n%s%s", title, server, txt, s.link("ai"))
	}
	return m
}

// --- IA ----------------------------------------------------------------------------------

const whatsappStyle = `

ESTA RESPOSTA VAI POR WHATSAPP (não para a tela do painel):
- No máximo %d linhas curtas. Sem tabelas, sem títulos com #, sem blocos de código.
- Negrito com UM asterisco (*assim*). Listas com "• ".
- Comece direto pelo que importa, sem saudação nem despedida, e não repita números que já estão no resumo.
- Se estiver tudo normal, diga isso em uma linha e pare.`

const (
	promptDaily = "Analise o dia de ontem (%s) neste servidor. Use as ferramentas (histórico do servidor e das apps em " +
		"24 h e 7 dias, eventos, logs com erro) para achar o que mudou ou chama atenção: picos, quedas, tendência de " +
		"memória ou disco, erros que se repetem, risco nos limites do plano grátis. Entregue até 3 pontos, cada um com o que fazer."
	promptWeekly = "Faça o relatório semanal de otimização deste servidor. Compare as apps nos últimos 7 dias (CPU, memória, " +
		"banda), veja o disco (o que dá para limpar com segurança, como cache de build e imagens sem uso), os riscos para os " +
		"limites do plano grátis e as tendências. Entregue até 5 sugestões concretas, da mais importante para a menos importante."
	promptLogs = "Resuma os erros dos logs das últimas 24 horas. Use a ferramenta de logs só com erros (todas as apps), agrupe " +
		"por app e por tipo de erro, destaque os que se repetem e diga a causa provável de cada um. Ignore ruído sem impacto."
	promptIncident = "Acabou de disparar este alerta no servidor: %q (%s). Contêiner ou alvo: %s. Investigue com as " +
		"ferramentas: logs só com erros desse contêiner, eventos das últimas horas e histórico de CPU e memória dele e do " +
		"servidor. Responda em três partes: *O que aconteceu*, *Causa provável* (separe fato de hipótese) e *O que fazer*."
)

// runAI roda a IA com as ferramentas do painel e devolve o texto no formato do WhatsApp.
func (s *Service) runAI(ctx context.Context, task string, lines int) (string, error) {
	cli := s.aiClient()
	if cli == nil {
		return "", errors.New("a IA não está configurada")
	}
	var b strings.Builder
	_, err := ai.Converse(ctx, cli, s.src.AIContext()+fmt.Sprintf(whatsappStyle, lines), []ai.Message{ai.Text("user", task)}, s.src.AI(),
		func(e ai.Event) {
			switch e.Type {
			case "tool":
				b.Reset() // o texto antes de consultar algo é só preâmbulo
			case "delta":
				b.WriteString(e.Text)
			}
		})
	if err != nil {
		var ae *ai.Error
		if errors.As(err, &ae) {
			return "", errors.New(ae.Message)
		}
		return "", err
	}
	return ToWhatsApp(b.String()), nil
}

// incident manda o diagnóstico da IA de um alerta urgente (até aiPerDay por dia).
func (s *Service) incident(t tracked, server string) {
	if s.aiClient() == nil {
		return
	}
	day := s.now().In(s.loc).Format("2006-01-02")
	s.mu.Lock()
	if s.st.AIDay != day {
		s.st.AIDay, s.st.AICount = day, 0
	}
	if s.st.AICount >= aiPerDay || s.running["incident"] {
		s.mu.Unlock()
		return
	}
	s.st.AICount++
	s.running["incident"] = true
	s.dirty = true
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.running, "incident"); s.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
		defer cancel()
		target := strings.TrimPrefix(strings.TrimPrefix(t.Target, "c:"), "s:")
		if target == "" {
			target = "o servidor inteiro"
		}
		txt, err := s.runAI(ctx, fmt.Sprintf(promptIncident, t.Title, t.Detail, target), 14)
		if err != nil {
			s.logEntry(LogEntry{T: s.now().Unix(), Kind: "ai_incident", Title: "Diagnóstico: " + t.Title, Status: "failed", Error: err.Error()})
			return
		}
		s.deliver(ctx, message{kind: "ai_incident", urgent: true, title: "Diagnóstico: " + t.Title,
			text: fmt.Sprintf("🤖 *Diagnóstico da IA · %s*\n_%s_\n\n%s", server, t.Title, txt)})
	}()
}

var (
	reBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reUnder   = regexp.MustCompile(`__([^_\n]+)__`)
	reHeading = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*$`)
	reBullet  = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	reTableSp = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$\n?`)
	reBlank   = regexp.MustCompile(`\n{3,}`)
)

// ToWhatsApp converte o Markdown da IA para o do WhatsApp.
func ToWhatsApp(md string) string {
	s := strings.TrimSpace(md)
	s = reTableSp.ReplaceAllString(s, "")
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|") {
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			ln = "• " + strings.Join(cells, " · ")
		}
		lines = append(lines, ln)
	}
	s = strings.Join(lines, "\n")
	s = reBold.ReplaceAllString(s, "*$1*")
	s = reUnder.ReplaceAllString(s, "_${1}_")
	s = reHeading.ReplaceAllString(s, "*$1*")
	s = reBullet.ReplaceAllString(s, "$1• ")
	s = strings.ReplaceAll(s, "***", "*")
	s = reBlank.ReplaceAllString(s, "\n\n")
	if r := []rune(s); len(r) > 3500 {
		s = string(r[:3500]) + "…"
	}
	return s
}

// --- envio -------------------------------------------------------------------------------

func inQuiet(now time.Time, from, to string) bool {
	if !hhmm.MatchString(from) || !hhmm.MatchString(to) || from == to {
		return false
	}
	cur := now.Format("15:04")
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to // atravessa a meia-noite
}

const reasonOff = "as notificações estão desligadas"

// blocked diz por que nada pode sair agora ("" = pode).
func (s *Service) blocked(manual bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockedLocked(manual)
}

func (s *Service) blockedLocked(manual bool) string {
	switch {
	case !s.cfg.Enabled && !manual:
		return reasonOff
	case len(s.cfg.Recipients) == 0:
		return "não há ninguém em \"Para quem enviar\""
	case !s.wa.Configured():
		return "o WhatsApp não está instalado neste servidor"
	case !s.status.Connected():
		return "o WhatsApp está desconectado"
	}
	return ""
}

// deliver manda (ou segura no silêncio) e registra. Alertas que não saíram
// (WhatsApp desconectado, sem destino) ficam pendentes e tentam de novo.
func (s *Service) deliver(ctx context.Context, m message) bool {
	now := s.now()
	s.mu.Lock()
	cfg := s.cfg
	if reason := s.blockedLocked(m.manual); reason != "" {
		// não registra a mesma falha toda volta: uma vez por hora por motivo
		if m.manual || now.Sub(time.Unix(s.failLog[reason], 0)) >= time.Hour {
			s.failLog[reason] = now.Unix()
			s.addLog(LogEntry{T: now.Unix(), Kind: m.kind, Title: m.title, Text: clipText(m.text), Status: "failed", Error: reason})
		}
		if m.done != "" && reason == reasonOff {
			s.markDone(m.done)
		}
		s.mu.Unlock()
		return false
	}
	if cfg.Quiet && !m.urgent && !m.manual && m.done == "" && inQuiet(now.In(s.loc), cfg.QuietFrom, cfg.QuietTo) {
		s.st.Held = append(s.st.Held, heldMsg{T: now.Unix(), Kind: m.kind, Title: m.title, Text: m.text})
		s.markSent(m, now)
		s.addLog(LogEntry{T: now.Unix(), Kind: m.kind, Title: m.title, Text: clipText(m.text), Status: "held",
			Error: "horário de silêncio: vai no resumo das " + cfg.QuietTo})
		s.mu.Unlock()
		return true
	}
	keep := s.st.Sends[:0]
	for _, t := range s.st.Sends {
		if now.Sub(time.Unix(t, 0)) < time.Hour {
			keep = append(keep, t)
		}
	}
	s.st.Sends = keep
	if len(s.st.Sends) >= maxPerHour && !m.manual {
		s.markSent(m, now) // não insiste: passou do limite, fica só no registro
		s.addLog(LogEntry{T: now.Unix(), Kind: m.kind, Title: m.title, Text: clipText(m.text), Status: "skipped",
			Error: fmt.Sprintf("passou de %d mensagens na última hora", maxPerHour)})
		s.mu.Unlock()
		return false
	}
	s.st.Sends = append(s.st.Sends, now.Unix())
	s.mu.Unlock()

	okN, errs := s.sendAll(ctx, cfg.Recipients, m.text)

	s.mu.Lock()
	e := LogEntry{T: now.Unix(), Kind: m.kind, Title: m.title, Text: clipText(m.text), Status: "sent"}
	switch {
	case okN == 0:
		e.Status, e.Error = "failed", strings.Join(errs, "; ")
	case len(errs) > 0:
		e.Status, e.Error = "partial", strings.Join(errs, "; ")
	}
	if okN > 0 {
		s.markSent(m, now)
	}
	s.addLog(e)
	s.mu.Unlock()
	if okN > 0 && m.after != nil {
		m.after()
	}
	return okN > 0
}

func (s *Service) sendAll(ctx context.Context, to []Recipient, text string) (int, []string) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	okN := 0
	var errs []string
	for _, r := range to {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.wa.Send(c, r.ID, text)
		cancel()
		if err != nil {
			name := r.Name
			if name == "" {
				name = r.ID
			}
			errs = append(errs, name+": "+err.Error())
			continue
		}
		okN++
	}
	return okN, errs
}

// markSent marca os alertas, apps e períodos da mensagem como avisados. Com s.mu travado.
func (s *Service) markSent(m message, now time.Time) {
	for _, k := range m.keys {
		if t := s.st.Active[k]; t != nil {
			t.Sent, t.SentLevel, t.SentAt = true, t.Level, now.Unix()
		}
	}
	for _, k := range m.apps {
		if a := s.st.Apps[k]; a != nil {
			a.Notified = true
		}
	}
	if m.done != "" {
		s.markDone(m.done)
	}
	s.dirty = true
}

// flushHeld manda, ao fim do silêncio, o que ficou segurado (numa mensagem só).
func (s *Service) flushHeld(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	cfg := s.cfg
	if len(s.st.Held) == 0 || (cfg.Quiet && inQuiet(now.In(s.loc), cfg.QuietFrom, cfg.QuietTo)) || !s.status.Connected() {
		s.mu.Unlock()
		return
	}
	held := s.st.Held
	s.st.Held = nil
	s.dirty = true
	s.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "🌙 *Durante o horário de silêncio (%s–%s)*", cfg.QuietFrom, cfg.QuietTo)
	for i, h := range held {
		if b.Len() > 3000 {
			fmt.Fprintf(&b, "\n\n… e mais %d", len(held)-i)
			break
		}
		fmt.Fprintf(&b, "\n\n_%s_\n%s", time.Unix(h.T, 0).In(s.loc).Format("15:04"), h.Text)
	}
	s.deliver(ctx, message{kind: "held", urgent: true, title: fmt.Sprintf("%d mensagem(ns) do silêncio", len(held)), text: b.String()})
}

func clipText(s string) string {
	if r := []rune(s); len(r) > 1500 {
		return string(r[:1500]) + "…"
	}
	return s
}

func (s *Service) addLog(e LogEntry) {
	s.st.Log = append(s.st.Log, e)
	if len(s.st.Log) > maxLog {
		s.st.Log = s.st.Log[len(s.st.Log)-maxLog:]
	}
	s.dirty = true
	if e.Status == "failed" || e.Status == "partial" {
		slog.Warn("notificação não saiu", "tipo", e.Kind, "titulo", e.Title, "erro", e.Error)
	}
}

func (s *Service) logEntry(e LogEntry) {
	s.mu.Lock()
	s.addLog(e)
	s.mu.Unlock()
}

// --- gravação ------------------------------------------------------------------------------

// save grava se algo mudou (no máximo a cada 10 min, a não ser que force).
func (s *Service) save(force bool) {
	s.mu.Lock()
	if !s.dirty || (!force && time.Since(s.saved) < 10*time.Minute) {
		s.mu.Unlock()
		return
	}
	b, _ := json.MarshalIndent(fileData{Config: s.cfg, State: s.st}, "", " ")
	s.dirty, s.saved = false, time.Now()
	s.mu.Unlock()
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		slog.Warn("não consegui gravar as notificações", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Warn("não consegui gravar as notificações", "err", err)
	}
}

// Alerts é o aviso que aparece em Infos quando as notificações estão paradas.
// Roda com o monitor travado: só lê o que já está em memória.
func (s *Service) Alerts() []monitor.Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || len(s.cfg.Recipients) == 0 || !s.wa.Configured() || s.statusAt.IsZero() {
		return nil
	}
	switch {
	case !s.status.Service:
		return []monitor.Alert{{Key: "monitor.whatsapp", Level: "warn", Area: "monitor",
			Title:  "O serviço de WhatsApp não está respondendo: as notificações estão paradas",
			Detail: "Confira o contêiner vpserver-whatsapp em Aplicações (vpserver-monitoring)."}}
	case !s.status.Connected():
		return []monitor.Alert{{Key: "monitor.whatsapp", Level: "warn", Area: "monitor",
			Title:  "WhatsApp desconectado: as notificações estão paradas",
			Detail: "Conecte de novo pelo QR code na aba Notificações."}}
	}
	return nil
}
