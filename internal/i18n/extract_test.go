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
	ptWords = regexp.MustCompile(`(?i)\b(nao|para|com|sem|uma|dos|das|pelo|pela|ainda|agora|quando|servidor|painel|banco|disco|senha|erro|falhou|parou|caiu|rodando|ligado|desligado|nenhum|nenhuma|aqui|depois|antes|hoje|ontem|dias|horas|livre|cheio|avisos|resumo|outros|outras|todos|todas|sistema|isso|esse|essa|este|esta|deste|desta|neste|nesta|nesse|nessa|nada|menos|tempo|seu|sua|algum|alguma|mesmo|mesma|cada|vezes|dele|dela|nunca|sempre|tela|entrada|saida|limite|parado|parada|pausado|pausada|retomado|retomada|ligada|desligada|ativo|ativa|urgente|alerta|alertas|aviso|piorou|continua|mais|idioma|desconhecido|de|em|ao|aos|nos|nas|durou|resolvido|resolvidos|imagem|nova|novo|atualizada|atualizado|removida|removido|entrou|ligou|trocada|teste|enviado|enviada|falhar|falha|fora|dentro|ficou|veja|confira|tente|use|abra|ou)\b`)
	// conectivos curtos que o inglês e o shell também têm ("no", "do", "set -e"): só contam
	// entre palavras, para achar português sem acento ("Logs do %s: %s e crescendo")
	ptJoin  = regexp.MustCompile(`(?:^|\s)(?:e|do|da|no|na)\s+[A-Za-zÀ-ÿ]`)
	letters = regexp.MustCompile(`[A-Za-zÀ-ÿ]{2,}`)
)

// fora do catálogo
var (
	skipDirs  = []string{"internal/xcrypto", "internal/setup", "internal/i18n", "internal/sshchat/sshtest"}
	skipFiles = map[string]bool{"internal/monitor/cloud.go": true} // nomes de cidade
	// prompts e scripts: vão para a IA ou para o shell, não para a tela
	skipDecls = regexp.MustCompile(`(?i)prompt|^whatsappStyle$|^rcScript$`)
	// campos que são código (a tela compara ou mapeia): nunca traduzir
	codeFields = map[string]bool{"Area": true, "Level": true, "Key": true, "Kind": true, "Project": true, "Service": true}
	// textos que não são de tela: rótulos do Docker, nomes técnicos, usuário de mentira do login, o
	// arquivo de teste do bucket (nome e conteúdo), os últimos comandos que vão para a IA do SSH e
	// comandos do shell (o autocompletar do SSH)
	skipTexts = regexp.MustCompile(`^(com\.docker\.|keepalive@|painel-central:|vpmon-usuario-|teste do VPServer: pode apagar|\$ %s)|\.vpserver-teste$|2>/dev/null`)
	skipFuncs = map[string]bool{"setupScript": true, "revokeScript": true}
	// arquivos em que só algumas declarações são tela: em monitor/ai.go, os rótulos das
	// consultas (aiLabel e os nomes que ele usa; o resto vai para a IA); em cleanup/message.go,
	// o aviso do WhatsApp. Nelas, todo texto com letras é de tela, mesmo sem acento ("CPU total",
	// "• Logs (%s): %s"), menos as chaves dos mapas. O teste falha se uma delas sumir ou ficar sem
	// texto (a lista apontava para Label, que só chama aiLabel)
	onlyDecls = map[string]map[string]bool{
		"internal/monitor/ai.go":      {"aiLabel": true, "fieldNames": true, "rangeLabel": true},
		"internal/cleanup/message.go": {"Message": true, "itemNames": true, "title": true},
	}
	skipCalls = regexp.MustCompile(`^(slog\.|regexp\.|strings\.(Has|Contains|Trim|Index|Cut|Split|Replace|Count|EqualFold|Fields)|errors\.Is|filepath\.|os\.|http\.)`)
	// argumentos que são código (chave, nível e área do alerta em add(chave, nível, área, título, detalhe, alvo))
	codeArgs = map[string]int{"add": 3}
	// campos posicionais que são código: chave e grupo de cada notify.Kind ({"app_down", "alertas", rótulo, …})
	codeElems = map[string]int{"Kind": 2}
	// chamadas cujo argumento é um formato do fmt (e em que posição)
	fmtCalls = map[string]int{"fmt.Sprintf": 0, "fmt.Errorf": 0, "fmt.Printf": 0, "fmt.Fprintf": 1, "line": 0, "list": 1, "top": 0}
)

