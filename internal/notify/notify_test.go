package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
)

// --- dublês ------------------------------------------------------------------------------

type fakeSrc struct {
	mu sync.Mutex
	o  monitor.Overview
}

func (f *fakeSrc) Overview() monitor.Overview { f.mu.Lock(); defer f.mu.Unlock(); return f.o }
func (f *fakeSrc) set(alerts []monitor.Alert, apps ...monitor.AppView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.o.Server.Name = "srv"
	f.o.Alerts = alerts
	f.o.Apps = apps
}
func (f *fakeSrc) Summary(kind string, now time.Time, lang string) string {
	return "📊 resumo " + kind
}
func (f *fakeSrc) PeriodOf(kind string, now time.Time) monitor.Period {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return monitor.Period{Kind: kind, From: d.AddDate(0, 0, -1), To: d, Label: "ontem"}
}
func (f *fakeSrc) AIContext() string { return "" }
func (f *fakeSrc) AI() ai.Executor   { return nil }

type fakeWA struct {
	mu    sync.Mutex
	state string
	sent  []string
}

func (w *fakeWA) Configured() bool { return true }
func (w *fakeWA) Status(context.Context) Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Status{Service: true, Exists: true, State: w.state}
}
func (w *fakeWA) Connect(context.Context, string) (QR, error) { return QR{}, nil }
func (w *fakeWA) Logout(context.Context) error                { return nil }
func (w *fakeWA) Groups(context.Context) ([]Group, error)     { return nil, nil }
func (w *fakeWA) Send(_ context.Context, to, text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, text)
	return nil
}
func (w *fakeWA) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.sent
	w.sent = nil
	return out
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func setup(t *testing.T, start string) (*Service, *fakeSrc, *fakeWA, *fakeClock) {
	t.Helper()
	loc := time.FixedZone("BRT", -3*3600)
	st, _ := time.ParseInLocation("2006-01-02 15:04", start, loc)
	c := &fakeClock{st}
	src, wa := &fakeSrc{}, &fakeWA{state: "open"}
	src.set(nil)
	s := New(src, wa, loc, t.TempDir(), "self")
	s.now = c.now
	s.cfg.Recipients = []Recipient{{ID: "5511900000000", Name: "eu"}}
	s.cfg.Events["daily"], s.cfg.Events["monthly"] = false, false // cada teste liga o que usa
	return s, src, wa, c
}

var down = monitor.Alert{Key: "app.unhealthy:api", Level: "crit", Area: "app", Title: "api está unhealthy", Detail: "o healthcheck falha"}

// --- alertas -------------------------------------------------------------------------------

func TestAlertWaitsThenSendsOnceThenResolves(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 14:00")
	ctx := context.Background()
	src.set([]monitor.Alert{down})
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("avisou antes de durar 2 min: %v", got)
	}
	c.add(2 * time.Minute)
	s.Tick(ctx)
	got := wa.take()
	if len(got) != 1 || !strings.Contains(got[0], "api está unhealthy") || !strings.Contains(got[0], "🔴") {
		t.Fatalf("esperava o alerta: %v", got)
	}
	c.add(time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("repetiu o alerta: %v", got)
	}
	src.set(nil) // resolveu
	c.add(time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("resolveu cedo demais: %v", got)
	}
	c.add(3 * time.Minute)
	s.Tick(ctx)
	got = wa.take()
	if len(got) != 1 || !strings.Contains(got[0], "Resolvido") {
		t.Fatalf("esperava o resolvido: %v", got)
	}
}

