package engine

import (
	"fmt"
	"time"

	"golang.org/x/text/language"
)

// calendar is a language's month and weekday names and its long date.
type calendar struct {
	months   [12]string
	weekdays [7]string // Sunday first, as time.Weekday
	// long formats day, month (in the case a date needs) and year.
	long func(t time.Time) string
}

func (c calendar) month(t time.Time) string   { return c.months[t.Month()-1] }
func (c calendar) weekday(t time.Time) string { return c.weekdays[t.Weekday()] }

// namesOf is the calendar of the closest language known, else English.
func namesOf(tag language.Tag) calendar {
	base, _ := tag.Base()

	if c, ok := calendars[base.String()]; ok {
		return c
	}

	return calendars["en"]
}

func dayMonthYear(genitive [12]string, sep string) func(time.Time) string {
	return func(t time.Time) string {
		return fmt.Sprintf("%d%s%s %d", t.Day(), sep, genitive[t.Month()-1], t.Year())
	}
}

//nolint:gochecknoglobals,lll // tables
var calendars = map[string]calendar{
	"en": {
		months:   [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
		weekdays: [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
		long: func(t time.Time) string {
			return fmt.Sprintf("%s %d, %d", [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}[t.Month()-1], t.Day(), t.Year())
		},
	},
	"ru": {
		months:   [12]string{"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"},
		weekdays: [7]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"},
		long:     dayMonthYear([12]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}, " "),
	},
	"uk": {
		months:   [12]string{"січень", "лютий", "березень", "квітень", "травень", "червень", "липень", "серпень", "вересень", "жовтень", "листопад", "грудень"},
		weekdays: [7]string{"неділя", "понеділок", "вівторок", "середа", "четвер", "пʼятниця", "субота"},
		long:     dayMonthYear([12]string{"січня", "лютого", "березня", "квітня", "травня", "червня", "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"}, " "),
	},
	"de": {
		months:   [12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
		weekdays: [7]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
		long:     dayMonthYear([12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}, ". "),
	},
	"fr": {
		months:   [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
		weekdays: [7]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
		long:     dayMonthYear([12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}, " "),
	},
	"es": {
		months:   [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
		weekdays: [7]string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
		long: func(t time.Time) string {
			return fmt.Sprintf("%d de %s de %d", t.Day(), [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}[t.Month()-1], t.Year())
		},
	},
	"it": {
		months:   [12]string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
		weekdays: [7]string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"},
		long:     dayMonthYear([12]string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"}, " "),
	},
	"pl": {
		months:   [12]string{"styczeń", "luty", "marzec", "kwiecień", "maj", "czerwiec", "lipiec", "sierpień", "wrzesień", "październik", "listopad", "grudzień"},
		weekdays: [7]string{"niedziela", "poniedziałek", "wtorek", "środa", "czwartek", "piątek", "sobota"},
		long:     dayMonthYear([12]string{"stycznia", "lutego", "marca", "kwietnia", "maja", "czerwca", "lipca", "sierpnia", "września", "października", "listopada", "grudnia"}, " "),
	},
	"pt": {
		months:   [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"},
		weekdays: [7]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"},
		long: func(t time.Time) string {
			return fmt.Sprintf("%d de %s de %d", t.Day(), [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"}[t.Month()-1], t.Year())
		},
	},
	"nl": {
		months:   [12]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"},
		weekdays: [7]string{"zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"},
		long:     dayMonthYear([12]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}, " "),
	},
}
