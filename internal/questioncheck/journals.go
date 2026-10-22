package questioncheck

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Бюджет выдержек в промпте судьи: сухой прогон оценивал вызов с журналами в
// 3–3.5 тыс. токенов
const (
	replyExcerpts     = 6
	replyExcerptRunes = 700
	decisionExcerpts  = 3
	decisionRunes     = 1200
	maxKeywords       = 10
	rareEntries       = 3
)

// Journals — журналы, где агент обязан искать ответ до вопроса пользователю
// (~/.claude/CLAUDE.md, правило про вопросы): реплики пользователя во всех
// переписках и журнал его решений
type Journals struct {
	RepliesPath   string
	DecisionsPath string
}

// DefaultJournals — пути журналов в ~/.claude
func DefaultJournals(claudeDir string) Journals {
	return Journals{
		RepliesPath:   claudeDir + "/user-replies.md",
		DecisionsPath: claudeDir + "/decisions.md",
	}
}

// Excerpts — найденное в журналах по словам вопроса
type Excerpts struct {
	Replies   []string
	Decisions []string
}

var (
	wordPattern = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}_-]*`)
	// replyHeader — заголовок блока журнала реплик: «### 2026-10-22T14:33:15.508Z (…) · ~ · claude …»
	replyHeader = regexp.MustCompile(`^### (\d{4}-\d{2}-\d{2}T[0-9:.]+Z)`)
	// decisionDate — первая дата «(16.10 …» в записи журнала решений
	decisionDate = regexp.MustCompile(`\((\d{2})\.(\d{2})[ ,)]`)
)

// stopWords — частые слова вопросов, которые ничего не находят
var stopWords = map[string]bool{
	"сейчас": true, "делать": true, "делаем": true, "давай": true, "можно": true, "нужно": true,
	"нужен": true, "нужна": true, "нужны": true, "твоего": true, "твоё": true, "твое": true,
	"вашего": true, "решение": true, "ответа": true, "ждёт": true, "ждет": true, "который": true,
	"которая": true, "которые": true, "чтобы": true, "потом": true, "после": true, "сначала": true,
	"тогда": true, "только": true, "ещё": true, "если": true, "когда": true, "какой": true,
	"какую": true, "какие": true, "этого": true, "этому": true, "этот": true, "этой": true,
	"будет": true, "будем": true, "сделать": true, "оставить": true, "правильно": true,
	"вопрос": true, "вопросы": true, "открытый": true, "открытых": true, "скажи": true,
}

// Find ищет в журналах записи по словам вопросов. Записи журнала реплик не
// позже before (нулевое — без ограничения): прогон по прошлым ответам не
// должен видеть ответ пользователя на сам вопрос
func (j Journals) Find(questions []Question, before time.Time) (Excerpts, error) {
	keys := keywords(questions)
	if len(keys) == 0 {
		return Excerpts{}, nil
	}
	var excerpts Excerpts

	replies, err := os.ReadFile(j.RepliesPath)
	if err != nil {
		return Excerpts{}, fmt.Errorf("read user replies: %w", err)
	}
	blocks := replyBlocks(string(replies), before)
	excerpts.Replies = best(blocks, keys, replyExcerpts, replyExcerptRunes)

	decisions, err := os.ReadFile(j.DecisionsPath)
	if err != nil {
		return Excerpts{}, fmt.Errorf("read decisions: %w", err)
	}
	entries, err := decisionEntries(string(decisions), before)
	if err != nil {
		return Excerpts{}, err
	}
	excerpts.Decisions = best(entries, keys, decisionExcerpts, decisionRunes)
	return excerpts, nil
}

