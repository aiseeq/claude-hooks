package tools

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// jiraStyleMarker признак текста, написанного агентом мимо правил
// write-as-user. Каждый маркер замерен на реальных комментариях: блокировка
// оправдана только там, где срабатывание почти всегда настоящее нарушение.
// Шумную проверку предупреждением не сделать: additionalContext PreToolUse
// модель видит рядом с результатом, то есть уже после отправки комментария.
// Поэтому десятичная запятая не проверяется: в комментариях она почти
// всегда цитирует формат сумм в интерфейсе ("1.000,00")
type jiraStyleMarker struct {
	description string
	fix         string
	// startsOnly: искать только в началах текстов (строки JSON и файлы тела
	// запроса), а не во всей команде: там, где важно, с чего начат комментарий
	startsOnly bool
	// find возвращает найденный фрагмент или пустую строку
	find func(text string) string
}

// wordBoundary левая граница слова: \b в RE2 понимает только ASCII
const wordBoundary = `(?:^|[^\p{L}\p{N}_-])`

var jiraStyleMarkers = []jiraStyleMarker{
	{
		description: "длинное тире",
		fix:         "обычный дефис с пробелами: ' - '",
		find:        regexpFinder(regexp.MustCompile(`\S*\s*—\s*\S*`)),
	},
	{
		description: "длинное тире в escape-форме",
		fix:         "обычный дефис с пробелами: ' - '",
		find:        regexpFinder(regexp.MustCompile(`\\u2014`)),
	},
	{
		description: "markdown-жирный",
		fix:         "обычный текст без звёздочек",
		find:        regexpFinder(regexp.MustCompile(`\*\*[^*\n]+\*\*`)),
	},
	{
		description: "markdown-заголовок",
		fix:         "обычный текст без решёток",
		find:        regexpFinder(regexp.MustCompile(`(?m)^#{1,6}\s.{0,30}`)),
	},
	{
		description: "типографские кавычки",
		fix:         `прямые кавычки "..." или без кавычек`,
		find:        regexpFinder(regexp.MustCompile(`[«»“”„][^«»“”„\n]{0,30}[«»“”„]?`)),
	},
	{
		description: "оборот 'X, а не Y'",
		fix:         "утверждение прямо ('перезаписывает запись'), противопоставление при нужде отдельным предложением",
		find:        regexpFinder(regexp.MustCompile(`(?i)\S*,\s+а\s+не\s+\S*`)),
	},
	{
		description: "оборот 'это не X, а Y'",
		fix:         "утверждение прямо ('это реальная просадка'), противопоставление при нужде отдельным предложением",
		find:        findThisIsNotButMarker,
	},
	{
		description: "оборот 'не из X, а из Y'",
		fix:         "утверждение прямо ('берёт варианты из ответа провайдера')",
		find:        findRepeatedPrepositionMarker,
	},
	{
		description: "приветствие",
		fix:         "в Jira сразу суть, без приветствия",
		startsOnly:  true,
		find:        regexpFinder(greetingPattern),
	},
	{
		description: "число прописью",
		fix:         "число цифрами: '3 расхождения', 'из 2 операций'",
		find:        findNumberWordMarker,
	},
}

// regexpFinder превращает выражение в поиск фрагмента
func regexpFinder(pattern *regexp.Regexp) func(string) string {
	return func(text string) string {
		return pattern.FindString(text)
	}
}

// greetingPattern приветствие в начале комментария, в том числе после
// упоминания: "@Имя привет", "[~accountid:x] Добрый день"
var greetingPattern = regexp.MustCompile(`(?i)^\s*(?:\[~[^\]]*\]\s*)?(?:@\p{L}+(?:\s+\p{L}+)?\s*,?\s*)?` +
	`(?:привет\p{L}*|здравствуй\p{L}*|добр(?:ый|ое|ого)\s+(?:день|утро|вечер|дня|утра|вечера))(?:[^\p{L}]|$)`)

// thisIsNotBut "это не X, а": X начинается со слова в первой группе
var thisIsNotBut = regexp.MustCompile(`(?i)` + wordBoundary + `(это\s+не\s+(\p{L}+)[^.!?\n,;:]{0,60},\s+а\s+\p{L}+)`)

// verbEndings окончания глаголов: "это не попадает, а ..." связывает два
// утверждения, противопоставления в нём нет. Существительное с таким
// окончанием даёт лишь пропуск, а не ложную блокировку
var verbEndings = []string{"ет", "ит", "ут", "ют", "ат", "ят", "ся", "сь", "ть", "ал", "ял", "ил", "ел", "ла", "ли", "ло"}

// findThisIsNotButMarker ищет "это не <не глагол> ..., а"
func findThisIsNotButMarker(text string) string {
	for _, m := range thisIsNotBut.FindAllStringSubmatch(text, -1) {
		word := strings.ToLower(m[2])
		if slices.ContainsFunc(verbEndings, func(ending string) bool { return strings.HasSuffix(word, ending) }) {
			continue
		}
		return m[1]
	}
	return ""
}

// prepositions предлоги, повтор которых после "а" делает конструкцию
// противопоставлением: "не из списка, а из ответа"
const prepositions = `из|по|в|во|на|с|со|к|ко|за|от|для|у|о|об|при|про|через`

// repeatedPreposition "не <предлог> X, а <слово>": вторая группа сравнивается
// с первой в коде, обратных ссылок в RE2 нет
var repeatedPreposition = regexp.MustCompile(`(?i)` + wordBoundary + `(не\s+(` + prepositions + `)\s+[^.!?\n,;:]{1,50},\s+а\s+(\p{L}+)\s+\p{L}+)`)

// findRepeatedPrepositionMarker ищет "не из X, а из Y" с одним и тем же предлогом
func findRepeatedPrepositionMarker(text string) string {
	for _, m := range repeatedPreposition.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[2], m[3]) {
			return m[1]
		}
	}
	return ""
}

// numberWord количественные числительные 2-20 и десятки во всех падежах, за
// которыми идёт слово
var numberWord = regexp.MustCompile(`(?i)` + wordBoundary + `((?:` +
	`дв(?:а|е|ух|ум|умя)|тр(?:и|ёх|ех|ём|ем|емя)|четыр(?:е|ёх|ех|ём|ем|ьмя)|` +
	`(?:пят|шест|девят|десят|одиннадцат|двенадцат|тринадцат|четырнадцат|пятнадцат|шестнадцат|семнадцат|восемнадцат|девятнадцат|двадцат|тридцат)(?:ь|и|ью)|` +
	`сем(?:ь|и|ью)|восем(?:ь|ью)|восьми|сорока?|(?:пять|шесть|семь|восемь)десят|(?:пяти|шести|семи|восьми)десяти|девяност[оа]` +
	`)\s+(\p{L}+))`)

// numberFollowers служебные слова после числительного: в "три из пяти" и
// "двух и трёх" числительное не стоит перед существительным, такие обороты
// проверка пропускает
var numberFollowers = []string{
	"и", "а", "но", "да", "или", "же", "ли", "не", "что", "как", "это",
	"в", "во", "из", "по", "за", "с", "со", "на", "до", "от", "к", "ко", "у", "о", "об", "при", "про", "через", "для",
}

// findNumberWordMarker ищет числительное прописью перед существительным
func findNumberWordMarker(text string) string {
	for _, m := range numberWord.FindAllStringSubmatch(text, -1) {
		if slices.Contains(numberFollowers, strings.ToLower(m[2])) {
			continue
		}
		return m[1]
	}
	return ""
}

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
