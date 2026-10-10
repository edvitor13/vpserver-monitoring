package i18n

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Textos de tela no código Go: o que pode chegar à tela (mensagens de erro,
// alertas, catálogos, avisos). Ficam de fora o log interno (slog), a linha de
// comando (cmd/, setup/), os prompts da IA e o rc do shell do SSH.

var (
	accent  = regexp.MustCompile(`[À-ÖØ-öø-ÿ]`)
	ptWords = regexp.MustCompile(`(?i)\b(nao|para|com|sem|uma|dos|das|pelo|pela|ainda|agora|quando|servidor|painel|banco|disco|senha|erro|falhou|parou|caiu|rodando|ligado|desligado|nenhum|nenhuma|aqui|depois|antes|hoje|ontem|dias|horas|livre|cheio|avisos|resumo|outros|outras|todos|todas|sistema|isso|esse|essa|este|esta|deste|desta|neste|nesta|nesse|nessa|nada|menos|tempo|seu|sua|algum|alguma|mesmo|mesma|cada|vezes|dele|dela|nunca|sempre|tela|entrada|saida|limite|parado|parada|pausado|pausada|retomado|retomada|ligada|desligada|ativo|ativa)\b`)
	letters = regexp.MustCompile(`[A-Za-zÀ-ÿ]{2,}`)
)

// fora do catálogo
var (
	skipDirs  = []string{"internal/xcrypto", "internal/setup", "internal/i18n", "internal/sshchat/sshtest"}
	skipFiles = map[string]bool{"internal/monitor/cloud.go": true} // nomes de cidade
	// prompts e scripts: vão para a IA ou para o shell, não para a tela
	skipDecls = regexp.MustCompile(`(?i)prompt|^whatsappStyle$|^rcScript$|^(weekday|month)Names$`)
	// campos que são código (a tela compara ou mapeia): nunca traduzir
	codeFields = map[string]bool{"Area": true, "Level": true, "Key": true, "Kind": true, "Project": true, "Service": true}
	// textos que não são de tela: rótulos do Docker, nomes técnicos, usuário de mentira do login
	skipTexts = regexp.MustCompile(`^(com\.docker\.|keepalive@|painel-central:|vpmon-usuario-)`)
	skipFuncs = map[string]bool{"setupScript": true, "revokeScript": true}
	// em monitor/ai.go, só os rótulos das ferramentas aparecem na tela (o resto é para a IA)
	onlyFuncs = map[string]map[string]bool{"internal/monitor/ai.go": {"Label": true}}
	skipCalls = regexp.MustCompile(`^(slog\.|regexp\.|strings\.(Has|Contains|Trim|Index|Cut|Split|Replace|Count|EqualFold|Fields)|errors\.Is|filepath\.|os\.|http\.)`)
	// argumentos que são código (chave, nível e área do alerta em add(chave, nível, área, título, detalhe, alvo))
	codeArgs = map[string]int{"add": 3}
	// chamadas cujo argumento é um formato do fmt (e em que posição)
	fmtCalls = map[string]int{"fmt.Sprintf": 0, "fmt.Errorf": 0, "fmt.Printf": 0, "fmt.Fprintf": 1, "line": 0, "list": 1}
)

type keys struct {
	exact   map[string]string // texto -> onde
	formats map[string]string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func isPT(s string) bool {
	return letters.MatchString(s) && (accent.MatchString(s) || ptWords.MatchString(s))
}

func extract(t *testing.T) keys {
	t.Helper()
	root := repoRoot(t)
	k := keys{exact: map[string]string{}, formats: map[string]string{}}
	filepath.Walk(filepath.Join(root, "internal"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			for _, d := range skipDirs {
				if rel == d {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || skipFiles[rel] {
			return nil
		}
		fs := token.NewFileSet()
		f, err := parser.ParseFile(fs, p, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		collect(f, fs, rel, &k)
		return nil
	})
	return k
}

func callName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		return "." + fn.Sel.Name
	}
	return ""
}

// concatFormat monta o formato de uma soma de strings: o texto fixo fica e o
// resto vira %s ("Disco " + x + " cheio" -> "Disco %s cheio").
func concatFormat(e ast.Expr) (string, bool) {
	var parts []string
	hasLit := false
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			walk(b.X)
			walk(b.Y)
			return
		}
		if p, ok := e.(*ast.ParenExpr); ok {
			walk(p.X)
			return
		}
		if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
			if s, err := strconv.Unquote(l.Value); err == nil {
				parts = append(parts, strings.ReplaceAll(s, "%", "%%"))
				hasLit = true
				return
			}
		}
		parts = append(parts, "%s")
	}
	walk(e)
	return strings.Join(parts, ""), hasLit
}

