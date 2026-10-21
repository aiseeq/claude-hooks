package tools

import (
	"strings"
)

// substMark заменяет в слове подстановку $(…), `…` или <(…): тот же символ
// подставляет Claude Code, и его регулярные выражения пропускают его как кавычку
const substMark = '\uE020'

// shWord слово оболочки: raw — как его видит проверка Claude Code (кавычки
// сохранены, подстановки заменены на substMark), text — исходный текст
type shWord struct {
	raw, text string
	// redirect — оператор перенаправления или его цель
	redirect bool
}

// shCommand простая команда; nested — внутри подстановки $(…) или `…`
type shCommand struct {
	words  []shWord
	nested bool
}

// shScript разбор всей команды Bash. Флаги — конструкции, на которых
// Claude Code перестаёт отслеживать значения переменных
type shScript struct {
	cmds []shCommand
	// dollarBrace — ${…} вне одинарных кавычек
	dollarBrace bool
	// group — подоболочка ( … ) или группа { …; }
	group bool
	// function — определение функции
	function bool
	// heredoc — here-document <<
	heredoc bool
	// complexArith — $((…)) с подстановкой внутри
	complexArith bool
}

// shLexer грубый разбор оболочки: кавычки, подстановки, here-doc, разделители
type shLexer struct {
	src    []rune
	out    *shScript
	nested bool

	words     []shWord
	raw, text strings.Builder
	inWord    bool
	redirNext bool
	heredocs  []heredocSpec
}

// heredocSpec ожидающее тело here-doc: строка-ограничитель и <<- (снимать табы)
type heredocSpec struct {
	delim     string
	stripTabs bool
}

// parseShell разбирает команду; команды подстановок попадают в тот же out
func parseShell(src string, nested bool, out *shScript) {
	l := &shLexer{src: []rune(src), out: out, nested: nested}
	l.run()
}

func (l *shLexer) add(raw, text string) {
	l.raw.WriteString(raw)
	l.text.WriteString(text)
	l.inWord = true
}

func (l *shLexer) flushWord() {
	if !l.inWord {
		return
	}
	w := shWord{raw: l.raw.String(), text: l.text.String(), redirect: l.redirNext}
	l.raw.Reset()
	l.text.Reset()
	l.inWord, l.redirNext = false, false
	if len(l.words) == 0 && (w.raw == "{" || w.raw == "}") {
		if w.raw == "{" {
			l.out.group = true
		}
		return
	}
	l.words = append(l.words, w)
}

func (l *shLexer) flushCmd() {
	l.flushWord()
	if len(l.words) > 0 {
		l.out.cmds = append(l.out.cmds, shCommand{words: l.words, nested: l.nested})
	}
	l.words = nil
}

func (l *shLexer) at(i int) rune {
	if i < len(l.src) {
		return l.src[i]
	}
	return 0
}

func (l *shLexer) slice(from, to int) string {
	to = min(to, len(l.src))
	return string(l.src[from:to])
}

func (l *shLexer) run() {
	for i := 0; i < len(l.src); {
		i = l.step(i)
	}
	l.flushCmd()
}

// step разбирает конструкцию, начинающуюся с символа i, и возвращает позицию после неё
func (l *shLexer) step(i int) int {
	c := l.src[i]
	if c == '\\' && l.at(i+1) == '\n' {
		return i + 2
	}
	if next, ok := l.expansion(i); ok {
		return next
	}
	if next, ok := l.operator(i); ok {
		return next
	}
	switch {
	case c == '\'':
		end := indexRune(l.src, '\'', i+1)
		s := l.slice(i, end+1)
		l.add(s, s)
		return end + 1
	case c == '"':
		return l.doubleQuoted(i)
	case c == '#' && !l.inWord:
		return indexRune(l.src, '\n', i)
	case c == ' ' || c == '\t':
		l.flushWord()
		return i + 1
	}
	s := string(c)
	l.add(s, s)
	return i + 1
}

