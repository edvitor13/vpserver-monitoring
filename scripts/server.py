#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
vpserver-monitoring num servidor, pela sua máquina (deploy por pacote via SSH).
Quem só quer instalar pode usar a imagem (README → "Instalar no seu servidor").

CONEXÃO: crie scripts/server.conf (fica fora do Git) com:
  VPMON_HOST=<ip do servidor>
  VPMON_KEY=~/.ssh/<sua-chave>
  VPMON_USER=ubuntu        (opcional)
  VPMON_PORT=22            (opcional)
As mesmas variáveis no ambiente valem mais que o arquivo.

USO:
  python scripts/server.py                 # estado do painel (contêineres, saúde, versão)
  python scripts/server.py logs [-f] [monitor|dockerproxy|tunnel]
  python scripts/server.py deploy          # compila aqui (Go, linux/arm64), envia e sobe
  python scripts/server.py restart         # recria os contêineres (relê o .env)
  python scripts/server.py rollback        # volta para a versão anterior
  python scripts/server.py password        # esqueci a senha: gera uma nova (ou VPMON_NEW_PASSWORD=...)
  python scripts/server.py url             # endereço do Quick Tunnel (quando não há domínio)
  python scripts/server.py setup           # 1ª vez: /opt/vpserver-monitoring, .env, receive
  python scripts/server.py ci-key <arquivo.pub>   # autoriza a chave de deploy do GitHub Actions
  python scripts/server.py ssh

O QUE ESTE SCRIPT NUNCA FAZ (o servidor pode ter outras apps):
  - mexer em contêiner, rede, volume ou arquivo de outro projeto;
  - `docker system/volume/network/builder prune`, `compose down -v`;
  - publicar porta no host;
  - imprimir o .env (a senha só aparece quando é gerada, uma vez).
"""
import argparse
import base64
import io
import os
import secrets
import subprocess
import sys
import tarfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]


def _conf():
    """Lê scripts/server.conf (KEY=VALOR); o ambiente vale mais."""
    out = {}
    p = REPO / "scripts" / "server.conf"
    if p.exists():
        for ln in p.read_text(encoding="utf-8").splitlines():
            k, sep, v = ln.strip().partition("=")
            if sep and not k.startswith("#"):
                out[k.strip()] = v.strip()
    out.update({k: v for k, v in os.environ.items() if k.startswith("VPMON_")})
    return out


CONF = _conf()
SSH_HOST = CONF.get("VPMON_HOST", "")
SSH_USER = CONF.get("VPMON_USER", "ubuntu")
SSH_KEY = os.path.expanduser(CONF.get("VPMON_KEY", ""))
SSH_PORT = CONF.get("VPMON_PORT", "22")

BASE = "/opt/vpserver-monitoring"
PROJECT = "vpserver-monitoring"
COMPOSE = f"docker compose -p {PROJECT} -f {BASE}/current/deploy/compose.yml --env-file {BASE}/.env"


def ssh_base(interactive=False):
    if not SSH_HOST:
        raise SystemExit("Falta o servidor: crie scripts/server.conf com VPMON_HOST=<ip> e VPMON_KEY=<chave> (ver o topo deste arquivo).")
    cmd = ["ssh", "-p", SSH_PORT, "-o", "StrictHostKeyChecking=accept-new"]
    if not interactive:
        cmd += ["-o", "ConnectTimeout=15", "-o", "BatchMode=yes"]
    if SSH_KEY and os.path.exists(SSH_KEY):
        cmd += ["-i", SSH_KEY]
    return cmd + [f"{SSH_USER}@{SSH_HOST}"]


def remote(script, *, capture=False, timeout=None):
    """Roda um script bash no servidor. O script vai pela stdin (nunca no argv),
    então segredos dentro dele não aparecem no `ps` do servidor."""
    remote_cmd = 'f=$(mktemp); cat > "$f"; bash "$f" </dev/null; r=$?; rm -f "$f"; exit $r'
    return subprocess.run(ssh_base() + [remote_cmd], input=script.encode(), capture_output=capture, timeout=timeout)


def git_version():
    try:
        v = subprocess.run(["git", "rev-parse", "--short=7", "HEAD"], cwd=REPO, capture_output=True, text=True, check=True).stdout.strip()
        dirty = subprocess.run(["git", "status", "--porcelain"], cwd=REPO, capture_output=True, text=True).stdout.strip()
        return v + ("-dirty" if dirty else "")
    except Exception:
        return "manual"


# --- estado -------------------------------------------------------------------------

INFO = rf"""
set +e
echo "== versão no ar: $(basename "$(readlink -f {BASE}/current 2>/dev/null)" 2>/dev/null)"
docker ps -a --filter label=com.docker.compose.project={PROJECT} --format 'table {{{{.Names}}}}\t{{{{.Status}}}}\t{{{{.Image}}}}'
echo
ids=$(docker ps -q --filter label=com.docker.compose.project={PROJECT})
[ -n "$ids" ] && docker stats --no-stream --format 'table {{{{.Name}}}}\t{{{{.CPUPerc}}}}\t{{{{.MemUsage}}}}\t{{{{.NetIO}}}}' $ids
echo
echo "== dados: $(du -sh {BASE}/data 2>/dev/null | cut -f1)   releases: $(ls {BASE}/releases 2>/dev/null | wc -l)"
echo "== outras apps (só leitura, para conferir que seguem de pé):"
docker ps --format '{{{{.Label "com.docker.compose.project"}}}}' | sort | uniq -c | sed 's/^/   /'
"""


def cmd_info(_):
    p = remote(INFO, capture=True, timeout=60)
    sys.stdout.write(p.stdout.decode(errors="replace"))
    if p.returncode:
        sys.stderr.write(p.stderr.decode(errors="replace"))


def cmd_logs(a):
    svc = {"monitor": "vpserver-monitor", "dockerproxy": "vpserver-dockerproxy", "tunnel": "vpserver-tunnel"}.get(a.service, a.service)
    flag = "-f" if a.follow else ""
    subprocess.run(ssh_base() + [f"docker logs --tail {a.tail} {flag} {svc}"])


# --- deploy ----------------------------------------------------------------------------

def build_package(version):
    out = REPO / "dist"
    out.mkdir(exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
    print("==> compilando (linux/arm64)…")
    subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}",
                    "-o", str(out / "vpmon"), "./cmd/vpmon"], cwd=REPO, env=env, check=True)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        tar.add(out / "vpmon", arcname="vpmon")
        data = (version + "\n").encode()
        ti = tarfile.TarInfo("VERSION")
        ti.size = len(data)
        tar.addfile(ti, io.BytesIO(data))
        for f in sorted((REPO / "deploy").iterdir()):
            tar.add(f, arcname=f"deploy/{f.name}")
    return buf.getvalue()


def cmd_deploy(_):
    version = git_version()
    pkg = build_package(version)
    print(f"==> enviando {len(pkg) / 1e6:.1f} MB (versão {version})")
    p = subprocess.run(ssh_base() + [f"{BASE}/bin/receive"], input=pkg)
    sys.exit(p.returncode)


def cmd_restart(_):
    remote(f"set -e; {COMPOSE} up -d --force-recreate; docker ps --filter label=com.docker.compose.project={PROJECT} --format '{{{{.Names}}}}: {{{{.Status}}}}'")


def cmd_rollback(_):
    remote(rf"""
