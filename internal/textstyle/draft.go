package textstyle

import (
	"regexp"
	"strings"
)

// Маркеры только для черновика: команду style запускает сам агент на голом
// тексте, кода рядом нет, поэтому проверки здесь строже, чем у хука
var (
	EnDash = Marker{
		Description: "короткое тире",
		Fix:         "обычный дефис с пробелами: ' - '",
		Find:        RegexpFinder(regexp.MustCompile(`\S*[ \t]*–[ \t]*\S*`)),
	}
	// DecimalComma в черновике десятичная запятая почти всегда ошибка: хук
	// Jira её не ловит, там она цитирует формат сумм в интерфейсе ("1.000,00")
	DecimalComma = Marker{
		Description: "десятичная запятая",
		Fix:         "десятичная точка: '2.5'",
		Find:        RegexpFinder(regexp.MustCompile(`\p{Nd}+,\p{Nd}+`)),
	}
	MarkdownTable = Marker{
		Description: "markdown-таблица",
		Fix:         "строки '- ' или текст без таблицы",
		Find:        RegexpFinder(regexp.MustCompile(`^\s*\|.*\|\s*$`)),
	}
	MarkdownLink = Marker{
		Description: "markdown-ссылка",
		Fix:         "голая ссылка или ключ тикета",
		Find:        RegexpFinder(regexp.MustCompile(`\[[^\]\n]+\]\([^)\s]+\)`)),
	}
	// Emoji иконки: эмоция в его текстах - =), ;), %), =(
	Emoji = Marker{
		Description: "эмодзи",
		Fix:         "без иконок, эмоция - =) ;) %) =(",
		Find:        RegexpFinder(regexp.MustCompile(`[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B50}\x{2B55}\x{2705}\x{274C}]`)),
	}
	Preamble = Marker{
		Description: "предисловие или мета-фраза",
		Fix:         "сразу суть, без вступления и прощания",
		Find:        RegexpFinder(regexp.MustCompile(`(?i)` + wordBoundary + `(отличный вопрос|хороший вопрос|давай разбер[её]мся|скажу честно|надеюсь,? (?:это )?помож\p{L}*|дай знать,? если)`)),
	}
	// ReportOpening первое слово - отчёт о своём действии вместо сути
	ReportOpening = Marker{
		Description: "отчёт о своём действии в начале",
		Fix:         "сразу суть: что найдено или что делать",
		Find:        RegexpFinder(regexp.MustCompile(`^\s*(?:Нашёл|Нашел|Разобрался|Посмотрел|Понял)\.`)),
	}
)

// DraftMarkers маркеры черновика: все маркеры хука Jira, кроме приветствия
// (в личке "Привет." первой репликой дня - правило), и маркеры черновика
var DraftMarkers = []Marker{
	EmDash, EnDash, Bold, Heading, MarkdownTable, MarkdownLink, TypographicQuotes,
	ButNot, ThisIsNotBut, RepeatedPreposition, NumberWord, DecimalComma,
	Emoji, Preamble, ReportOpening,
}

// Finding нарушение в строке черновика
type Finding struct {
	Line     int
	Marker   Marker
	Fragment string
}

// CheckDraft проверяет черновик построчно: номер строки ведёт к месту правки,
// а оборот через перенос строки в коротком сообщении почти не встречается
func CheckDraft(text string) []Finding {
	var findings []Finding
	for i, line := range strings.Split(text, "\n") {
		for _, marker := range DraftMarkers {
			if fragment := marker.Find(line); fragment != "" {
				findings = append(findings, Finding{Line: i + 1, Marker: marker, Fragment: strings.TrimSpace(fragment)})
			}
		}
	}
	return findings
}
