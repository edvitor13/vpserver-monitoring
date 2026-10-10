package backup

import (
	"bytes"
	"errors"
	"strings"
)

// Engine é um tipo de banco: como reconhecer, como tirar o dump e como
// conferir a saída. Os comandos rodam com `sh -c` DENTRO do contêiner do banco
// e são fixos: as credenciais vêm das variáveis do próprio contêiner e o nome
// do banco escolhido na tela chega só como a variável VPMON_DB (nunca é colado
// no comando).
type Engine struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Ext     string   `json:"ext"`     // extensão do arquivo (antes do .age)
	Restore []string `json:"restore"` // como restaurar (comandos genéricos para a tela)
	script  string
	check   func(head, tail []byte) error
}

var errBadDump = errors.New("a saída não parece um dump completo")

var Engines = []*Engine{
	{
		Key: "postgres", Name: "PostgreSQL", Ext: "dump",
		script: `set -e
U="${POSTGRES_USER:-${POSTGRESQL_USERNAME:-postgres}}"
export PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRESQL_PASSWORD:-}}"
D="${VPMON_DB:-${POSTGRES_DB:-${POSTGRESQL_DATABASE:-$U}}}"
exec pg_dump -Fc -U "$U" -d "$D"`,
		check: func(head, _ []byte) error {
			if !bytes.HasPrefix(head, []byte("PGDMP")) {
				return errBadDump
			}
			return nil
		},
		Restore: []string{
			"age -d -i chave.txt ARQUIVO.dump.age > banco.dump",
			"docker exec -i <contêiner> pg_restore -U <usuário> -d <banco> --clean --if-exists < banco.dump",
		},
	},
	{
		Key: "mysql", Name: "MySQL / MariaDB", Ext: "sql",
		script: `set -e
if command -v mariadb-dump >/dev/null 2>&1; then DUMP=mariadb-dump; else DUMP=mysqldump; fi
P="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}}"
if [ -n "$P" ]; then U=root; else U="${MYSQL_USER:-${MARIADB_USER:-root}}"; P="${MYSQL_PASSWORD:-${MARIADB_PASSWORD:-}}"; fi
export MYSQL_PWD="$P"
if [ -n "${VPMON_DB:-}" ]; then
  exec "$DUMP" --single-transaction --routines --triggers --no-tablespaces -u"$U" --databases "$VPMON_DB"
elif [ "$U" = root ]; then
  exec "$DUMP" --single-transaction --routines --triggers --events -u"$U" --all-databases
else
  exec "$DUMP" --single-transaction --no-tablespaces -u"$U" --databases "${MYSQL_DATABASE:-${MARIADB_DATABASE:-}}"
fi`,
		check: func(_, tail []byte) error {
			if !bytes.Contains(tail, []byte("-- Dump completed")) {
				return errBadDump
			}
			return nil
		},
		Restore: []string{
			"age -d -i chave.txt ARQUIVO.sql.age > banco.sql",
			`docker exec -i <contêiner> sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD"' < banco.sql`,
		},
	},
	{
		Key: "mongo", Name: "MongoDB", Ext: "archive.gz",
		script: `set -e
if [ -n "${MONGO_INITDB_ROOT_USERNAME:-}" ]; then
  if [ -n "${VPMON_DB:-}" ]; then
    exec mongodump --archive --gzip --username "$MONGO_INITDB_ROOT_USERNAME" --password "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin --db "$VPMON_DB"
  fi
  exec mongodump --archive --gzip --username "$MONGO_INITDB_ROOT_USERNAME" --password "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin
fi
if [ -n "${VPMON_DB:-}" ]; then exec mongodump --archive --gzip --db "$VPMON_DB"; fi
exec mongodump --archive --gzip`,
		check: func(head, _ []byte) error {
			if !bytes.HasPrefix(head, []byte{0x1f, 0x8b}) { // gzip
				return errBadDump
			}
			return nil
		},
		Restore: []string{
			"age -d -i chave.txt ARQUIVO.archive.gz.age > banco.archive.gz",
			"docker exec -i <contêiner> mongorestore --archive --gzip --drop [--username ... --authenticationDatabase admin] < banco.archive.gz",
		},
	},
	{
		Key: "redis", Name: "Redis / Valkey", Ext: "rdb",
		script: `set -e
if command -v redis-cli >/dev/null 2>&1; then C=redis-cli; else C=valkey-cli; fi
P="${REDIS_PASSWORD:-${VALKEY_PASSWORD:-}}"
if [ -n "$P" ]; then export REDISCLI_AUTH="$P"; fi
T="$(mktemp)"
trap 'rm -f "$T"' EXIT
"$C" --rdb "$T" >&2
cat "$T"`,
		check: func(head, _ []byte) error {
			if !bytes.HasPrefix(head, []byte("REDIS")) {
				return errBadDump
			}
			return nil
		},
		Restore: []string{
			"age -d -i chave.txt ARQUIVO.rdb.age > dump.rdb",
			"Pare o Redis, troque o dump.rdb da pasta de dados dele por este arquivo e suba de novo.",
		},
	},
}

func engineByKey(k string) *Engine {
	for _, e := range Engines {
		if e.Key == k {
			return e
		}
	}
	return nil
}

// notDB são imagens com nome de banco que não são o banco (painéis, exportadores...).
var notDB = []string{"admin", "express", "commander", "insight", "exporter", "adminer", "backup", "proxy", "pgbouncer", "pgpool", "operator", "ui"}

// Detect diz que banco roda numa imagem ("" = nenhum).
func Detect(image string) string {
	img := strings.ToLower(image)
	if i := strings.LastIndexByte(img, '@'); i >= 0 { // tira o digest
		img = img[:i]
	}
	if i := strings.LastIndexByte(img, '/'); i >= 0 { // fica o nome, sem registro/dono
		img = img[i+1:]
	}
	if i := strings.IndexByte(img, ':'); i >= 0 { // e sem a tag
		img = img[:i]
	}
	for _, w := range notDB {
		if strings.Contains(img, w) {
			return ""
		}
	}
	switch {
	case strings.Contains(img, "postgres") || strings.Contains(img, "postgis") || strings.Contains(img, "timescale") || img == "pgvector":
		return "postgres"
	case strings.Contains(img, "mysql") || strings.Contains(img, "mariadb") || strings.Contains(img, "percona"):
		return "mysql"
	case strings.HasPrefix(img, "mongo"):
		return "mongo"
	case strings.Contains(img, "redis") || strings.Contains(img, "valkey") || strings.Contains(img, "keydb"):
		return "redis"
	}
	return ""
}
