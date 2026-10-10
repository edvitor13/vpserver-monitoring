package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/edvitor13/vpserver-monitoring/internal/ai"
)

// Idiomas da tela: o texto fica em português no app.js, dentro de T('…'), e cada
// outro idioma é um catálogo em static/i18n/<idioma>.js com a frase em português
// como chave. Estes testes seguram as duas pontas: tudo traduzido, nada sobrando,
// e nenhum texto novo escrito direto na tela sem passar pelo T().

// jsSegment é um pedaço de texto do app.js: string ou trecho de template.
type jsSegment struct {
	text string
	line int
	key  bool // primeiro argumento de T('…')
}

// scanJS separa strings, templates, comentários e regex do app.js (o suficiente
// para o nosso código: sem build, sem JSX).
func scanJS(src string) []jsSegment {
	var out []jsSegment
	r := []rune(src)
	n := len(r)
	line := 1
	var stack []int // profundidade de chaves dentro de cada ${ … } aberto
	lastSig := rune(0)
	lastWord := ""
	isWordRune := func(c rune) bool { return c == '_' || c == '$' || unicode.IsLetter(c) || unicode.IsDigit(c) }
	precededByT := func(start int) bool {
		j := start - 1
		for j >= 0 && (r[j] == ' ' || r[j] == '\n' || r[j] == '\t') {
			j--
		}
		if j < 1 || r[j] != '(' {
			return false
		}
		j--
		return r[j] == 'T' && (j == 0 || !isWordRune(r[j-1]))
	}
	readTemplate := func(i int) int { // a partir do caractere depois de ` ou de }
		var sb strings.Builder
		startLine := line
		for i < n {
			c := r[i]
			if c == '\\' && i+1 < n {
				sb.WriteRune(r[i+1])
				i += 2
				continue
			}
			if c == '\n' {
				line++
			}
			if c == '`' {
				out = append(out, jsSegment{text: sb.String(), line: startLine})
				return i + 1
			}
			if c == '$' && i+1 < n && r[i+1] == '{' {
				out = append(out, jsSegment{text: sb.String(), line: startLine})
				stack = append(stack, 0)
				return i + 2
			}
			sb.WriteRune(c)
			i++
		}
		return i
	}
	for i := 0; i < n; {
		c := r[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == '/' && i+1 < n && r[i+1] == '/':
			for i < n && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && r[i+1] == '*':
			i += 2
			for i+1 < n && !(r[i] == '*' && r[i+1] == '/') {
				if r[i] == '\n' {
					line++
				}
				i++
			}
			i += 2
		case c == '\'' || c == '"':
			start := i
			i++
			var sb strings.Builder
			for i < n && r[i] != c {
				if r[i] == '\\' && i+1 < n {
					sb.WriteRune(r[i+1])
					i += 2
					continue
				}
				sb.WriteRune(r[i])
				i++
			}
			i++
			out = append(out, jsSegment{text: sb.String(), line: line, key: precededByT(start)})
			lastSig = 'a'
		case c == '`':
			i = readTemplate(i + 1)
			lastSig = 'a'
		case c == '/' && (strings.ContainsRune("(,=:[!&|?{};+-*%<>~^", lastSig) || lastSig == 0 ||
			lastWord == "return" || lastWord == "typeof" || lastWord == "case"):
			// regex: até a barra que fecha (fora de [ … ])
			i++
			inClass := false
			for i < n && (r[i] != '/' || inClass) && r[i] != '\n' {
				if r[i] == '\\' {
					i += 2
					continue
				}
				if r[i] == '[' {
					inClass = true
				} else if r[i] == ']' {
					inClass = false
				}
				i++
			}
			i++
			for i < n && unicode.IsLetter(r[i]) {
				i++
			}
			lastSig = 'a'
		case c == '{':
			if len(stack) > 0 {
				stack[len(stack)-1]++
			}
			lastSig, lastWord = c, ""
			i++
		case c == '}':
			if len(stack) > 0 {
				if stack[len(stack)-1] == 0 {
					stack = stack[:len(stack)-1]
					i = readTemplate(i + 1)
					lastSig = 'a'
					continue
				}
				stack[len(stack)-1]--
			}
			lastSig, lastWord = c, ""
			i++
		case unicode.IsSpace(c):
			i++
		case isWordRune(c):
			j := i
			for j < n && isWordRune(r[j]) {
				j++
			}
			lastWord, lastSig = string(r[i:j]), 'a'
			i = j
		default:
			lastSig, lastWord = c, ""
			i++
		}
	}
	return out
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("static", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// catalog lê static/i18n/<lang>.js: (window.VPMON_I18N = …).<lang> = { JSON };
func catalog(t *testing.T, lang string) map[string]string {
	t.Helper()
	src := readStatic(t, "i18n/"+lang+".js")
	a, b := strings.Index(src, "= {"), strings.LastIndex(src, "}")
	if a < 0 || b < a {
		t.Fatalf("i18n/%s.js fora do formato", lang)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(src[a+2:b+1]), &m); err != nil {
		t.Fatalf("i18n/%s.js: %v", lang, err)
	}
	return m
}

var (
	rePlaceholder = regexp.MustCompile(`\{\d+\}`)
	reTag         = regexp.MustCompile(`</?(?:a|b|i|p|br|code|small|span|strong|em|kbd|div|ul|ol|li|table|thead|tbody|tr|th|td|details|summary)(?:\s[^>]*)?/?>`)
)

func sortedAll(re *regexp.Regexp, s string) string {
	l := re.FindAllString(s, -1)
	sort.Strings(l)
	return strings.Join(l, " ")
}

func TestScreenTextsHaveEnglish(t *testing.T) {
	keys := map[string]int{}
	for _, seg := range scanJS(readStatic(t, "app.js")) {
		if seg.key {
			keys[seg.text] = seg.line
		}
	}
	if len(keys) < 500 {
		t.Fatalf("poucas chaves achadas (%d): o leitor do app.js quebrou?", len(keys))
	}
	en := catalog(t, "en")
	var missing []string
	for k, line := range keys {
		v, ok := en[k]
		if !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, strconv.Itoa(line)+": "+k)
			continue
		}
		if sortedAll(rePlaceholder, k) != sortedAll(rePlaceholder, v) {
			t.Errorf("{n} diferentes na tradução de %q: %q", k, v)
		}
		if sortedAll(reTag, k) != sortedAll(reTag, v) {
			t.Errorf("tags HTML diferentes na tradução de %q: %q", k, v)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d texto(s) da tela sem inglês em i18n/en.js:\n%s", len(missing), strings.Join(missing, "\n"))
	}
	for k := range en {
		if _, ok := keys[k]; !ok {
			t.Errorf("tradução sobrando em i18n/en.js (o texto não existe mais na tela): %q", k)
		}
	}
}

// textAllow são textos com acento que ficam fora do T() de propósito.
var textAllow = map[string]bool{
	"Português": true, // nome do idioma, escrito no próprio idioma
	// recorta o nome que vem do servidor (catálogo de Notificações), que ainda é só em português
	"no resumo diário": true,
}

func TestNoUntranslatedScreenText(t *testing.T) {
	accent := regexp.MustCompile(`[À-ÖØ-öø-ÿ]`)
	for _, seg := range scanJS(readStatic(t, "app.js")) {
		if seg.key || textAllow[strings.TrimSpace(seg.text)] {
			continue
		}
		if accent.MatchString(seg.text) {
			t.Errorf("app.js:%d: texto de tela fora do T() (vai aparecer em português em todos os idiomas): %q", seg.line, seg.text)
		}
	}
}

func TestScanJSFindsKeysAndSkipsComments(t *testing.T) {
	src := "// comentário com acentuação\nconst a = T('Olá {0}', [x]); /* também ação */\n" +
		"const b = `<p>${T('Dentro')}</p>${cond ? `<i>${'ação solta'}</i>` : ''}`;\nconst re = /(senha|contraseña)/i;\nconst c = x / 2;\n"
	segs := scanJS(src)
	var keys, others []string
	for _, s := range segs {
		if s.key {
			keys = append(keys, s.text)
		} else {
			others = append(others, s.text)
		}
	}
	if strings.Join(keys, "|") != "Olá {0}|Dentro" {
		t.Fatalf("chaves: %q", keys)
	}
	if !strings.Contains(strings.Join(others, "|"), "ação solta") || strings.Contains(strings.Join(others, "|"), "contraseña") {
		t.Fatalf("outros textos: %q", others)
	}
}

func TestIndexLoadsCatalogBeforeApp(t *testing.T) {
	html := readStatic(t, "index.html")
	i, j := strings.Index(html, `src="i18n/en.js"`), strings.Index(html, `src="app.js"`)
	if i < 0 || j < 0 || i > j {
		t.Fatal("o index.html carrega o catálogo antes do app.js")
	}
	s := New(nil, NewAuth("u", "senha-muito-boa", "s", true, "", false), true, ai.Config{}, "", nil, nil)
	if !strings.Contains(string(s.assets["/index.html"].body), `src="i18n/en.js?v=`) {
		t.Fatal("o catálogo ganha o ?v= da versão, como o app.js")
	}
}

func TestAccountLanguage(t *testing.T) {
	a := NewAuth("chefe", "senha-muito-boa", "s", true, t.TempDir(), false)
	h := New(nil, a, true, ai.Config{}, "", nil, nil).Handler()
	c := sessionCookie(a, "chefe")
	me := func() map[string]any {
		req := httptest.NewRequest("GET", "/api/me", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var j map[string]any
		json.Unmarshal(rec.Body.Bytes(), &j)
		return j
	}
	if l := me()["lang"]; l != "" {
		t.Fatalf("sem escolha, o idioma fica vazio (vale o do navegador): %v", l)
	}
	if rec := postJSON(h, "/api/lang", `{"lang":"fr"}`, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("idioma desconhecido: %d", rec.Code)
	}
	if rec := postJSON(h, "/api/lang", `{"lang":"en"}`, c); rec.Code != 200 {
		t.Fatalf("guardar: %d %s", rec.Code, rec.Body)
	}
	if l := me()["lang"]; l != "en" {
		t.Fatalf("o idioma volta no /api/me (e a sessão continua): %v", l)
	}
	req := httptest.NewRequest("POST", "/api/lang", strings.NewReader(`{"lang":"en"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem login: %d", rec.Code)
	}
}
