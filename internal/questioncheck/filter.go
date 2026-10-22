// Package questioncheck — проверка вопросов пользователю в конце хода.
//
// Опыт недели 15–22.10 (~/.claude/improve/runs/2026-10-22/question-checker-dryrun.md):
// каждый третий настоящий вопрос пользователю лишний — ответ лежал в его
// прошлой реплике, в журналах или в зоне самого агента. Stop-хук находит в
// последнем сообщении вопрос дешёвым фильтром и отдаёт его судье-модели;
// вердикт «лишний» возвращает агента к работе: искать самому.
package questioncheck

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// fencedCode — блок кода ```…``` или ~~~…~~~; незакрытый тянется до конца
	fencedCode = regexp.MustCompile("(?s)(```|~~~).*?(```|~~~|\\z)")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	urlPattern = regexp.MustCompile(`https?://\S+`)

	// Цитаты: «…» без вложенных ёлочек, "…" и “…” в пределах строки.
	// Вопрос в кавычках — чужие слова, а не вопрос пользователю
	guillemetQuote = regexp.MustCompile(`«[^«»]*»`)
	asciiQuote     = regexp.MustCompile(`"[^"\n]*"`)
	curlyQuote     = regexp.MustCompile(`“[^”\n]*”`)

	// reportedSpeech — пересказ чужой реплики: «Иван спрашивает: …?»,
	// «Олег пишет, что …?». В ~/work это половина «вопросов» (сухой прогон)
	reportedSpeech = regexp.MustCompile(`(?i)(^|[^а-яё])(спрашива(ет|ют|л|ла|ли)|спросил[аи]?|интересу(ет|ют)ся|интересовал[аи]?сь|уточня(ет|ют)|уточнил[аи]?|пиш(ет|ут)|написал[аи]?|вопрос от|просит|просил[аи]?)([^а-яё]|$)`)

	// requestMarkers — просьба к пользователю без «?»: из ответов недели,
	// на которых стояли поправки пользователя (сухой прогон, 93 ответа)
	requestMarkers = regexp.MustCompile(`(?i)` +
		`(жд(ёт|ет|ут|у)|нужн[оаы]?)\s+(твоего|твоё|твое|вашего|ваше)\s+(«да»|"да"|да([^а-яё]|$)|решени|ответ|подтвержд|выбор|согласи)` +
		`|решаешь\s+ты|тебе\s+решать|на\s+твоё\s+усмотрение` +
		`|скажи,\s+(если|как|какой|какую|что|когда)` +
		`|открыт(ый|ые|ых|ым|ая)\s+вопрос` +
		`|(^|[^а-яё])(нажми|вставь|вставьте|выполни\s+через\s+!|запусти\s+через\s+!)([^а-яё]|$)`)
)

// Question — вопрос или просьба к пользователю и текст перед ним в той же
// строке: по его словам ищутся выдержки из журналов («Удалить?» сам по себе
// слов не даёт)
type Question struct {
	Text string
	Line string
}

// Questions возвращает вопросы и просьбы к пользователю из сообщения агента:
// предложения с «?» вне кода, цитат и пересказа чужих реплик, и предложения с
// маркером просьбы («ждёт твоего «да»», «нужно твоё решение»). Пустой
// результат — вопроса нет, судью звать незачем
func Questions(text string) []Question {
	// Маркеры ищутся до вырезания цитат: «да» в «ждёт твоего «да»» — тоже кавычки
	plain := stripCode(text)
	var found []Question
	seen := map[string]bool{}
	var line string
	add := func(sentence string) {
		sentence = strings.TrimSpace(strings.Trim(strings.TrimSpace(sentence), "*_#-–•"))
		if sentence == "" || seen[sentence] {
			return
		}
		seen[sentence] = true
		found = append(found, Question{Text: sentence, Line: contextBefore(line, sentence)})
	}

	for _, line = range strings.Split(plain, "\n") {
		if isQuoteLine(line) {
			continue
		}
		for _, sentence := range splitSentences(line) {
			if requestMarkers.MatchString(sentence) && !reportedSpeech.MatchString(sentence) {
				add(sentence)
			}
		}
		unquoted := stripQuotes(line)
		for _, sentence := range splitSentences(unquoted) {
			if strings.HasSuffix(strings.TrimSpace(sentence), "?") && !reportedSpeech.MatchString(sentence) {
				add(sentence)
			}
		}
	}
	return found
}

// stripCode убирает блоки и вставки кода и адреса ссылок: «?» в них не вопрос
func stripCode(text string) string {
	text = fencedCode.ReplaceAllString(text, "\n")
	text = inlineCode.ReplaceAllString(text, "X")
	return urlPattern.ReplaceAllString(text, "URL")
}

// stripQuotes убирает цитаты в кавычках, вложенные ёлочки — изнутри наружу
func stripQuotes(line string) string {
	for {
		next := guillemetQuote.ReplaceAllString(line, "Q")
		if next == line {
			break
		}
		line = next
	}
	line = asciiQuote.ReplaceAllString(line, "Q")
	return curlyQuote.ReplaceAllString(line, "Q")
}

// isQuoteLine — строка markdown-цитаты «> …»
func isQuoteLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), ">")
}

// splitSentences режет строку на предложения: конец — «.», «!», «?» или «…»
// перед пробелом или концом строки. Точка внутри слова (CLAUDE.md, 1.5) —
// не конец
func splitSentences(line string) []string {
	runes := []rune(line)
	var sentences []string
	start := 0
	for i := 0; i < len(runes); i++ {
		if !isTerminator(runes[i]) {
			continue
		}
		end := i
		for end+1 < len(runes) && isTerminator(runes[end+1]) {
			end++
		}
		if end+1 == len(runes) || unicode.IsSpace(runes[end+1]) || strings.ContainsRune("*_)»\"", runes[end+1]) {
			sentences = append(sentences, string(runes[start:end+1]))
			start = end + 1
		}
		i = end
	}
	if start < len(runes) {
		sentences = append(sentences, string(runes[start:]))
	}
	return sentences
}

func isTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '…'
}

// Texts — тексты вопросов для журнала
func Texts(questions []Question) []string {
	texts := make([]string, 0, len(questions))
	for _, question := range questions {
		texts = append(texts, question.Text)
	}
	return texts
}

// contextRunes — сколько текста строки до конца вопроса берётся для поиска
// по журналам: вопрос и пара предложений перед ним, не весь абзац
const contextRunes = 300

// contextBefore — до contextRunes знаков строки, кончающихся вопросом. Конец
// вопроса ищется по последним знакам предложения: в нём цитаты заменены, и
// целиком в строке его может не быть
func contextBefore(line, sentence string) string {
	runes := []rune(sentence)
	probe := string(runes[max(0, len(runes)-12):])
	end := strings.LastIndex(line, probe)
	if end < 0 {
		return sentence
	}
	head := []rune(line[:end+len(probe)])
	return strings.TrimSpace(string(head[max(0, len(head)-contextRunes):]))
}
