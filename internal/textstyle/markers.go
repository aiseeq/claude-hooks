// Package textstyle ищет в тексте от имени пользователя маркеры, которые
// скилл write-as-user запрещает: тире, markdown, типографские кавычки,
// противопоставление "X, а не Y", числа прописью. Те же маркеры блокируют
// комментарий в Jira (хук jira_style) и проверяют черновик командой
// claude-hooks style
package textstyle

import (
	"regexp"
	"slices"
	"strings"
)

// Marker признак текста, написанного агентом мимо правил write-as-user
type Marker struct {
	Description string
	Fix         string
	// Find возвращает найденный фрагмент или пустую строку
	Find func(text string) string
}

// wordBoundary левая граница слова: \b в RE2 понимает только ASCII
const wordBoundary = `(?:^|[^\p{L}\p{N}_-])`

// Маркеры хука jira_style замерены на реальных комментариях: блокировка
// оправдана только там, где срабатывание почти всегда настоящее нарушение,
// поэтому часть из них пропускает похожий код рядом с текстом
var (
	// EmDash тире между словами или в начале строки: одиночный "—" в шаблоне
	// grep или в проверке '—' in s текстом не является. Escape-форма —
	// в строках JSON раскодируется, по сырой команде она встречается только
	// в коде
	EmDash = Marker{
		Description: "длинное тире",
		Fix:         "обычный дефис с пробелами: ' - '",
		Find:        RegexpFinder(regexp.MustCompile(`(?m)(?:^|\S*[\p{L}\p{N}][)"»]?)[ \t]*—[ \t]*[\p{L}\p{N}]\S*`)),
	}
	Bold = Marker{
		Description: "markdown-жирный",
		Fix:         "обычный текст без звёздочек",
		Find:        RegexpFinder(regexp.MustCompile(`\*\*[^*\n]+\*\*`)),
	}
	Heading = Marker{
		Description: "markdown-заголовок",
		Fix:         "обычный текст без решёток",
		Find:        RegexpFinder(regexp.MustCompile(`(?m)^#{1,6}\s.{0,30}`)),
	}
	// TypographicQuotes пара кавычек с буквой или цифрой внутри: одиночная «
	// или пустая пара в шаблоне проверки ('[—«»]', "«" not in s) текстом не
	// является
	TypographicQuotes = Marker{
		Description: "типографские кавычки",
		Fix:         `прямые кавычки "..." или без кавычек`,
		Find: RegexpFinder(regexp.MustCompile(
			`[«“„][^«»“”„'"\n]{0,40}[\p{L}\p{N}][^«»“”„'"\n]{0,40}[»”“]`)),
	}
	ButNot = Marker{
		Description: "оборот 'X, а не Y'",
		Fix:         "утверждение прямо ('перезаписывает запись'), противопоставление при нужде отдельным предложением",
		Find:        RegexpFinder(regexp.MustCompile(`(?i)\S*,\s+а\s+не\s+\S*`)),
	}
	ThisIsNotBut = Marker{
		Description: "оборот 'это не X, а Y'",
		Fix:         "утверждение прямо ('это реальная просадка'), противопоставление при нужде отдельным предложением",
		Find:        findThisIsNotBut,
	}
	RepeatedPreposition = Marker{
		Description: "оборот 'не из X, а из Y'",
		Fix:         "утверждение прямо ('берёт варианты из ответа провайдера')",
		Find:        findRepeatedPreposition,
	}
	NumberWord = Marker{
		Description: "число прописью",
		Fix:         "число цифрами: '3 расхождения', 'из 2 операций'",
		Find:        findNumberWord,
	}
)

// RegexpFinder превращает выражение в поиск фрагмента
func RegexpFinder(pattern *regexp.Regexp) func(string) string {
	return func(text string) string {
		return pattern.FindString(text)
	}
}

// thisIsNotButPattern "это не X, а": X начинается со слова в первой группе
var thisIsNotButPattern = regexp.MustCompile(`(?i)` + wordBoundary + `(это\s+не\s+(\p{L}+)[^.!?\n,;:]{0,60},\s+а\s+\p{L}+)`)

// verbEndings окончания глаголов: "это не попадает, а ..." связывает два
// утверждения, противопоставления в нём нет. Существительное с таким
// окончанием даёт лишь пропуск, а не ложную блокировку
var verbEndings = []string{"ет", "ит", "ут", "ют", "ат", "ят", "ся", "сь", "ть", "ал", "ял", "ил", "ел", "ла", "ли", "ло"}

// findThisIsNotBut ищет "это не <не глагол> ..., а"
func findThisIsNotBut(text string) string {
	for _, m := range thisIsNotButPattern.FindAllStringSubmatch(text, -1) {
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

// repeatedPrepositionPattern "не <предлог> X, а <слово>": вторая группа
// сравнивается с первой в коде, обратных ссылок в RE2 нет
var repeatedPrepositionPattern = regexp.MustCompile(`(?i)` + wordBoundary + `(не\s+(` + prepositions + `)\s+[^.!?\n,;:]{1,50},\s+а\s+(\p{L}+)\s+\p{L}+)`)

// findRepeatedPreposition ищет "не из X, а из Y" с одним и тем же предлогом
func findRepeatedPreposition(text string) string {
	for _, m := range repeatedPrepositionPattern.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[2], m[3]) {
			return m[1]
		}
	}
	return ""
}

// numberWordPattern количественные числительные 2-20 и десятки во всех
// падежах, за которыми идёт слово
var numberWordPattern = regexp.MustCompile(`(?i)` + wordBoundary + `((?:` +
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

// findNumberWord ищет числительное прописью перед существительным
func findNumberWord(text string) string {
	for _, m := range numberWordPattern.FindAllStringSubmatch(text, -1) {
		if slices.Contains(numberFollowers, strings.ToLower(m[2])) {
			continue
		}
		return m[1]
	}
	return ""
}