type keys struct {
	exact   map[string]string // texto -> onde
	formats map[string]string
	used    map[string]bool // "arquivo:nome" das declarações de onlyDecls que deram texto
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
	return letters.MatchString(s) && (accent.MatchString(s) || ptWords.MatchString(s) || ptJoin.MatchString(s))
}

func extract(t *testing.T) keys {
	t.Helper()
	root := repoRoot(t)
	k := keys{exact: map[string]string{}, formats: map[string]string{}, used: map[string]bool{}}
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

// listed diz se algum dos nomes está na lista.
func listed(names []*ast.Ident, set map[string]bool) bool {
	for _, nm := range names {
		if set[nm.Name] {
			return true
		}
	}
	return false
}

func collect(f *ast.File, fs *token.FileSet, rel string, k *keys) {
	var stack []ast.Node
	skip := map[ast.Node]bool{} // os "texto" de case "texto": e as chaves dos mapas de onlyDecls
	only := onlyDecls[rel]
	// marca a declaração de onlyDecls em que está o texto achado
	mark := func() {
		if only == nil {
			return
		}
		for i := len(stack) - 1; i >= 0; i-- {
			switch d := stack[i].(type) {
			case *ast.FuncDecl:
				k.used[rel+":"+d.Name.Name] = true
				return
			case *ast.ValueSpec:
				for _, nm := range d.Names {
					k.used[rel+":"+nm.Name] = true
				}
				return
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		atTop := len(stack) <= 2 // o arquivo e, no máximo, um var/const do topo por cima
		switch x := n.(type) {
		case *ast.ImportSpec, *ast.Field:
			return false
		case *ast.FuncDecl:
			if skipFuncs[x.Name.Name] || (only != nil && !only[x.Name.Name]) {
				return false
			}
		case *ast.GenDecl:
			if only != nil && atTop { // nesses arquivos, só as variáveis escolhidas
				keep := false
				for _, sp := range x.Specs {
					if v, ok := sp.(*ast.ValueSpec); ok && listed(v.Names, only) {
						keep = true
					}
				}
				if !keep {
					return false
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && codeFields[id.Name] {
				return false
			}
			if _, ok := x.Key.(*ast.BasicLit); ok && only != nil {
				skip[x.Key] = true // chave de mapa: código
			}
		case *ast.ValueSpec:
			for _, nm := range x.Names {
				if skipDecls.MatchString(nm.Name) {
					return false
				}
			}
			if only != nil && atTop && !listed(x.Names, only) {
				return false
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
				skip[e] = true
			}
		}
		if skip[n] {
			return false
		}
		stack = append(stack, n)
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || skipTexts.MatchString(s) || !(isPT(s) || only != nil && letters.MatchString(s)) {
			return true
		}
		where := rel + ":" + strconv.Itoa(fs.Position(lit.Pos()).Line)
		parent := stack[len(stack)-2]
		if inner, ok := parent.(*ast.CompositeLit); ok && inner.Type == nil && len(stack) >= 3 {
			if outer, ok := stack[len(stack)-3].(*ast.CompositeLit); ok {
				if at, ok := outer.Type.(*ast.ArrayType); ok {
					if id, ok := at.Elt.(*ast.Ident); ok {
						for i := 0; i < codeElems[id.Name] && i < len(inner.Elts); i++ {
							if inner.Elts[i] == lit {
								return true
							}
						}
					}
				}
			}
		}
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
				mark()
			}
			return true
		}
		if c, ok := parent.(*ast.CallExpr); ok {
			if i, ok := fmtCalls[callName(c)]; ok && i < len(c.Args) && c.Args[i] == lit && verb.MatchString(strings.ReplaceAll(s, "%%", "")) {
				k.formats[s] = where
				mark()
				return true
			}
		}
		k.exact[s] = where
		mark()
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

// TestServerTextsHaveEnglish: todo texto de tela do servidor tem inglês, e nada sobra.
// Com VPMON_I18N_DUMP=<arquivo>, grava o que falta (para traduzir).
func TestServerTextsHaveEnglish(t *testing.T) {
	k := extract(t)
	if len(k.exact)+len(k.formats) < 200 {
		t.Fatalf("poucos textos achados (%d): a leitura do código quebrou?", len(k.exact)+len(k.formats))
	}
	for file, names := range onlyDecls {
		for name := range names {
			if !k.used[file+":"+name] {
				t.Errorf("%s: %s sumiu ou não tem mais texto de tela (ajuste onlyDecls)", file, name)
			}
		}
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
