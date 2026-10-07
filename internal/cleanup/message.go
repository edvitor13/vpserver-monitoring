package cleanup

import (
	"fmt"
	"strconv"
	"strings"
)

var itemNames = map[string]string{
	"build_cache": "Cache de build",
	"dangling":    "Imagens sem nome",
	"logs":        "Logs",
}

// Bytes formata em base 1024, como o resto do painel ("1,5 GB").
func Bytes(v uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, i := float64(v), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	d := 0
	if i > 0 && f < 100 {
		d = 1
	}
	return strings.Replace(strconv.FormatFloat(f, 'f', d, 64), ".", ",", 1) + " " + units[i]
}

func pctText(p float64) string {
	return strings.Replace(strconv.FormatFloat(p, 'f', 0, 64), ".", ",", 1) + "%"
}

func title(r Run) string {
	if r.By == "" {
		return "Limpeza automática do disco"
	}
	return "Limpeza do disco por " + r.By
}

// Message é o aviso pelo WhatsApp (tipo "cleanup").
func Message(server string, r Run) string {
	var b strings.Builder
	head := "🧹 *Limpeza do disco · " + server + "*\n"
	if r.By == "" {
		head = "🧹 *Limpeza automática · " + server + "*\nO disco passou do limite configurado (" + pctText(r.Before) + ").\n"
	} else {
		head += "Feita por " + r.By + " pela tela.\n"
	}
	b.WriteString(head)
	if r.Note != "" {
		b.WriteString(r.Note + "\n")
	}
	for _, st := range r.Steps {
		name := itemNames[st.Item]
		switch {
		case st.Error != "":
			fmt.Fprintf(&b, "• %s: falhou (%s)\n", name, st.Error)
		case st.Item == "logs":
			fmt.Fprintf(&b, "• %s (%s): %s\n", name, strings.Join(st.Names, ", "), Bytes(st.Freed))
		case st.Item == "dangling":
			fmt.Fprintf(&b, "• %s (%d): %s\n", name, st.Removed, Bytes(st.Freed))
		default:
			fmt.Fprintf(&b, "• %s: %s\n", name, Bytes(st.Freed))
		}
	}
	fmt.Fprintf(&b, "Liberado: *%s*. Disco: %s → %s.", Bytes(r.Freed), pctText(r.Before), pctText(r.After))
	return b.String()
}
