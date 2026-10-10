// vpmon — painel de monitoramento do servidor (vpserver-monitoring).
//
//	vpmon              sobe o coletor e o painel web
//	vpmon init         prepara a pasta de instalação (compose.yml, .env, data/) — ver internal/setup
//	vpmon reset-password <usuário>  esqueci a senha: gera uma provisória (o painel pode estar rodando)
//	vpmon healthcheck  usado pelo healthcheck do Docker (a imagem não tem curl)
//	vpmon version
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // fuso horário embutido (a imagem não tem /usr/share/zoneinfo)

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
	"github.com/edvitor13/vpserver-monitoring/internal/backup"
	"github.com/edvitor13/vpserver-monitoring/internal/cleanup"
	"github.com/edvitor13/vpserver-monitoring/internal/docker"
	"github.com/edvitor13/vpserver-monitoring/internal/fleet"
	"github.com/edvitor13/vpserver-monitoring/internal/monitor"
	"github.com/edvitor13/vpserver-monitoring/internal/notify"
	"github.com/edvitor13/vpserver-monitoring/internal/setup"
	"github.com/edvitor13/vpserver-monitoring/internal/sshchat"
	"github.com/edvitor13/vpserver-monitoring/internal/web"
)

// Trocados no build (-ldflags "-X main.version=... -X main.commit=... -X main.built=..."):
// a versão (1.4.0 no CI; <última>-dev.<commit> no deploy manual), o commit e a
// data da versão (RFC 3339).
var (
	version = "dev"
	commit  = ""
	built   = ""
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(env(k, ""), 64); err == nil {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	listen := env("VPMON_LISTEN", ":8080")

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			port := listen[strings.LastIndexByte(listen, ':')+1:]
			c := &http.Client{Timeout: 3 * time.Second}
			resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
			if err != nil || resp.StatusCode != http.StatusOK {
				os.Exit(1)
			}
			return
		case "version":
			if extra := strings.Trim(commit+", "+built, ", "); extra != "" {
				fmt.Printf("%s (%s)\n", version, extra)
			} else {
				fmt.Println(version)
			}
			return
		case "reset-password":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "uso: vpmon reset-password <usuário>")
				os.Exit(2)
			}
			pass, err := web.ResetPasswordOffline(env("VPMON_DATA", "/data"), env("VPMON_USER", web.DefaultUser), os.Args[2])
			if err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			fmt.Printf("Senha provisória de %s: %s\nNo próximo acesso o painel pede uma senha nova. As sessões abertas dessa pessoa caíram e a verificação em duas etapas foi desligada.\n", os.Args[2], pass)
			return
		case "init":
			err := setup.Run(setup.Options{Out: env("VPMON_INIT_DIR", "/out"), DockerGID: os.Getenv("DOCKER_GID"),
				Owner: os.Getenv("OWNER"), TunnelToken: os.Getenv("VPMON_TUNNEL_TOKEN"), Image: os.Getenv("VPMON_IMAGE")})
			if err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		}
	}

	user, pass := env("VPMON_USER", web.DefaultUser), os.Getenv("VPMON_PASSWORD")
	forceChange := env("VPMON_FORCE_PASSWORD_CHANGE", "false") == "true"
	if pass == "" {
		slog.Warn("sem VPMON_PASSWORD: login inicial admin/admin, com troca obrigatória no primeiro acesso — entre e troque já")
	} else if len(pass) < 10 && !forceChange {
		slog.Error("VPMON_PASSWORD precisa ter pelo menos 10 caracteres")
		os.Exit(2)
	}
	dataDir := env("VPMON_DATA", "/data")
	secret := env("VPMON_SECRET", "")
	if secret == "" {
		secret = loadOrCreateSecret(filepath.Join(dataDir, "secret"))
	}
	loc, err := time.LoadLocation(env("VPMON_TZ", "America/Sao_Paulo"))
	if err != nil {
		loc = time.UTC
	}
	interval, err := time.ParseDuration(env("VPMON_INTERVAL", "5s"))
	if err != nil || interval < time.Second {
		interval = 5 * time.Second
	}

	mon := monitor.New(monitor.Config{
		Interval:    interval,
		Proc:        env("VPMON_PROC", "/proc"),
		Sys:         env("VPMON_SYS", "/sys"),
		Cgroup:      env("VPMON_CGROUP", "/host/cgroup"),
		DataDir:     dataDir,
		DockerAddr:  env("VPMON_DOCKER", "http://vpserver-dockerproxy:2375"),
		Loc:         loc,
		AppNames:    monitor.ParseAppNames(env("VPMON_APP_NAMES", "")),
		SelfProject: env("VPMON_SELF_PROJECT", "vpserver-monitoring"),
		ServerName:  env("VPMON_SERVER_NAME", ""),
		Version:     version,
		LogSizes:    env("VPMON_LOGSIZES", "/sizes/logsizes.txt"),
		Limits: monitor.Limits{
			EgressTB:    envFloat("VPMON_EGRESS_TB", 10),
			FreeOCPU:    envFloat("VPMON_FREE_OCPU", 4),
			FreeMemGB:   envFloat("VPMON_FREE_RAM_GB", 24),
			FreeDiskGB:  envFloat("VPMON_FREE_BLOCK_GB", 200),
			GbpsPerOCPU: envFloat("VPMON_GBPS_PER_OCPU", 1),
			AlwaysFree:  env("VPMON_ALWAYS_FREE", "unknown"),
		},
	})

	// Notificações pelo WhatsApp: Evolution API no contêiner vpserver-whatsapp
	// (sem VPMON_WA_KEY, a aba mostra como instalar).
	wa := notify.NewEvolution(env("VPMON_WA_URL", "http://vpserver-whatsapp:8080"), os.Getenv("VPMON_WA_KEY"),
		env("VPMON_WA_INSTANCE", "vpserver-monitoring"))
	nt := notify.New(mon, wa, loc, dataDir, env("VPMON_SELF_PROJECT", "vpserver-monitoring"))

	// Vários servidores: este painel pode ser central (recebe resumos de outros)
	// e/ou estar conectado a um central (manda o resumo daqui).
	fl := &fleet.Fleet{Central: fleet.NewCentral(dataDir)}
	fl.Client = fleet.NewClient(dataDir, version, func() fleet.Report {
		return fleet.FromOverview(mon.Overview(), nt.PanelURL(), nt.WhatsAppMode())
	})
	// Backups dos bancos (docker exec pelo proxy, envio ao bucket configurado na tela).
	bk := backup.New(backupSource{mon}, docker.New(env("VPMON_DOCKER", "http://vpserver-dockerproxy:2375")), dataDir)
	bk.SetNotify(nt.Audit)
	mon.SetExtraAlerts(func() []monitor.Alert {
		return append(append(nt.Alerts(), fl.Central.Alerts()...), bk.Alerts()...)
	})
	nt.SetRelay(fl.Client)

	// Limpeza do disco: cache de build e imagens sem nome pelo proxy; logs pelo vpserver-cleaner.
	cl := cleanup.New(docker.New(env("VPMON_DOCKER", "http://vpserver-dockerproxy:2375")), mon, dataDir, env("VPMON_CLEAN_DIR", "/clean"))
	cl.SetNotify(func() string { return mon.Overview().Server.Name }, nt.Audit)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() { mon.Run(ctx); close(done) }()
	go nt.Run(ctx)
	go fl.Client.Run(ctx)
	go cl.Run(ctx)
	go bk.Run(ctx)
	// SSH pela tela (desligado até um administrador com 2FA ativar)
	sc, err := sshchat.New(dataDir)
	if err != nil {
		slog.Error("SSH pela tela indisponível", "err", err)
	} else {
		go sc.Run(ctx)
	}
	defer fl.Central.Save()

	// IA (DeepSeek), pelas variáveis DEEPSEEK_*. A chave também pode
	// ser posta pela tela (Configurações → IA), o que vale mais que o .env.
	aiCfg := ai.Config{
		APIKey:   os.Getenv("DEEPSEEK_API_KEY"),
		Host:     env("DEEPSEEK_API_HOST", "api.deepseek.com"),
		Endpoint: env("DEEPSEEK_API_ENDPOINT", "/v1/chat/completions"),
		Model:    env("DEEPSEEK_API_MODEL", "deepseek-chat"),
	}
	if aiCfg.Enabled() {
		slog.Info("IA pelo .env", "modelo", aiCfg.Model, "url", aiCfg.URL())
	}

	auth := web.NewAuth(user, pass, secret, env("VPMON_COOKIE_SECURE", "true") == "true", dataDir, forceChange)
	ws := web.New(mon, auth, env("VPMON_TRUST_CF", "true") == "true", aiCfg, filepath.Join(dataDir, "settings.json"), nt, fl).WithCleanup(cl).WithBackup(bk).WithBuild(commit, built)
	if sc != nil {
		ws.WithSSH(sc)
	}
	fl.Client.SetLocal(ws.LocalView) // o central pode ler este painel (se compartilhado)
	go fl.Client.RunViews(ctx)
	srv := &http.Server{
		Addr:              listen,
		Handler:           ws.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("vpmon no ar", "version", version, "listen", listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("servidor web caiu", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shut)
	<-done // espera salvar o estado
	slog.Info("vpmon parado")
}

// loadOrCreateSecret guarda um segredo aleatório em /data/secret na primeira vez.
func loadOrCreateSecret(p string) string {
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b))
	}
	buf := make([]byte, 32)
	rand.Read(buf)
	s := hex.EncodeToString(buf)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		slog.Warn("não consegui gravar o segredo; as sessões caem a cada reinício", "err", err)
	}
	return s
}

// backupSource dá aos backups os contêineres e o nome do servidor.
type backupSource struct{ *monitor.Monitor }

func (b backupSource) ServerName() string { return b.Overview().Server.Name }