set -e
cd {BASE}
cur=$(readlink -f current)
prev=$(ls -1dt releases/*/ | while read -r d; do d=$(readlink -f "$d"); [ "$d" != "$cur" ] && echo "$d" && break; done)
[ -n "$prev" ] || {{ echo "não há versão anterior"; exit 1; }}
echo "==> voltando para $(basename "$prev")"
ln -sfn "$prev" current.new && mv -Tf current.new current
{COMPOSE} up -d --no-deps --force-recreate monitor
""")


# --- 1ª vez ------------------------------------------------------------------------------

def env_line(k, v):
    if "\n" in v or "$" in v:
        raise SystemExit(f"{k} não pode ter quebra de linha nem '$'")
    return f"{k}={v}\n"


def cmd_setup(_):
    token = os.environ.get("VPMON_SETUP_TUNNEL_TOKEN", "").strip()
    tfile = os.environ.get("VPMON_SETUP_TUNNEL_TOKEN_FILE")
    if not token and tfile:
        token = Path(tfile).read_text().strip()
    if not token:
        print("==> sem VPMON_SETUP_TUNNEL_TOKEN: o acesso será por Quick Tunnel (endereço trycloudflare.com; veja com `server.py url`)")
    password = os.environ.get("VPMON_SETUP_PASSWORD") or secrets.token_urlsafe(18)
    example = (REPO / "deploy" / "env.example").read_text(encoding="utf-8")
    env = []
    for ln in example.splitlines(keepends=True):
        if ln.startswith("VPMON_PASSWORD="):
            ln = env_line("VPMON_PASSWORD", password)
        elif ln.startswith("VPMON_TUNNEL_TOKEN="):
            ln = env_line("VPMON_TUNNEL_TOKEN", token)
        elif ln.startswith("VPMON_TUNNEL_COMMAND=") and not token:
            ln = env_line("VPMON_TUNNEL_COMMAND", "tunnel --no-autoupdate --url http://vpserver-monitor:8080")
        elif ln.startswith("VPMON_WA_KEY="):
            ln = env_line("VPMON_WA_KEY", secrets.token_hex(24))
        elif ln.startswith("VPMON_WA_DB_PASSWORD="):
            ln = env_line("VPMON_WA_DB_PASSWORD", secrets.token_hex(18))
        env.append(ln)
    env_b64 = base64.b64encode("".join(env).encode()).decode()
    receive_b64 = base64.b64encode((REPO / "deploy" / "receive.sh").read_bytes()).decode()
    p = remote(rf"""
set -e
sudo install -d -o ubuntu -g ubuntu -m 750 {BASE}
install -d -m 750 {BASE}/releases {BASE}/bin
sudo install -d -o 65532 -g 65532 -m 700 {BASE}/data
echo {receive_b64} | base64 -d > {BASE}/bin/receive && chmod 755 {BASE}/bin/receive
if [ -f {BASE}/.env ]; then
  echo "EXISTS"
else
  umask 077
  echo {env_b64} | base64 -d > {BASE}/.env
  gid=$(getent group docker | cut -d: -f3)
  sed -i "s/^DOCKER_GID=.*/DOCKER_GID=$gid/" {BASE}/.env
  chmod 600 {BASE}/.env
  echo "CREATED"
fi
""", capture=True)
    out = p.stdout.decode()
    if p.returncode:
        sys.stderr.write(p.stderr.decode())
        raise SystemExit(p.returncode)
    if "CREATED" in out:
        print("==> /opt/vpserver-monitoring criado.")
        print(f"    usuário: admin   senha inicial: {password}")
        print("    No primeiro acesso o painel obriga a trocar (e dá para trocar o usuário).")
    else:
        print("==> .env já existia: nada mudou nele. Pastas e bin/receive conferidos.")


def cmd_password(_):
    password = os.environ.get("VPMON_NEW_PASSWORD") or secrets.token_urlsafe(18)
    line = base64.b64encode(env_line("VPMON_PASSWORD", password).encode()).decode()
    p = remote(rf"""
set -e
cd {BASE}
new=$(echo {line} | base64 -d)
grep -v '^VPMON_PASSWORD=' .env > .env.tmp; echo "$new" >> .env.tmp
chmod 600 .env.tmp && mv .env.tmp .env
sudo rm -f {BASE}/data/auth.json   # senha trocada pela tela deixa de valer
{COMPOSE} up -d --no-deps --force-recreate monitor >/dev/null
echo ok
""", capture=True)
    if p.returncode:
        sys.stderr.write(p.stderr.decode())
        raise SystemExit(p.returncode)
    print(f"==> senha trocada (todas as sessões caíram). Nova senha: {password}")


def cmd_ci_key(a):
    pub = Path(a.pubkey).read_text().strip()
    if not pub.startswith(("ssh-ed25519 ", "ssh-rsa ", "ecdsa-")) or "\n" in pub:
        raise SystemExit("isso não parece uma chave pública SSH")
    parts = pub.split()
    line = f'restrict,command="{BASE}/bin/receive" {parts[0]} {parts[1]} vpserver-monitoring-ci'
    b64 = base64.b64encode(line.encode()).decode()
    remote(rf"""
set -e
f=~/.ssh/authorized_keys
touch "$f"; chmod 600 "$f"
grep -v 'vpserver-monitoring-ci$' "$f" > "$f.tmp" || true
echo {b64} | base64 -d >> "$f.tmp"; echo >> "$f.tmp"
mv "$f.tmp" "$f"; chmod 600 "$f"
echo "==> chave do CI autorizada (só consegue rodar {BASE}/bin/receive)"
""")


def cmd_url(_):
    p = remote("docker logs vpserver-tunnel 2>&1 | grep -o 'https://[a-z0-9-]*\\.trycloudflare\\.com' | tail -1", capture=True, timeout=30)
    url = p.stdout.decode().strip()
    print(url or "Sem Quick Tunnel (o painel deve estar no seu domínio, pelo túnel com token).")


def cmd_ssh(_):
    os.execvp("ssh", ssh_base(interactive=True))


def main():
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    ap = argparse.ArgumentParser(description="vpserver-monitoring no servidor (via SSH)")
    sub = ap.add_subparsers(dest="cmd")
    sub.add_parser("info", help="estado (padrão)")
    lg = sub.add_parser("logs", help="logs de um contêiner do painel")
    lg.add_argument("service", nargs="?", default="monitor")
    lg.add_argument("-f", "--follow", action="store_true")
    lg.add_argument("--tail", type=int, default=100)
    sub.add_parser("deploy", help="compila, envia e sobe")
    sub.add_parser("restart", help="recria os contêineres (relê o .env)")
    sub.add_parser("rollback", help="volta para a versão anterior")
    sub.add_parser("setup", help="1ª vez")
    sub.add_parser("password", help="troca a senha do painel")
    ck = sub.add_parser("ci-key", help="autoriza a chave de deploy do GitHub Actions")
    ck.add_argument("pubkey")
    sub.add_parser("url", help="endereço do Quick Tunnel")
    sub.add_parser("ssh", help="SSH interativo")
    a = ap.parse_args()
    {"info": cmd_info, None: cmd_info, "logs": cmd_logs, "deploy": cmd_deploy, "restart": cmd_restart,
     "rollback": cmd_rollback, "setup": cmd_setup, "password": cmd_password, "ci-key": cmd_ci_key,
     "url": cmd_url, "ssh": cmd_ssh}[a.cmd](a)


if __name__ == "__main__":
    main()