// expansion экранирование и раскрытия, одинаковые вне кавычек и в "…"
func (l *shLexer) expansion(i int) (int, bool) {
	c := l.src[i]
	switch {
	case c == '\\':
		s := l.slice(i, i+2)
		l.add(s, s)
		return i + 2, true
	case c == '$' && l.at(i+1) == '(' && l.at(i+2) == '(':
		return l.arithmetic(i), true
	case c == '$' && l.at(i+1) == '(':
		return l.substitution(i, i+1), true
	case c == '$' && l.at(i+1) == '{':
		return l.dollarBrace(i), true
	case c == '`':
		return l.backtick(i), true
	}
	return i, false
}

// operator разделители команд, скобки и перенаправления вне кавычек
func (l *shLexer) operator(i int) (int, bool) {
	if next, ok := l.separator(i); ok {
		return next, true
	}
	c := l.src[i]
	switch {
	case (c == '<' || c == '>') && l.at(i+1) == '(' && !l.inWord:
		return l.substitution(i, i+1), true
	case c == '&' && l.at(i+1) == '>':
		l.flushWord()
		return l.redirect(i), true
	case c == '(' && l.inWord && l.nextNonSpace(i+1) == ')':
		l.out.function = true
		l.flushCmd()
		return indexRune(l.src, ')', i+1) + 1, true
	case c == '(':
		l.out.group = true
		l.flushCmd()
		return i + 1, true
	case c == ')':
		l.flushCmd()
		return i + 1, true
	case c == '<' || c == '>':
		if l.inWord && !isFD(l.raw.String()) {
			l.flushWord()
		}
		return l.redirect(i), true
	}
	return i, false
}

// separator конец простой команды: перевод строки, ; & | && || |&
func (l *shLexer) separator(i int) (int, bool) {
	c := l.src[i]
	switch {
	case c == '\n':
		l.flushCmd()
		return l.skipHeredocs(i + 1), true
	case c == '&' && l.at(i+1) == '>':
		return i, false // &> — перенаправление
	case c == ';' || c == '&' || c == '|':
		l.flushCmd()
		if n := l.at(i + 1); n == c || (c == '|' && n == '&') {
			return i + 2, true
		}
		return i + 1, true
	}
	return i, false
}

// doubleQuoted разбирает "…" начиная с открывающей кавычки
func (l *shLexer) doubleQuoted(i int) int {
	l.add(`"`, `"`)
	i++
	for i < len(l.src) && l.src[i] != '"' {
		if next, ok := l.expansion(i); ok {
			i = next
			continue
		}
		s := string(l.src[i])
		l.add(s, s)
		i++
	}
	if i < len(l.src) {
		l.add(`"`, `"`)
	}
	return i + 1
}

// arithmetic $((…)); подстановка внутри останавливает отслеживание переменных
func (l *shLexer) arithmetic(i int) int {
	end := matchParen(l.src, i+1)
	s := l.slice(i, end+1)
	if strings.Contains(s[3:], "$(") || strings.Contains(s, "`") {
		l.out.complexArith = true
	}
	l.add(s, s)
	return end + 1
}

// dollarBrace ${…} целиком, без разбора
func (l *shLexer) dollarBrace(i int) int {
	l.out.dollarBrace = true
	end := matchBrace(l.src, i+1)
	s := l.slice(i, end+1)
	l.add(s, s)
	return end + 1
}

// substitution $(…) или <(…): тело разбирается как отдельные команды
func (l *shLexer) substitution(start, open int) int {
	end := matchParen(l.src, open)
	inner := l.slice(open+1, end)
	parseShell(inner, true, l.out)
	l.add(string(substMark), l.slice(start, end+1))
	return end + 1
}

// backtick `…`: тело разбирается как отдельные команды
func (l *shLexer) backtick(i int) int {
	end := i + 1
	for end < len(l.src) && l.src[end] != '`' {
		if l.src[end] == '\\' {
			end++
		}
		end++
	}
	inner := l.slice(i+1, end)
	parseShell(inner, true, l.out)
	l.add(string(substMark), l.slice(i, end+1))
	return end + 1
}

