package engine

import (
	"fmt"
	"time"

	"golang.org/x/text/language"
)

// agosto is August in Spanish, Italian and Portuguese alike.
const agosto = "agosto"

// calendar is a language's month and weekday names and its long date.
type calendar struct {
	months [12]string
	// genitive is the case a date puts the month in; zero: months as is.
	genitive [12]string
	weekdays [7]string // Sunday first, as time.Weekday
	// long orders day, month and year as the language writes a date.
	long func(day int, month string, year int) string
}

func (c calendar) month(t time.Time) string   { return c.months[t.Month()-1] }
func (c calendar) weekday(t time.Time) string { return c.weekdays[t.Weekday()] }

// longDate is t as the language writes a date in full.
func (c calendar) longDate(t time.Time) string {
	names := c.months
	if c.genitive != ([12]string{}) {
		names = c.genitive
	}

	return c.long(t.Day(), names[t.Month()-1], t.Year())
}

// namesOf is the calendar of the closest language known, else English.
func namesOf(tag language.Tag) calendar {
	base, _ := tag.Base()

	if c, ok := calendars[base.String()]; ok {
		return c
	}

	return calendars["en"]
}

func dayMonthYear(sep string) func(int, string, int) string {
	return func(day int, month string, year int) string {
		return fmt.Sprintf("%d%s%s %d", day, sep, month, year)
	}
}

func monthDayYear(day int, month string, year int) string {
	return fmt.Sprintf("%s %d, %d", month, day, year)
}

func dayDeMonthDeYear(day int, month string, year int) string {
	return fmt.Sprintf("%d de %s de %d", day, month, year)
}

//nolint:gochecknoglobals,lll // tables
var calendars = map[string]calendar{
	"en": {
		months:   [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
		weekdays: [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
		long:     monthDayYear,
	},
	"ru": {
		months:   [12]string{"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"},
		genitive: [12]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"},
		weekdays: [7]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"},
		long:     dayMonthYear(" "),
	},
	"uk": {
		months:   [12]string{"січень", "лютий", "березень", "квітень", "травень", "червень", "липень", "серпень", "вересень", "жовтень", "листопад", "грудень"},
		genitive: [12]string{"січня", "лютого", "березня", "квітня", "травня", "червня", "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"},
		weekdays: [7]string{"неділя", "понеділок", "вівторок", "середа", "четвер", "пʼятниця", "субота"},
		long:     dayMonthYear(" "),
	},
	"de": {
		months:   [12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
		weekdays: [7]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
		long:     dayMonthYear(". "),
	},
	"fr": {
		months:   [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
		weekdays: [7]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
		long:     dayMonthYear(" "),
	},
	"es": {
		months:   [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", agosto, "septiembre", "octubre", "noviembre", "diciembre"},
		weekdays: [7]string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
		long:     dayDeMonthDeYear,
	},
	"it": {
		months:   [12]string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", agosto, "settembre", "ottobre", "novembre", "dicembre"},
		weekdays: [7]string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"},
		long:     dayMonthYear(" "),
	},
	"pl": {
		months:   [12]string{"styczeń", "luty", "marzec", "kwiecień", "maj", "czerwiec", "lipiec", "sierpień", "wrzesień", "październik", "listopad", "grudzień"},
		genitive: [12]string{"stycznia", "lutego", "marca", "kwietnia", "maja", "czerwca", "lipca", "sierpnia", "września", "października", "listopada", "grudnia"},
		weekdays: [7]string{"niedziela", "poniedziałek", "wtorek", "środa", "czwartek", "piątek", "sobota"},
		long:     dayMonthYear(" "),
	},
	"pt": {
		months:   [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", agosto, "setembro", "outubro", "novembro", "dezembro"},
		weekdays: [7]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"},
		long:     dayDeMonthDeYear,
	},
	"nl": {
		months:   [12]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"},
		weekdays: [7]string{"zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"},
		long:     dayMonthYear(" "),
	},
}
