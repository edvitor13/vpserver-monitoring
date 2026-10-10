// Package i18n traduz o texto que o servidor manda para a tela (mensagens de
// erro, alertas, Infos, catálogos). O código continua escrevendo em português;
// a tradução acontece na saída, por catálogo: o texto em português é a chave.
//
// O catálogo (en.json) tem dois mapas:
//   - exact: textos fixos ("Dados inválidos." → "Invalid data.");
//   - formats: formatos com valores, do jeito que estão no código
//     (fmt.Sprintf("Disco %.0f%% cheio", p) ou nome + " está unhealthy", que
//     vira "%s está unhealthy"). Um texto pronto é reconhecido pelo formato, os
//     valores são tirados dele, traduzidos também (ex.: "3 dias") e vão para o
//     formato em inglês, que pode trocar a ordem com %[2]s.
//
// O teste do pacote lê o código Go e falha se um texto de tela em português
// ficar sem tradução (ou se sobrar tradução de texto que não existe mais).
package i18n

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Default é o idioma do código (e de quem não diz o idioma, como um curl).
const Default = "pt-BR"

//go:embed en.json
var enJSON []byte

// File é o formato do catálogo.
type File struct {
	Exact   map[string]string `json:"exact"`
	Formats map[string]string `json:"formats"`
}

type pattern struct {
	re    *regexp.Regexp
	lit   string // maior pedaço fixo: filtro barato antes da regex
	score int    // total de texto fixo: o formato mais específico ganha
	out   string // formato traduzido, com todos os verbos como %s (índices mantidos)
}

type catalog struct {
	exact map[string]string
	pats  []pattern
}

var (
	once sync.Once
	cats map[string]*catalog
)

// verb acha os verbos do fmt (%s, %d, %.0f, %[2]s, %q, %w…) e o %% literal.
var (
	verb      = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*(\d+|\*)?(\.(\d+|\*))?[a-zA-Z%]`)
	verbIndex = regexp.MustCompile(`^%(\[\d+\])`)
)

func load() {
	cats = map[string]*catalog{}
	var f File
	if err := json.Unmarshal(enJSON, &f); err != nil {
		panic("i18n/en.json: " + err.Error())
	}
	cats["en"] = build(f)
}

func build(f File) *catalog {
	c := &catalog{exact: f.Exact}
	if c.exact == nil {
		c.exact = map[string]string{}
	}
	for pt, en := range f.Formats {
		if p, ok := compile(pt, en); ok {
			c.pats = append(c.pats, p)
		}
	}
	sort.SliceStable(c.pats, func(i, j int) bool { return c.pats[i].score > c.pats[j].score })
	return c
}

// compile transforma o formato em português numa regex com um grupo por valor.
func compile(pt, en string) (pattern, bool) {
	var b strings.Builder
	b.WriteString(`(?s)^`)
	last, score, lit := 0, 0, ""
	addLit := func(s string) {
		b.WriteString(regexp.QuoteMeta(s))
		score += len(s)
		if len(s) > len(lit) {
			lit = s
		}
	}
	for _, m := range verb.FindAllStringIndex(pt, -1) {
		addLit(pt[last:m[0]])
		v := pt[m[0]:m[1]]
		switch v[len(v)-1] {
		case '%':
			addLit("%")
		case 'd':
			b.WriteString(`(-?\d+)`)
		case 'f', 'g':
			b.WriteString(`(-?[\d.,]+)`)
		default:
			b.WriteString(`(.*?)`)
		}
		last = m[1]
	}
	addLit(pt[last:])
	b.WriteString(`$`)
	// pouco texto fixo ("%s: %s") reconheceria qualquer coisa
	letters := 0
	for _, r := range verb.ReplaceAllString(pt, "") {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 3 {
		return pattern{}, false
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return pattern{}, false
	}
	out := verb.ReplaceAllStringFunc(en, func(v string) string {
		if v == "%%" {
			return v
		}
		if m := verbIndex.FindStringSubmatch(v); m != nil {
			return "%" + m[1] + "s"
		}
		return "%s"
	})
	return pattern{re: re, lit: lit, score: score, out: out}, true
}

func get(lang string) *catalog {
	once.Do(load)
	return cats[lang]
}

// Tr traduz um texto pronto (fixo ou montado por um formato conhecido). Sem
// tradução, devolve o próprio texto.
func Tr(lang, s string) string {
	c := get(lang)
	if c == nil || s == "" {
		return s
	}
	return c.tr(s, 0)
}

func (c *catalog) tr(s string, depth int) string {
	if v, ok := c.exact[s]; ok {
		return v
	}
	if depth > 2 || len(s) > 2000 || !strings.ContainsAny(s, " \n") {
		return s
	}
	for _, p := range c.pats {
		if p.lit != "" && !strings.Contains(s, p.lit) {
			continue
		}
		m := p.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		args := make([]any, len(m)-1)
		for i, v := range m[1:] {
			args[i] = localNum(c.tr(v, depth+1))
		}
		return fmt.Sprintf(p.out, args...)
	}
	// listas montadas com " · " ou " — " (ex.: "3 dias · ligado há 2 h"): parte por parte
	for _, sep := range []string{" · ", " — "} {
		if !strings.Contains(s, sep) {
			continue
		}
		parts := strings.Split(s, sep)
		changed := false
		for i, part := range parts {
			if t := c.tr(part, depth+1); t != part {
				parts[i], changed = t, true
			}
		}
		if changed {
			return strings.Join(parts, sep)
		}
	}
	return s
}

// número em português (1.234,5) dentro de um valor vira o do inglês (1,234.5)
var ptNum = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{3})+|\d+),(\d+)\b`)

func localNum(s string) string {
	return ptNum.ReplaceAllStringFunc(s, func(n string) string {
		m := ptNum.FindStringSubmatch(n)
		return strings.ReplaceAll(m[1], ".", ",") + "." + m[2]
	})
}

// JSON traduz os textos de uma resposta JSON. Os valores sob as chaves de skip
// (logs, saída de comando, nomes de arquivo…) ficam como estão. Se não der para
// ler, devolve o corpo original.
func JSON(lang string, body []byte, skip map[string]bool) []byte {
	c := get(lang)
	if c == nil || len(body) > 8<<20 {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return body
	}
	v = c.walk(v, skip)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	if err := enc.Encode(v); err != nil {
		return body
	}
	return out.Bytes()
}

func (c *catalog) walk(v any, skip map[string]bool) any {
	switch t := v.(type) {
	case string:
		return c.tr(t, 0)
	case []any:
		for i := range t {
			t[i] = c.walk(t[i], skip)
		}
	case map[string]any:
		for k, x := range t {
			if !skip[k] {
				t[k] = c.walk(x, skip)
			}
		}
	}
	return v
}

// Has diz se o idioma tem catálogo (o padrão não precisa).
func Has(lang string) bool { return get(lang) != nil }

// Use troca o catálogo de um idioma (testes).
func Use(lang string, f File) {
	once.Do(load)
	cats[lang] = build(f)
}