func TestEscalationAndDisabledKind(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 14:00")
	ctx := context.Background()
	disk := monitor.Alert{Key: "host.disk", Level: "warn", Title: "Disco 82% cheio"}
	src.set([]monitor.Alert{disk})
	s.Tick(ctx)
	c.add(2 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 {
		t.Fatalf("esperava o aviso do disco: %v", got)
	}
	disk.Level, disk.Title = "crit", "Disco 93% cheio"
	src.set([]monitor.Alert{disk})
	c.add(time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 || !strings.Contains(got[0], "piorou") {
		t.Fatalf("esperava 'piorou': %v", got)
	}

	s.cfg.Events["app_down"] = false
	src.set([]monitor.Alert{disk, down})
	s.Tick(ctx)
	c.add(5 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("tipo desligado não deveria avisar: %v", got)
	}
}

func TestCPUWarnIsNotNotified(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 14:00")
	src.set([]monitor.Alert{{Key: "host.cpu", Level: "warn", Title: "CPU alta: 80%"}})
	s.Tick(context.Background())
	c.add(20 * time.Minute)
	s.Tick(context.Background())
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("CPU 75-90%% não vira notificação: %v", got)
	}
}

func TestDisconnectedKeepsPendingUntilConnected(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 14:00")
	ctx := context.Background()
	wa.state = "close"
	src.set([]monitor.Alert{down})
	s.Tick(ctx)
	c.add(3 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("desconectado não envia: %v", got)
	}
	if a := s.Alerts(); len(a) != 1 || a[0].Key != "monitor.whatsapp" {
		t.Fatalf("Infos deveria avisar que o WhatsApp caiu: %+v", a)
	}
	wa.state = "open"
	c.add(time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 {
		t.Fatalf("ao reconectar, o pendente sai: %v", got)
	}
	if a := s.Alerts(); len(a) != 0 {
		t.Fatalf("conectado, sem alerta: %+v", a)
	}
}

func TestQuietHoldsWarningsButNotUrgent(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 23:00")
	ctx := context.Background()
	disk := monitor.Alert{Key: "host.disk", Level: "warn", Title: "Disco 82% cheio"}
	src.set([]monitor.Alert{disk, down})
	s.Tick(ctx)
	c.add(2 * time.Minute)
	s.Tick(ctx)
	got := wa.take()
	if len(got) != 1 || !strings.Contains(got[0], "2 alertas") {
		t.Fatalf("o urgente sai no silêncio (junto com o resto da volta): %v", got)
	}

	src.set([]monitor.Alert{down, {Key: "host.swap", Level: "warn", Title: "Swap acima de 50%"}})
	s.Tick(ctx)
	c.add(3 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("aviso não urgente fica segurado no silêncio: %v", got)
	}
	if len(s.st.Held) != 2 { // o swap novo e o "resolvido" do disco
		t.Fatalf("esperava 2 mensagens seguradas, veio %d", len(s.st.Held))
	}
	c.t = time.Date(2026, 10, 7, 7, 1, 0, 0, c.t.Location())
	s.Tick(ctx)
	got = wa.take()
	if len(got) < 1 || !strings.Contains(strings.Join(got, "\n"), "horário de silêncio") || !strings.Contains(strings.Join(got, "\n"), "Swap") {
		t.Fatalf("ao fim do silêncio, sai o resumo do que ficou: %v", got)
	}
}

// --- agendados, apps, segurança -----------------------------------------------------------------

func TestDailySummaryOncePerDay(t *testing.T) {
	s, _, wa, c := setup(t, "2026-10-06 07:59")
	s.cfg.Events["daily"] = true
	ctx := context.Background()
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("antes do horário: %v", got)
	}
	c.add(2 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 || !strings.Contains(got[0], "resumo daily") {
		t.Fatalf("esperava o resumo diário: %v", got)
	}
	c.add(10 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("resumo repetido: %v", got)
	}
	c.add(24 * time.Hour)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 {
		t.Fatalf("no outro dia sai de novo: %v", got)
	}
}

func TestNewAppAfterSeed(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 14:00")
	ctx := context.Background()
	old := monitor.AppView{Key: "old", Name: "Old", Kind: "compose"}
	src.set(nil, old)
	s.Tick(ctx) // o que já existia não é "novo"
	app := monitor.AppView{Key: "loja", Name: "Loja", Kind: "compose", Units: []monitor.UnitView{
		{Container: &docker.Container{Name: "loja-api", State: "running", Service: "api", ImageID: "sha:1"}}}}
	src.set(nil, old, app)
	c.add(time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 0 {
		t.Fatalf("espera 5 min para avisar app nova: %v", got)
	}
	c.add(5 * time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 || !strings.Contains(got[0], "Nova aplicação") || !strings.Contains(got[0], "loja-api") {
		t.Fatalf("esperava a app nova: %v", got)
	}
	s.cfg.Events["deploys"] = true
	app.Units[0].Container = &docker.Container{Name: "loja-api", State: "running", Service: "api", ImageID: "sha:2"}
	src.set(nil, old, app)
	c.add(time.Minute)
	s.Tick(ctx)
	if got := wa.take(); len(got) != 1 || !strings.Contains(got[0], "atualizada") {
		t.Fatalf("esperava o deploy: %v", got)
	}
}

func TestSecurityAfterFiveFailures(t *testing.T) {
	s, _, wa, _ := setup(t, "2026-10-06 14:00")
	s.status = Status{Service: true, Exists: true, State: "open"}
	for i := 0; i < 7; i++ {
		s.Security("login_fail", "1.2.3.4", "")
	}
	time.Sleep(100 * time.Millisecond)
	got := wa.take()
	if len(got) != 1 || !strings.Contains(got[0], "senha errada") {
		t.Fatalf("esperava 1 aviso de segurança: %v", got)
	}
}

// --- formatação e validação ------------------------------------------------------------------------

func TestToWhatsApp(t *testing.T) {
	in := "## Causa\n**Memória** subiu\n- item um\n* item dois\n\n\n\n| app | cpu |\n|---|---|\n| api | 3% |"
	out := ToWhatsApp(in)
	for _, want := range []string{"*Causa*", "*Memória* subiu", "• item um", "• item dois", "• app · cpu", "• api · 3%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("faltou %q em:\n%s", want, out)
		}
	}
	if strings.Contains(out, "---") || strings.Contains(out, "\n\n\n") {
		t.Fatalf("sobrou sujeira:\n%s", out)
	}
}

func TestNormalizeNumber(t *testing.T) {
	for in, want := range map[string]string{"+55 (11) 91234-5678": "5511912345678", "120363012345678901@g.us": "120363012345678901@g.us"} {
		if got, err := NormalizeNumber(in); err != nil || got != want {
			t.Fatalf("%q → %q, %v", in, got, err)
		}
	}
	if _, err := NormalizeNumber("123"); err == nil {
		t.Fatal("curto demais deveria falhar")
	}
}

func TestSaveConfigValidates(t *testing.T) {
	s, _, _, _ := setup(t, "2026-10-06 14:00")
	c := defaultConfig()
	c.Recipients = []Recipient{{ID: "+55 11 91234-5678"}, {ID: "5511912345678"}}
	c.Events = map[string]bool{"daily": false, "inventado": true}
	got, err := s.SaveConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Recipients) != 1 || got.Events["daily"] || got.Events["inventado"] || !got.Events["app_down"] {
		t.Fatalf("config errada: %+v", got)
	}
	c.DailyAt = "25:00"
	if _, err := s.SaveConfig(c); err == nil {
		t.Fatal("horário inválido deveria falhar")
	}
}

func TestInQuiet(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	if !inQuiet(at(23, 0), "22:00", "07:00") || !inQuiet(at(3, 0), "22:00", "07:00") || inQuiet(at(7, 0), "22:00", "07:00") {
		t.Fatal("silêncio que atravessa a meia-noite")
	}
	if !inQuiet(at(13, 0), "12:00", "14:00") || inQuiet(at(15, 0), "12:00", "14:00") {
		t.Fatal("silêncio no mesmo dia")
	}
}

// --- Evolution ----------------------------------------------------------------------------------------

func TestEvolutionClient(t *testing.T) {
	var mu sync.Mutex
	state, created, loggedOut := "", false, false
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("apikey") != "k" {
			w.WriteHeader(401)
			w.Write([]byte(`{"status":401,"error":"Unauthorized","response":{"message":"Unauthorized"}}`))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/instance/connectionState/"):
			if state == "" {
				w.WriteHeader(404)
				w.Write([]byte(`{"status":404,"response":{"message":["The \"x\" instance does not exist"]}}`))
				return
			}
			w.Write([]byte(`{"instance":{"instanceName":"x","state":"` + state + `"}}`))
		case r.URL.Path == "/instance/create":
			created, state = true, "connecting"
			w.Write([]byte(`{"instance":{"status":"connecting"},"qrcode":{"pairingCode":null,"code":"abc","base64":"data:image/png;base64,AAA","count":1}}`))
		case strings.HasPrefix(r.URL.Path, "/instance/logout/"):
			loggedOut, state = true, "close"
			w.Write([]byte(`{"status":"SUCCESS"}`))
		case strings.HasPrefix(r.URL.Path, "/instance/connect/"):
			code := "null"
			if r.URL.Query().Get("number") != "" && state == "close" {
				code = `"ABCD1234"`
			}
			w.Write([]byte(`{"pairingCode":` + code + `,"code":"abc","base64":"data:image/png;base64,BBB","count":2}`))
		case strings.HasPrefix(r.URL.Path, "/instance/fetchInstances"):
			w.Write([]byte(`[{"name":"x","ownerJid":"5511999990000@s.whatsapp.net","profileName":"Ana"}]`))
		case strings.HasPrefix(r.URL.Path, "/message/sendText/"):
			json.NewDecoder(r.Body).Decode(&sent)
			w.Write([]byte(`{"key":{}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	if st := NewEvolution(srv.URL, "errada", "x").Status(ctx); !st.Service || !strings.Contains(st.Error, "VPMON_WA_KEY") {
		t.Fatalf("chave errada: %+v", st)
	}
	e := NewEvolution(srv.URL, "k", "x")
	if st := e.Status(ctx); !st.Service || st.Exists {
		t.Fatalf("instância inexistente: %+v", st)
	}
	qr, err := e.Connect(ctx, "")
	if err != nil || !created || qr.Image != "data:image/png;base64,AAA" {
		t.Fatalf("create: %+v %v", qr, err)
	}
	qr, err = e.Connect(ctx, "5511999990000")
	if err != nil || !loggedOut || qr.PairingCode != "ABCD1234" {
		t.Fatalf("código de pareamento precisa de logout antes: %+v %v (logout=%v)", qr, err, loggedOut)
	}
	mu.Lock()
	state = "open"
	mu.Unlock()
	st := e.Status(ctx)
	if !st.Connected() || st.Number != "5511999990000" || st.Name != "Ana" {
		t.Fatalf("conectado: %+v", st)
	}
	if err := e.Send(ctx, "5511888880000", "oi"); err != nil || sent["number"] != "5511888880000" || sent["text"] != "oi" {
		t.Fatalf("envio: %v %v", sent, err)
	}
	if NewEvolution(srv.URL, "", "x").Configured() {
		t.Fatal("sem chave não está configurado")
	}
}

func TestPanelLinkFollowsPublicAddress(t *testing.T) {
	s, _, _, _ := setup(t, "2026-10-06 14:00")
	for origin, public := range map[string]bool{
		"https://painel.loja.com": true, "https://abc-def.trycloudflare.com": true,
		"http://painel.loja.com": false, "http://localhost:8080": false, "https://127.0.0.1:8080": false, "https://localhost": false,
	} {
		if PublicOrigin(origin) != public {
			t.Fatalf("%s: público=%v", origin, !public)
		}
	}
	s.SeenOrigin("https://abc-def.trycloudflare.com")
	s.SeenOrigin("http://localhost:8080") // túnel SSH: não troca
	if s.cfg.PanelURL != "https://abc-def.trycloudflare.com" {
		t.Fatalf("link: %s", s.cfg.PanelURL)
	}
	s.SeenOrigin("https://xyz.trycloudflare.com") // o Quick Tunnel reiniciou com outro endereço
	if s.cfg.PanelURL != "https://xyz.trycloudflare.com" {
		t.Fatalf("o link tem de acompanhar o endereço novo: %s", s.cfg.PanelURL)
	}
	c := s.cfg
	c.PanelURL = "http://localhost:8080" // salvou pela tela entrando pelo túnel SSH
	got, err := s.SaveConfig(c)
	if err != nil || got.PanelURL != "https://xyz.trycloudflare.com" {
		t.Fatalf("endereço local não substitui o público: %v %v", got.PanelURL, err)
	}
}

// --- idioma ------------------------------------------------------------------------------------

// ptLeft acha português que sobrou numa mensagem que devia estar em inglês.
var ptLeft = regexp.MustCompile(`(?i)[à-ÿ]|\b(de|do|da|em|está|com|para|pelo|durou|piorou|continua|urgente|alerta|alertas|aviso|resolvido|nova|aplicação|senha|painel|servidor|tentativas|errada|contêiner|teste)\b`)

// Com o idioma das mensagens em inglês, o que sai pelo WhatsApp (e fica no
// registro) é inglês: alertas, piora, resolvido, app nova, segurança e teste.
func TestMessagesFollowLanguage(t *testing.T) {
	s, src, wa, c := setup(t, "2026-10-06 14:00")
	ctx := context.Background()
	s.cfg.Lang, s.cfg.PanelURL = "en", "https://painel.exemplo.com"
	s.cfg.Events["deploys"] = true
	unhealthy := monitor.Alert{Key: "app.unhealthy:loja-api-1", Level: "crit", Area: "app", Title: "loja-api-1 está unhealthy",
		Detail: "O healthcheck do contêiner está falhando. Veja os logs."}
	disk := monitor.Alert{Key: "host.disk", Level: "warn", Title: "Disco 82% cheio"}
	old := monitor.AppView{Key: "old", Name: "Old", Kind: "compose"}
	var sent []string

	src.set([]monitor.Alert{unhealthy}, old)
	s.Tick(ctx)
	c.add(2 * time.Minute)
	s.Tick(ctx)
	sent = append(sent, wa.take()...) // um alerta
	src.set([]monitor.Alert{unhealthy, disk}, old)
	c.add(time.Minute)
	s.Tick(ctx)
	c.add(2 * time.Minute)
	s.Tick(ctx)
	disk.Level, disk.Title = "crit", "Disco 93% cheio"
	src.set([]monitor.Alert{unhealthy, disk}, old)
	c.add(time.Minute)
	s.Tick(ctx)
	sent = append(sent, wa.take()...) // disco e a piora
	app := monitor.AppView{Key: "loja", Name: "Loja", Kind: "compose", Units: []monitor.UnitView{
		{Container: &docker.Container{Name: "loja-api", State: "running", Service: "api", ImageID: "sha:1"}}}}
	src.set(nil, old, app) // resolveu tudo e chegou uma app
	c.add(6 * time.Minute)
	s.Tick(ctx)
	c.add(6 * time.Minute) // a app nova sai depois de 5 min
	s.Tick(ctx)
	app.Units[0].Container = &docker.Container{Name: "loja-api", State: "running", Service: "api", ImageID: "sha:2"}
	src.set(nil, old, app)
	c.add(time.Minute)
	s.Tick(ctx)
	sent = append(sent, wa.take()...)
	s.Security("password", "203.0.113.7", "ana")
	for i := 0; i < 7; i++ {
		s.Security("login_fail", "203.0.113.8", "")
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := s.SendNow(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	sent = append(sent, wa.take()...)

	all := strings.Join(sent, "\n---\n")
	if len(sent) < 7 {
		t.Fatalf("esperava pelo menos 7 mensagens, saíram %d:\n%s", len(sent), all)
	}
	for _, m := range sent {
		for _, ln := range strings.Split(m, "\n") {
			if strings.Contains(ln, "https://") {
				continue
			}
			if w := ptLeft.FindString(ln); w != "" {
				t.Errorf("sobrou português (%q) na linha %q de:\n%s", w, ln, m)
			}
		}
	}
	for _, want := range []string{"🔴 *Urgent · srv*\n*loja-api-1 is unhealthy*", "The container's healthcheck is failing.",
		"*Disk 93% full* _(got worse)_", "*2 alerts resolved · srv*", "• Disk 93% full _(lasted 9 min)_",
		"*Loja* showed up on the server", "🚀 *Loja updated · srv*", "*Panel security*", "*VPServer test · srv*"} {
		if !strings.Contains(all, want) {
			t.Errorf("faltou %q em:\n%s", want, all)
		}
	}
	if log := s.View(ctx).Log; len(log) == 0 || strings.Contains(log[len(log)-1].Text, "Teste do VPServer") {
		t.Fatalf("o registro guarda o que saiu (em inglês): %+v", log)
	}
}

func TestSaveConfigLanguage(t *testing.T) {
	s, _, _, _ := setup(t, "2026-10-06 14:00")
	c := defaultConfig()
	c.Lang = "en"
	if got, err := s.SaveConfig(c); err != nil || got.Lang != "en" {
		t.Fatalf("idioma: %+v %v", got.Lang, err)
	}
	c.Lang = ""
	if got, _ := s.SaveConfig(c); got.Lang != "en" {
		t.Fatalf("sem o campo (tela antiga), mantém o idioma: %q", got.Lang)
	}
	c.Lang = "fr"
	if _, err := s.SaveConfig(c); err == nil {
		t.Fatal("idioma desconhecido deveria falhar")
	}
}
