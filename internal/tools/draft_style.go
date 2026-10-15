package tools

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Черновик текста наружу (сообщение в чат, ответ коллеге) модель выдаёт
// текстом ответа, а не вызовом инструмента: PreToolUse его не видит, и
// проверить его можно только на остановке по last_assistant_message. Отправляет
// пользователь сам, поэтому подсказка продолжает ход, а не блокирует отправку

// fencedBlock блок кода в markdown: язык и тело. RE2 без обратных ссылок,
// поэтому закрывающая ограда — любые три обратные кавычки в начале строки
var fencedBlock = regexp.MustCompile("(?ms)^```([A-Za-z]*)[ \\t]*\\n(.*?)^```")

// draftCommandLine строка команды или кода: такой блок не черновик, даже если
// в нём русский текст (сообщение коммита, комментарий)
var draftCommandLine = regexp.MustCompile(`(?m)^\s*(?:\$|cd |git |ssh |curl |sudo |graft |go |npm |SELECT|func |import |\{|<|#!)`)

// minDraftLength короче этого блок — слово или значение, а не реплика
const minDraftLength = 15

// draftBlocks находит в ответе блоки-черновики: без языка или text, с
// русскими буквами в большинстве и без строк команд. Замер на транскриптах
// с сентября 2026: 515 таких блоков на 72 тысячи ответов
func draftBlocks(message string) []string {
	var blocks []string
	for _, m := range fencedBlock.FindAllStringSubmatch(message, -1) {
		lang, body := m[1], strings.TrimSpace(m[2])
		if lang != "" && lang != "text" {
			continue
		}
		if len([]rune(body)) < minDraftLength || draftCommandLine.MatchString(body) {
			continue
		}
		if !mostlyCyrillic(body) {
			continue
		}
		blocks = append(blocks, body)
	}
	return blocks
}

// mostlyCyrillic: не меньше половины букв текста кириллица
func mostlyCyrillic(text string) bool {
	letters, cyrillic := 0, 0
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.Is(unicode.Cyrillic, r) {
			cyrillic++
		}
	}
	return letters > 0 && cyrillic*2 >= letters
}

// draftOnlyMarkers правки, которые пользователь чаще всего вносит в черновики
// (скилл write-as-user, corpus-stats.md). Только узкие формы: на 515 блоках
// дали 37 срабатываний, все в репликах, которые он правил. Длину не проверяет:
// длиннее 300 символов треть черновиков, и оправдана ли длина, решает модель
var draftOnlyMarkers = []jiraStyleMarker{
	{
		description: "вопрос-проверка в конце",
		fix:         "снять: вопрос остаётся, только если от ответа зависит наше действие",
		find: regexpFinder(regexp.MustCompile(`(?i)(?:получилось|глянешь|сходится|ты пробовал[аи]?|` +
			`тебе для чего[^?\n]*|для чего тебе[^?\n]*)\?\s*$`)),
	},
	{
		description: "отчёт о своём действии в начале",
		fix:         "сразу суть, без 'Нашёл.', 'Разобрался.', 'Посмотрел.'",
		find: regexpFinder(regexp.MustCompile(`(?i)^\s*(?:@\S+(?:\s\S+)?\s+)?` +
			`(?:нашёл|нашел|разобрался|посмотрел|понял|проверил|глянул)[.!]`)),
	},
	{
		description: "срок или обещание",
		fix:         "без дат и будущих шагов, если пользователь их не разрешал; что делается сейчас - настоящим временем",
		find: regexpFinder(regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:к \d{1,2}\.\d{2}(?:[^\d.]|$)|` +
			`(?:скину|сделаю|досчитаю|выложу|выкачу|гляну)\s+(?:сегодня|завтра)|` +
			`(?:сегодня|завтра)\s+(?:скину|сделаю|досчитаю|выложу|выкачу|гляну))`)),
	},
}

// ReviewDrafts проверяет черновики в последнем ответе модели и возвращает
// подсказку для продолжения хода; пустая строка — замечаний нет
func ReviewDrafts(message string) string {
	var findings []string
	for _, block := range draftBlocks(message) {
		for _, marker := range draftMarkers() {
			fragment := marker.find(block)
			if fragment == "" {
				continue
			}
			findings = append(findings, fmt.Sprintf("- %s: %q, нужно: %s",
				marker.description, strings.TrimSpace(fragment), marker.fix))
		}
	}
	if len(findings) == 0 {
		return ""
	}
	return "Черновик текста от имени пользователя расходится со скиллом write-as-user:\n" +
		strings.Join(findings, "\n") +
		"\nПерепиши черновик по write-as-user и выдай его заново целиком. Если блок не текст наружу" +
		" или пункт тут оправдан, ничего не переписывай и закончи одной строкой."
}

// draftMarkers маркеры формы из jira_style, кроме правил только для Jira,
// и правки черновиков. Один список формы на оба места: правило добавляется
// в jiraStyleMarkers и сразу действует и в Jira, и в чате
func draftMarkers() []jiraStyleMarker {
	markers := make([]jiraStyleMarker, 0, len(jiraStyleMarkers)+len(draftOnlyMarkers))
	for _, marker := range jiraStyleMarkers {
		if !marker.jiraOnly {
			markers = append(markers, marker)
		}
	}
	return append(markers, draftOnlyMarkers...)
}