// keywords — основы значимых слов строк с вопросами: без окончания, чтобы
// «артефакт» находил «артефакта». Длинные и редкие слова полезнее, вес — по
// редкости в самих журналах (best)
func keywords(questions []Question) []string {
	seen := map[string]bool{}
	var keys []string
	for _, question := range questions {
		for _, word := range wordPattern.FindAllString(strings.ToLower(question.Line), -1) {
			if utf8.RuneCountInString(word) < 4 || stopWords[word] {
				continue
			}
			key := stem(word)
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	// Длинные слова вперёд: короткие чаще общие
	sort.SliceStable(keys, func(a, b int) bool {
		return utf8.RuneCountInString(keys[a]) > utf8.RuneCountInString(keys[b])
	})
	if len(keys) > maxKeywords {
		keys = keys[:maxKeywords]
	}
	return keys
}

// stem отрезает от русского слова до двух последних букв (окончание),
// оставляя не меньше четырёх. Латиницу и идентификаторы (atlas, ABC-12) не
// трогает: это имена
func stem(word string) string {
	runes := []rune(word)
	for _, r := range runes {
		if !unicode.Is(unicode.Cyrillic, r) && r != '-' {
			return word
		}
	}
	keep := len(runes) - 2
	if keep < 4 {
		keep = len(runes)
	}
	return string(runes[:keep])
}

// replyBlocks режет журнал реплик на блоки «### время …» не позже before
func replyBlocks(text string, before time.Time) []string {
	var blocks []string
	var current strings.Builder
	keep := false
	flush := func() {
		if keep && current.Len() > 0 {
			blocks = append(blocks, strings.TrimSpace(current.String()))
		}
		current.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if m := replyHeader.FindStringSubmatch(line); m != nil {
			flush()
			keep = true
			if !before.IsZero() {
				at, err := time.Parse(time.RFC3339Nano, m[1])
				keep = err == nil && at.Before(before)
			}
		}
		if keep {
			current.WriteString(line)
			current.WriteByte('\n')
		}
	}
	flush()
	return blocks
}

// decisionEntries режет журнал решений на записи «## slug». Шапка журнала —
// не запись. С before отбрасываются записи, решение в которых датировано
// позже (дата «(16.10 …» без года — год before)
func decisionEntries(text string, before time.Time) ([]string, error) {
	var entries []string
	parts := strings.Split(text, "\n## ")
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "Записи") || strings.HasPrefix(part, "Как ") || strings.HasPrefix(part, "Формат") {
			continue
		}
		if !before.IsZero() {
			m := decisionDate.FindStringSubmatch(part)
			if m == nil {
				continue
			}
			decided, err := time.ParseInLocation("02.01.2006", fmt.Sprintf("%s.%s.%d", m[1], m[2], before.Year()), before.Location())
			if err != nil {
				return nil, fmt.Errorf("decision date %s.%s: %w", m[1], m[2], err)
			}
			if decided.After(before) {
				continue
			}
		}
		entries = append(entries, "## "+part)
	}
	return entries, nil
}

// best выбирает записи по BM25 с бинарной частотой: вес основы —
// log(1 + (N − n + 0.5) / (n + 0.5)), где n — число записей с ней, так редкое
// слово весит больше частого; длинная запись (вставленный лог) не выигрывает
// числом слов. Нужно совпадение хотя бы двух основ, если основ больше одной,
// или одной редкой (id артефакта, номер тикета — не больше rareEntries записей);
// при равном весе новее та, что ниже в журнале
func best(entries, keys []string, limit, runes int) []string {
	if len(entries) == 0 {
		return nil
	}
	const k1, b = 1.2, 0.75
	lower := make([]string, len(entries))
	freq := make(map[string]int, len(keys))
	total := 0
	for i, entry := range entries {
		lower[i] = strings.ToLower(entry)
		total += utf8.RuneCountInString(entry)
		for _, key := range keys {
			if strings.Contains(lower[i], key) {
				freq[key]++
			}
		}
	}
	avg := float64(total) / float64(len(entries))
	need := 2
	if len(keys) == 1 {
		need = 1
	}
	type scored struct {
		index int
		score float64
	}
	var hits []scored
	for i := range entries {
		norm := (k1 + 1) / (1 + k1*(1-b+b*float64(utf8.RuneCountInString(entries[i]))/avg))
		matched := 0
		rare := false
		score := 0.0
		for _, key := range keys {
			n := float64(freq[key])
			if n > 0 && strings.Contains(lower[i], key) {
				matched++
				rare = rare || freq[key] <= rareEntries
				score += math.Log(1+(float64(len(entries))-n+0.5)/(n+0.5)) * norm
			}
		}
		if matched >= need || rare {
			hits = append(hits, scored{index: i, score: score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].index > hits[b].index
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	result := make([]string, 0, len(hits))
	for _, hit := range hits {
		result = append(result, truncateRunes(entries[hit.index], runes))
	}
	return result
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + " …[обрезано]"
}
