#!/usr/bin/env bash
# Sobe uma versão já extraída em /opt/vpserver-monitoring/releases/<versão>.
# Chamado por deploy/receive.sh (deploy do GitHub Actions ou do server.py).
#
# Só mexe no projeto Compose "vpserver-monitoring". Nunca: prune global,
# down -v, contêiner/rede/volume de outro projeto.
set -euo pipefail

HOME_DIR=/opt/vpserver-monitoring
REL="$1"
PROJECT=vpserver-monitoring
cd "$HOME_DIR"

chmod 755 "$REL/vpmon"
echo "==> versão: $("$REL/vpmon" version)"

prev=$(readlink -f current 2>/dev/null || true)
switch_to() { ln -sfn "$1" current.new && mv -Tf current.new current; }
compose() { docker compose -p "$PROJECT" -f "$HOME_DIR/current/deploy/compose.yml" --env-file "$HOME_DIR/.env" "$@"; }

switch_to "$REL"
echo "==> subindo"
compose up -d --remove-orphans
# o binário é montado do "current": recria o painel para pegar o novo
compose up -d --no-deps --force-recreate monitor

wait_healthy() {
  for _ in $(seq 1 30); do
    s=$(docker inspect -f '{{.State.Health.Status}}' vpserver-monitor 2>/dev/null || echo "?")
    [ "$s" = "healthy" ] && return 0
    sleep 2
  done
  return 1
}

if wait_healthy; then
  echo "==> no ar (healthy)"
else
  echo "!!! o painel não ficou saudável; últimas linhas do log:"
  docker logs --tail 30 vpserver-monitor 2>&1 || true
  if [ -n "$prev" ] && [ "$prev" != "$(readlink -f "$REL")" ] && [ -d "$prev" ]; then
    echo "==> voltando para $(basename "$prev")"
    switch_to "$prev"
    compose up -d --no-deps --force-recreate monitor
    wait_healthy && echo "==> versão anterior de volta" || echo "!!! a anterior também não subiu"
  fi
  exit 1
fi

# guarda as 5 versões mais recentes (para rollback) e apaga o resto
cur=$(readlink -f current)
ls -1dt "$HOME_DIR"/releases/*/ 2>/dev/null | tail -n +6 | while read -r d; do
  [ "$(readlink -f "$d")" = "$cur" ] || rm -rf "$d"
done
docker ps --filter "label=com.docker.compose.project=$PROJECT" --format '    {{.Names}}: {{.Status}}'
