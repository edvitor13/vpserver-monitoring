#!/usr/bin/env bash
# Recebe uma versão empacotada (.tar.gz) pela entrada padrão e sobe.
#
# Instalado em /opt/vpserver-monitoring/bin/receive. É o ÚNICO comando que a
# chave de deploy do GitHub Actions consegue rodar no servidor
# (authorized_keys: restrict,command="/opt/vpserver-monitoring/bin/receive").
# O server.py usa o mesmo caminho no deploy manual.
#
# O pacote tem: vpmon (binário linux/arm64), VERSION e deploy/.
set -euo pipefail

HOME_DIR=/opt/vpserver-monitoring
exec 9>"$HOME_DIR/.deploy.lock"
flock -w 600 9 || { echo "outro deploy em andamento"; exit 1; }

mkdir -p "$HOME_DIR/releases"
tmp=$(mktemp -d "$HOME_DIR/releases/.in-XXXXXX")
trap 'rm -rf "$tmp"' EXIT
head -c 200000000 | tar -xz -C "$tmp" --no-same-owner
[ -x "$tmp/vpmon" ] || [ -f "$tmp/vpmon" ] || { echo "pacote sem o binário vpmon"; exit 1; }

v=$(tr -cd 'a-zA-Z0-9._-' < "$tmp/VERSION" 2>/dev/null | head -c 40 || true)
dest="$HOME_DIR/releases/$(date -u +%Y%m%d-%H%M%S)-${v:-manual}"
mv "$tmp" "$dest"
trap - EXIT

# mantém o próprio receive atualizado com o que veio no pacote
install -m 755 "$dest/deploy/receive.sh" "$HOME_DIR/bin/receive.new" && mv -f "$HOME_DIR/bin/receive.new" "$HOME_DIR/bin/receive"

exec bash "$dest/deploy/on-server.sh" "$dest"