// redirect оператор перенаправления; номер дескриптора уже в текущем слове
func (l *shLexer) redirect(i int) int {
	start := i
	if l.src[i] == '&' {
		i++
	}
	switch {
	case l.slice(i, i+3) == "<<<":
		i += 3
	case l.slice(i, i+3) == "<<-":
		i += 3
	case l.slice(i, i+2) == "<<":
		i += 2
	case strings.Contains("&|>", string(l.at(i+1))) && l.src[i] == '>',
		strings.Contains("&>", string(l.at(i+1))) && l.src[i] == '<':
		i += 2
	default:
		i++
	}
	op := l.slice(start, i)
	l.add(op, op)
	l.redirNext = true
	l.flushWord()
	if op == "<<" || op == "<<-" {
		l.out.heredoc = true
		var d heredocSpec
		d, i = l.heredocDelim(i)
		d.stripTabs = op == "<<-"
		l.heredocs = append(l.heredocs, d)
		return i
	}
	l.redirNext = true
	return i
}

// heredocDelim читает ограничитель here-doc, кавычки снимаются
func (l *shLexer) heredocDelim(i int) (heredocSpec, int) {
	for i < len(l.src) && (l.src[i] == ' ' || l.src[i] == '\t') {
		i++
	}
	var b strings.Builder
	for i < len(l.src) && !strings.ContainsRune(" \t\n;&|<>()", l.src[i]) {
		switch c := l.src[i]; c {
		case '\'', '"':
			end := indexRune(l.src, c, i+1)
			b.WriteString(l.slice(i+1, end))
			i = end + 1
		case '\\':
			b.WriteString(l.slice(i+1, i+2))
			i += 2
		default:
			b.WriteRune(c)
			i++
		}
	}
	return heredocSpec{delim: b.String()}, i
}

// skipHeredocs пропускает тела ожидающих here-doc, начиная со строки i
func (l *shLexer) skipHeredocs(i int) int {
	for _, d := range l.heredocs {
		for i < len(l.src) {
			end := indexRune(l.src, '\n', i)
			line := l.slice(i, end)
			i = end + 1
			if d.stripTabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == d.delim {
				break
			}
		}
	}
	l.heredocs = nil
	return i
}

func (l *shLexer) nextNonSpace(i int) rune {
	for i < len(l.src) && (l.src[i] == ' ' || l.src[i] == '\t') {
		i++
	}
	return l.at(i)
}

// isFD слово — номер дескриптора перед перенаправлением (2>…)
func isFD(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// indexRune позиция r начиная с from; без совпадения — последний символ
func indexRune(src []rune, r rune, from int) int {
	for i := from; i < len(src); i++ {
		if src[i] == r {
			return i
		}
	}
	return len(src)
}

// matchParen позиция скобки, закрывающей src[open] == '('
func matchParen(src []rune, open int) int {
	return matchPair(src, open, '(', ')')
}

// matchBrace позиция скобки, закрывающей src[open] == '{'
func matchBrace(src []rune, open int) int {
	return matchPair(src, open, '{', '}')
}

func matchPair(src []rune, open int, left, right rune) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\\':
			i++
		case c == '\'' && left == '(':
			i = indexRune(src, '\'', i+1)
		case c == '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case c == left:
			depth++
		case c == right:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(src)
}

// unquoteWord снимает кавычки и экранирование, как оболочка при разборе слова
func unquoteWord(s string) string {
	var b strings.Builder
	src := []rune(s)
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '\\':
			if i+1 < len(src) {
				i++
				b.WriteRune(src[i])
			}
		case '\'':
			end := indexRune(src, '\'', i+1)
			b.WriteString(string(src[i+1 : min(end, len(src))]))
			i = end
		case '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' && i+1 < len(src) && strings.ContainsRune("\"\\$`\n", src[i+1]) {
					i++
				}
				b.WriteRune(src[i])
			}
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// varRefs имена переменных $NAME и ${NAME…} в тексте вне одинарных кавычек
func varRefs(s string) []string {
	var names []string
	src := []rune(s)
	inDouble := false
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\\':
			i++
		case c == '\'' && !inDouble:
			i = indexRune(src, '\'', i+1)
		case c == '"':
			inDouble = !inDouble
		case c == '$':
			j := i + 1
			if j < len(src) && src[j] == '{' {
				j++
			}
			k := j
			for k < len(src) && isNameRune(src[k], k == j) {
				k++
			}
			if k > j {
				names = append(names, string(src[j:k]))
			}
		}
	}
	return names
}

func isNameRune(c rune, first bool) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (!first && c >= '0' && c <= '9')
}
