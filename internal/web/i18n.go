package web

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/edvitor13/vpserver-monitoring/internal/i18n"
)

// Idioma do texto que o servidor manda (veja internal/i18n): a tela diz o
// idioma em cada pedido (X-VPMon-Lang). Sem ele (curl, outros clientes), tudo
// sai em português, como sempre.

func reqLang(r *http.Request) string {
	if l := r.Header.Get("X-VPMon-Lang"); Langs[l] && i18n.Has(l) {
		return l
	}
	return i18n.Default
}

// noTranslate são chaves do JSON cujo valor é dado, nunca texto da tela:
// linhas de log, saída e histórico de comandos do SSH, registro do SSH,
// arquivos do bucket e os scripts de preparo.
var noTranslate = map[string]bool{"lines": true, "blocks": true, "history": true, "entries": true, "objects": true,
	"setup": true, "revoke": true, "out": true, "cmd": true}

// translate traduz as respostas JSON da API para o idioma do pedido. Streams
// (SSE da IA), downloads e o que não é JSON passam direto.
func (s *Server) translate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := reqLang(r)
		if lang == i18n.Default || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		tw := &trWriter{ResponseWriter: w, lang: lang}
		next.ServeHTTP(tw, r)
		tw.finish()
	})
}

type trWriter struct {
	http.ResponseWriter
	lang   string
	status int
	buf    bytes.Buffer
	pass   bool // não é JSON: escreve direto
	wrote  bool
}

func (t *trWriter) WriteHeader(code int) {
	if t.wrote {
		return
	}
	t.wrote, t.status = true, code
	if !strings.HasPrefix(t.Header().Get("Content-Type"), "application/json") {
		t.pass = true
		t.ResponseWriter.WriteHeader(code)
	}
}

func (t *trWriter) Write(b []byte) (int, error) {
	if !t.wrote {
		t.WriteHeader(http.StatusOK)
	}
	if t.pass {
		return t.ResponseWriter.Write(b)
	}
	return t.buf.Write(b)
}

func (t *trWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok && t.pass {
		f.Flush()
	}
}

func (t *trWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func (t *trWriter) finish() {
	if t.pass || !t.wrote {
		return
	}
	out := i18n.JSON(t.lang, t.buf.Bytes(), noTranslate)
	t.Header().Del("Content-Length")
	t.ResponseWriter.WriteHeader(t.status)
	t.ResponseWriter.Write(out)
}

// aiLangNote vai no fim do prompt da IA quando a tela não está em português.
func aiLangNote(lang string) string {
	if lang == "en" {
		return "\n\nIMPORTANT: the person is using the panel in English. Answer in English."
	}
	return ""
}