func collect(f *ast.File, fs *token.FileSet, rel string, k *keys) {
	var stack []ast.Node
	caseExprs := map[ast.Node]bool{}
	only := onlyFuncs[rel]
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		switch x := n.(type) {
		case *ast.ImportSpec, *ast.Field:
			return false
		case *ast.FuncDecl:
			if skipFuncs[x.Name.Name] || (only != nil && !only[x.Name.Name]) {
				return false
			}
		case *ast.GenDecl:
			if only != nil && x.Tok != token.IMPORT { // nesses arquivos, só as funções escolhidas
				return false
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && codeFields[id.Name] {
				return false
			}
		case *ast.ValueSpec:
			for _, nm := range x.Names {
				if skipDecls.MatchString(nm.Name) {
					return false
				}
			}
		case *ast.CallExpr:
			if skipCalls.MatchString(callName(x)) {
				return false
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				return false
			}
		case *ast.CaseClause:
			// case "texto": é comparação, não tela (o corpo do case é tela, como qualquer outro)
			for _, e := range x.List {
				caseExprs[e] = true
			}
		}
		if caseExprs[n] {
			return false
		}
		stack = append(stack, n)
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !isPT(s) || skipTexts.MatchString(s) {
			return true
		}
		where := rel + ":" + strconv.Itoa(fs.Position(lit.Pos()).Line)
		parent := stack[len(stack)-2]
		if c, ok := parent.(*ast.CallExpr); ok {
			if n, ok := codeArgs[callName(c)]; ok {
				for i := 0; i < n && i < len(c.Args); i++ {
					if c.Args[i] == lit {
						return true
					}
				}
			}
		}
		// soma de strings: o formato da soma inteira
		if b, ok := parent.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			top := len(stack) - 2
			for top > 0 {
				if pb, ok := stack[top-1].(*ast.BinaryExpr); ok && pb.Op == token.ADD {
					top--
					continue
				}
				if _, ok := stack[top-1].(*ast.ParenExpr); ok {
					top--
					continue
				}
				break
			}
			if fm, ok := concatFormat(stack[top].(ast.Expr)); ok && strings.Contains(fm, "%s") {
				k.formats[fm] = where
			}
			return true
		}
		if c, ok := parent.(*ast.CallExpr); ok {
			if i, ok := fmtCalls[callName(c)]; ok && i < len(c.Args) && c.Args[i] == lit && verb.MatchString(strings.ReplaceAll(s, "%%", "")) {
				k.formats[s] = where
				return true
			}
		}
		k.exact[s] = where
		return true
	})
}

func readCatalog(t *testing.T) File {
	t.Helper()
	b, err := os.ReadFile("en.json")
	if err != nil {
		t.Fatal(err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func countVerbs(s string) int {
	n := 0
	for _, v := range verb.FindAllString(s, -1) {
		if v != "%%" {
			n++
		}
	}
	return n
}

// TestServerTextsHaveEnglish: todo texto de tela do servidor tem inglês, e nada sobra.
// Com VPMON_I18N_DUMP=<arquivo>, grava o que falta (para traduzir).
func TestServerTextsHaveEnglish(t *testing.T) {
	k := extract(t)
	if len(k.exact)+len(k.formats) < 200 {
		t.Fatalf("poucos textos achados (%d): a leitura do código quebrou?", len(k.exact)+len(k.formats))
	}
	f := readCatalog(t)
	type miss struct {
		Exact   []string `json:"exact"`
		Formats []string `json:"formats"`
	}
	var m miss
	var lines []string
	for s, where := range k.exact {
		if strings.TrimSpace(f.Exact[s]) == "" {
			m.Exact = append(m.Exact, s)
			lines = append(lines, where+": "+strconv.Quote(s))
		}
	}
	for s, where := range k.formats {
		en, ok := f.Formats[s]
		if !ok || strings.TrimSpace(en) == "" {
			m.Formats = append(m.Formats, s)
			lines = append(lines, where+": "+strconv.Quote(s))
			continue
		}
		if countVerbs(s) != countVerbs(en) {
			t.Errorf("valores diferentes na tradução de %q: %q", s, en)
		}
	}
	sort.Strings(m.Exact)
	sort.Strings(m.Formats)
	sort.Strings(lines)
	if out := os.Getenv("VPMON_I18N_DUMP"); out != "" {
		b, _ := json.MarshalIndent(m, "", " ")
		os.WriteFile(out, b, 0o644)
	}
	if len(lines) > 0 {
		t.Errorf("%d texto(s) do servidor sem inglês em internal/i18n/en.json:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for s := range f.Exact {
		if _, ok := k.exact[s]; !ok {
			t.Errorf("tradução sobrando (exact): %q", s)
		}
	}
	for s := range f.Formats {
		if _, ok := k.formats[s]; !ok {
			t.Errorf("tradução sobrando (formats): %q", s)
		}
	}
}
