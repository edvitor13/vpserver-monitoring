#!/bin/sh
# Próxima versão do VPServer (o CI chama a cada merge na master).
#
#   next-version.sh <última tag, ex. v1.4.2, ou vazio> [etiquetas do PR...]
#
# Etiqueta "breaking" sobe a maior (2.0.0), "enhancement" a menor (1.5.0) e
# qualquer outra (bug, docs, nenhuma) a correção (1.4.3). Sem tag ainda: 1.0.0.
set -eu
tag="${1:-}"
[ $# -gt 0 ] && shift
last="${tag#v}"
if [ -z "$last" ]; then
  echo 1.0.0
  exit 0
fi
case "$last" in
  *.*.*.*|*..*) echo "tag fora do formato vX.Y.Z: $tag" >&2; exit 1 ;;
  *.*.*) ;;
  *) echo "tag fora do formato vX.Y.Z: $tag" >&2; exit 1 ;;
esac
ma="${last%%.*}"; rest="${last#*.}"; mi="${rest%%.*}"; pa="${rest#*.}"
for n in "$ma" "$mi" "$pa"; do
  case "$n" in ""|*[!0-9]*) echo "tag fora do formato vX.Y.Z: $tag" >&2; exit 1;; esac
done
bump=patch
for l in "$@"; do
  case "$l" in
    breaking) bump=major ;;
    enhancement) [ "$bump" = major ] || bump=minor ;;
  esac
done
case "$bump" in
  major) echo "$((ma + 1)).0.0" ;;
  minor) echo "$ma.$((mi + 1)).0" ;;
  *) echo "$ma.$mi.$((pa + 1))" ;;
esac
