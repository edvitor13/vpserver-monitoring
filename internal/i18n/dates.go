package i18n

import (
	"fmt"
	"time"
)

// Datas em texto (resumos do WhatsApp) no formato de cada idioma, como os
// números: dia/mês em português, mês/dia em inglês (igual à tela).

var (
	ptWeekdays = [...]string{"domingo", "segunda", "terça", "quarta", "quinta", "sexta", "sábado"}
	ptMonths   = [...]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto",
		"setembro", "outubro", "novembro", "dezembro"}
)

// Weekday: "segunda" ou "Monday".
func Weekday(lang string, d time.Weekday) string {
	if lang == "en" {
		return d.String()
	}
	return ptWeekdays[d]
}

// Month: "outubro" ou "October".
func Month(lang string, m time.Month) string {
	if lang == "en" {
		return m.String()
	}
	return ptMonths[m-1]
}

// DayMonth: "05/10" ou "10/05".
func DayMonth(lang string, t time.Time) string {
	if lang == "en" {
		return t.Format("01/02")
	}
	return t.Format("02/01")
}

// MonthYear: "outubro de 2026" ou "October 2026".
func MonthYear(lang string, t time.Time) string {
	if lang == "en" {
		return t.Format("January 2006")
	}
	return fmt.Sprintf("%s de %d", Month(lang, t.Month()), t.Year())
}
