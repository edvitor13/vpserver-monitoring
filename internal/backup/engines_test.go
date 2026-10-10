package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := map[string]string{
		"postgres:17-alpine": "postgres", "docker.io/library/postgres@sha256:abc": "postgres", "postgis/postgis:16-3.4": "postgres",
		"timescale/timescaledb:latest-pg16": "postgres", "pgvector/pgvector:pg16": "postgres", "bitnami/postgresql:16": "postgres",
		"mysql:8.4": "mysql", "mariadb:11": "mysql", "percona/percona-server:8.0": "mysql",
		"mongo:7": "mongo", "redis:7-alpine": "redis", "valkey/valkey:8": "redis", "eqalpha/keydb": "redis",
		"dpage/pgadmin4": "", "mongo-express": "", "rediscommander/redis-commander": "", "redis/redisinsight": "",
		"adminer": "", "prometheuscommunity/postgres-exporter": "", "bitnami/pgbouncer": "", "nginx:1.27": "", "ghcr.io/acme/api:1.2": "",
	}
	for img, want := range cases {
		if got := Detect(img); got != want {
			t.Errorf("%s: %q (esperava %q)", img, got, want)
		}
	}
}

// fakeBin cria programas falsos que imprimem os argumentos (um por linha) e
// algumas variáveis, para conferir o que cada script manda rodar.
func fakeBin(t *testing.T, names ...string) string {
	dir := t.TempDir()
	for _, n := range names {
		body := "#!/bin/sh\necho \"PROG=" + n + "\"\nfor a in \"$@\"; do echo \"ARG=$a\"; done\necho \"PGPASSWORD=${PGPASSWORD:-}\"\necho \"MYSQL_PWD=${MYSQL_PWD:-}\"\necho \"REDISCLI_AUTH=${REDISCLI_AUTH:-}\"\n"
		if n == "redis-cli" || n == "valkey-cli" { // --rdb <arquivo>: grava o arquivo
			body = "#!/bin/sh\necho \"REDIS0011 auth=${REDISCLI_AUTH:-}\" > \"$2\"\n"
		}
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runScript(t *testing.T, eng string, bin string, env ...string) []string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sem sh")
	}
	cmd := exec.Command(sh, "-c", engineByKey(eng).script)
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	cmd.Env = append([]string{"PATH=" + bin + sep + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", eng, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func has(lines []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, l := range lines {
			if strings.TrimRight(l, "\r") == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestScriptsUseContainerEnvAndSafeDatabaseName(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("sh"); err != nil {
			t.Skip("sem sh")
		}
	}
	bin := fakeBin(t, "pg_dump", "mysqldump", "mariadb-dump", "mongodump", "redis-cli")
	evil := `x"; touch /tmp/hackeado; echo "`

	// PostgreSQL: usuário, senha e banco do contêiner; o escolhido na tela chega intacto como um argumento só
	out := runScript(t, "postgres", bin, "POSTGRES_USER=app", "POSTGRES_PASSWORD=s3nha", "POSTGRES_DB=principal")
	if !has(out, "PROG=pg_dump", "ARG=-Fc", "ARG=-U", "ARG=app", "ARG=-d", "ARG=principal", "PGPASSWORD=s3nha") {
		t.Fatalf("postgres: %v", out)
	}
	out = runScript(t, "postgres", bin, "VPMON_DB="+evil)
	if !has(out, "ARG=postgres", "ARG="+evil) {
		t.Fatalf("postgres com nome estranho: %v", out)
	}

	// MySQL/MariaDB: prefere o mariadb-dump; root com --all-databases; senha só pelo ambiente
	out = runScript(t, "mysql", bin, "MARIADB_ROOT_PASSWORD=raiz")
	if !has(out, "PROG=mariadb-dump", "ARG=-uroot", "ARG=--all-databases", "MYSQL_PWD=raiz") || has(out, "ARG=raiz") {
		t.Fatalf("mariadb: %v", out)
	}
	out = runScript(t, "mysql", bin, "MYSQL_USER=app", "MYSQL_PASSWORD=p", "MYSQL_DATABASE=loja")
	if !has(out, "ARG=-uapp", "ARG=--databases", "ARG=loja", "MYSQL_PWD=p") {
		t.Fatalf("mysql sem root: %v", out)
	}
	out = runScript(t, "mysql", bin, "MYSQL_ROOT_PASSWORD=r", "VPMON_DB="+evil)
	if !has(out, "ARG=--databases", "ARG="+evil) {
		t.Fatalf("mysql com nome estranho: %v", out)
	}

	// MongoDB: com e sem usuário raiz
	out = runScript(t, "mongo", bin, "MONGO_INITDB_ROOT_USERNAME=adm", "MONGO_INITDB_ROOT_PASSWORD=pw", "VPMON_DB=vendas")
	if !has(out, "PROG=mongodump", "ARG=--archive", "ARG=--gzip", "ARG=adm", "ARG=pw", "ARG=admin", "ARG=--db", "ARG=vendas") {
		t.Fatalf("mongo: %v", out)
	}
	out = runScript(t, "mongo", bin)
	if !has(out, "ARG=--archive") || has(out, "ARG=--username") {
		t.Fatalf("mongo sem usuário: %v", out)
	}

	// Redis: grava num temporário e devolve o arquivo; senha pelo ambiente
	out = runScript(t, "redis", bin, "REDIS_PASSWORD=rp")
	if !has(out, "REDIS0011 auth=rp") {
		t.Fatalf("redis: %v", out)
	}
	if _, err := os.Stat("/tmp/hackeado"); err == nil {
		t.Fatal("o nome do banco virou comando")
	}
}

func TestChecks(t *testing.T) {
	ok := map[string][2]string{
		"postgres": {"PGDMP\x01\x0e", ""}, "mysql": {"-- MySQL dump", "\n-- Dump completed on 2026-10-09\n"},
		"mongo": {"\x1f\x8b\x08", ""}, "redis": {"REDIS0011", ""},
	}
	for k, v := range ok {
		e := engineByKey(k)
		if err := e.check([]byte(v[0]), []byte(v[1])); err != nil {
			t.Errorf("%s: dump válido recusado: %v", k, err)
		}
		if err := e.check([]byte("pg_dump: error: connection failed"), []byte("cortado")); err == nil {
			t.Errorf("%s: saída inválida aceita", k)
		}
	}
}
