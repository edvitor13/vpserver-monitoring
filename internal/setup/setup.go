// Package setup é o `vpmon init`: prepara a pasta de instalação no servidor
// (compose.yml, .env com senha inicial e a pasta de dados) para quem instala
// pela imagem, sem clonar o repositório.
//
//	sudo mkdir -p /opt/vpserver-monitoring && cd /opt/vpserver-monitoring
//	docker run --rm --user 0 -v "$PWD":/out \
//	  -e DOCKER_GID=$(getent group docker | cut -d: -f3) -e OWNER=$(id -u):$(id -g) \
//	  ghcr.io/edvitor13/vpserver-monitoring init
//	docker compose up -d
package setup

import (
	"bufio"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed compose.yml
var composeYML []byte

type Options struct {
	Out         string // pasta montada do host (/out)
	DockerGID   string
	Owner       string // "uid:gid" do usuário do host (para o .env e o compose.yml)
	TunnelToken string
	Image       string
	Stdout      io.Writer
	MemTotal    uint64 // RAM do host em bytes (0 = lê /proc/meminfo, que no contêiner mostra a do host)
}

// minRAMWhatsApp: abaixo disso (ex.: VM Micro de 1 GB), o WhatsApp das
// notificações (~250 MB) vem desligado.
const minRAMWhatsApp = 2 << 30

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func memTotal() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb << 10
		}
	}
	return 0
}

// whatsappEnv é o trecho do .env das notificações pelo WhatsApp.
func whatsappEnv(ram uint64, withProfile bool) string {
	profile := "COMPOSE_PROFILES=whatsapp\n"
	if ram > 0 && ram < minRAMWhatsApp {
		profile = "# desligado: este servidor tem menos de 2 GB de RAM. Para ligar: COMPOSE_PROFILES=whatsapp\nCOMPOSE_PROFILES=\n"
	}
	if !withProfile {
		profile = ""
	}
	return `
# WhatsApp das notificações (contêineres vpserver-whatsapp e vpserver-whatsapp-db, ~250 MB de RAM).
# A conexão (QR code), os destinos e o que avisar ficam na aba Notificações do painel.
# Para desligar: tire "whatsapp" de COMPOSE_PROFILES e rode  docker compose up -d --remove-orphans
` + profile + "VPMON_WA_KEY=" + randomHex(24) + "\nVPMON_WA_DB_PASSWORD=" + randomHex(18) + "\n"
}

func randomPassword() string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

func chown(path, owner string) {
	uid, gid, ok := strings.Cut(owner, ":")
	if !ok {
		return
	}
	u, err1 := strconv.Atoi(uid)
	g, err2 := strconv.Atoi(gid)
	if err1 == nil && err2 == nil {
		os.Chown(path, u, g)
	}
}

