package tools

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/aiseeq/claude-hooks/internal/textstyle"
)

// jiraStyleMarker маркер write-as-user с областью поиска в команде.
// Шумную проверку предупреждением не сделать: additionalContext PreToolUse
// модель видит рядом с результатом, то есть уже после отправки комментария.
// Поэтому десятичная запятая не проверяется: в комментариях она почти
// всегда цитирует формат сумм в интерфейсе ("1.000,00")
type jiraStyleMarker struct {
	textstyle.Marker
	// textOnly: искать только в тексте комментария (строки JSON и файлы тела
	// запроса). Остальные маркеры ищутся и по всей команде: тело часто
	// собирает генератор в той же команде из строк Python или shell, которые
	// строками JSON не разобрать, поэтому их выражения не задевают код рядом
	textOnly bool
}

var jiraStyleMarkers = []jiraStyleMarker{
	{Marker: textstyle.EmDash},
	{Marker: textstyle.Bold},
	// В строках команды "# " начинает комментарий shell или Python
	{Marker: textstyle.Heading, textOnly: true},
	{Marker: textstyle.TypographicQuotes},
	{Marker: textstyle.ButNot},
	{Marker: textstyle.ThisIsNotBut},
	{Marker: textstyle.RepeatedPreposition},
	{Marker: textstyle.Marker{
		Description: "приветствие",
		Fix:         "в Jira сразу суть, без приветствия",
		Find:        textstyle.RegexpFinder(greetingPattern),
	}, textOnly: true},
	{Marker: textstyle.NumberWord},
}

// greetingPattern приветствие в начале комментария, в том числе после
// упоминания: "@Имя привет", "[~accountid:x] Добрый день"
var greetingPattern = regexp.MustCompile(`(?i)^\s*(?:\[~[^\]]*\]\s*)?(?:@\p{L}+(?:\s+\p{L}+)?\s*,?\s*)?` +
	`(?:привет\p{L}*|здравствуй\p{L}*|добр(?:ый|ое|ого)\s+(?:день|утро|вечер|дня|утра|вечера))(?:[^\p{L}]|$)`)

// jsonStringLiteral строка JSON в тексте команды или файла тела запроса
var jsonStringLiteral = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)

// jsonStrings раскодирует строки JSON в тексте: тело, записанное json.dumps
// без ensure_ascii=False, хранит кириллицу и кавычки как \uXXXX, и искать
// надо в раскодированном тексте. Строки ADF дают и начала абзацев и узлов,
// по которым видно, с чего начат комментарий
func jsonStrings(text string) []string {
	var decoded []string
	for _, literal := range jsonStringLiteral.FindAllString(text, -1) {
		var value string
		if json.Unmarshal([]byte(literal), &value) != nil {
			// Не JSON-строка, а, например, строка shell: её текст уже
			// проверен в исходной команде как есть
			continue
		}
		if strings.IndexFunc(value, unicode.IsLetter) >= 0 {
			decoded = append(decoded, value)
		}
	}
	return decoded
}
