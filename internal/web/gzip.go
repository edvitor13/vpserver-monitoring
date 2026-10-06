package web

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// O túnel da Cloudflare não comprime o caminho servidor → Cloudflare, então o
// painel comprime as próprias respostas (JSON e arquivos da tela ficam de 5 a
// 10 vezes menores). O streaming do chat (text/event-stream) passa sem
// compressão, senão os pedaços ficariam presos no buffer.

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression); return w }}

func compressible(ct string) bool {
	ct = strings.ToLower(ct)
	if strings.HasPrefix(ct, "text/event-stream") {
		return false
	}
	return strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "javascript") || strings.Contains(ct, "svg") || strings.Contains(ct, "manifest")
}

type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	started bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.started {
		g.started = true
		h := g.Header()
		if code != http.StatusNoContent && code != http.StatusNotModified && h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
			h.Del("Content-Length")
			h.Set("Content-Encoding", "gzip")
			h.Add("Vary", "Accept-Encoding")
			g.gz = gzPool.Get().(*gzip.Writer)
			g.gz.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.started {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.gz != nil {
		g.gz.Close()
		gzPool.Put(g.gz)
		g.gz = nil
	}
}

// withGzip comprime quando o cliente aceita gzip (a Cloudflare sempre aceita).
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.URL.Path == "/api/chat" {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