// Run cria/atualiza os arquivos. O compose.yml é sempre reescrito (é do
// painel); o .env só é criado na primeira vez (é seu).
func Run(o Options) error {
	w := o.Stdout
	if w == nil {
		w = os.Stdout
	}
	if st, err := os.Stat(o.Out); err != nil || !st.IsDir() {
		return fmt.Errorf("monte a pasta de instalação em %s (-v \"$PWD\":%s)", o.Out, o.Out)
	}
	compose := filepath.Join(o.Out, "compose.yml")
	if old, err := os.ReadFile(compose); err == nil && string(old) != string(composeYML) {
		os.WriteFile(compose+".bak", old, 0o644)
	}
	if err := os.WriteFile(compose, composeYML, 0o644); err != nil {
		return err
	}
	chown(compose, o.Owner)

	data := filepath.Join(o.Out, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	os.Chown(data, 65532, 65532) // usuário do painel na imagem (distroless nonroot)

	ram := o.MemTotal
	if ram == 0 {
		ram = memTotal()
	}
	envPath := filepath.Join(o.Out, ".env")
	if old, err := os.ReadFile(envPath); err == nil {
		// atualização: o .env é seu; só acrescenta o que uma versão nova precisa
		if !strings.Contains(string(old), "\nVPMON_WA_KEY=") && !strings.HasPrefix(string(old), "VPMON_WA_KEY=") {
			add := whatsappEnv(ram, !strings.Contains(string(old), "COMPOSE_PROFILES="))
			f, err := os.OpenFile(envPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = f.WriteString(add)
			f.Close()
			if err != nil {
				return err
			}
			fmt.Fprintln(w, "compose.yml atualizado. No .env, acrescentei as chaves do WhatsApp das notificações (o resto ficou como estava).")
		} else {
			fmt.Fprintln(w, "compose.yml atualizado. O .env já existia e não foi mexido.")
		}
		fmt.Fprintln(w, "Para subir a versão nova:  docker compose pull && docker compose up -d")
		return nil
	}
	if o.DockerGID == "" {
		return fmt.Errorf("faltou DOCKER_GID: rode com -e DOCKER_GID=$(getent group docker | cut -d: -f3)")
	}
	pass := randomPassword()
	tunnelCmd := ""
	if o.TunnelToken == "" {
		tunnelCmd = "tunnel --no-autoupdate --url http://vpserver-monitor:8080"
	}
	env := fmt.Sprintf(`# vpserver-monitoring — gerado por "vpmon init". Fica só neste servidor (chmod 600).
# Depois de mudar: docker compose up -d

# Login inicial: usuário admin e a senha abaixo. No primeiro acesso o painel obriga
# a trocar (e dá para trocar o usuário também). Esqueceu a senha? Veja o README.
VPMON_USER=admin
VPMON_PASSWORD=%s
VPMON_FORCE_PASSWORD_CHANGE=true

# Grupo docker do host (o proxy só leitura precisa dele para abrir o socket).
DOCKER_GID=%s

# Acesso pela internet (escolha um):
# - com domínio: token do seu túnel na Cloudflare (Networking -> Tunnels) e deixe VPMON_TUNNEL_COMMAND vazio;
#   na Cloudflare, crie a rota  painel.seudominio.com -> http://vpserver-monitor:8080
# - sem domínio: VPMON_TUNNEL_COMMAND abaixo cria um endereço https://...trycloudflare.com
#   (muda a cada reinício; veja com: docker compose logs tunnel | grep trycloudflare)
VPMON_TUNNEL_TOKEN=%s
VPMON_TUNNEL_COMMAND=%s

# IA (opcional): dá para pôr a chave pela tela, em Configurações -> IA.
DEEPSEEK_API_KEY=

# Opcionais
VPMON_SERVER_NAME=
VPMON_APP_NAMES=
VPMON_ALWAYS_FREE=unknown
VPMON_TZ=America/Sao_Paulo
`, pass, o.DockerGID, o.TunnelToken, tunnelCmd) + whatsappEnv(ram, true)
	if o.Image != "" {
		env += "VPMON_IMAGE=" + o.Image + "\n"
	}
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		return err
	}
	chown(envPath, o.Owner)

	fmt.Fprintf(w, `
Pronto: compose.yml, .env e data/ criados em %s.

1) Suba o painel (nesta pasta):
     docker compose up -d

2) Endereço:
`, "/opt/vpserver-monitoring")
	if o.TunnelToken == "" {
		fmt.Fprintln(w, "     sem domínio (Quick Tunnel). Em ~20 s:  docker compose logs tunnel | grep -o 'https://.*trycloudflare.com'")
	} else {
		fmt.Fprintln(w, "     na Cloudflare, no seu túnel: rota  painel.seudominio.com  ->  http://vpserver-monitor:8080")
	}
	fmt.Fprintf(w, `
3) Entre com:   usuário admin   senha %s
   No primeiro acesso o painel pede uma senha nova e, se quiser, a chave da IA
   e a conexão com o WhatsApp das notificações (QR code).

Guarde a senha agora: ela não aparece de novo (fica no .env, que só você lê).
`, pass)
	return nil
}
