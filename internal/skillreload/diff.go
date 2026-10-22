package skillreload

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

// diffLimit — больший diff в контекст не кладётся: правку проще перечитать
// целиком, агенту даётся перечень изменённых разделов
const diffLimit = 2048

// lineDiff — unified diff текстов по строкам без строк контекста: правила в
// инструкциях — длинные строки, контекст удвоил бы diff. Место правки
// называет раздел в заголовке каждого куска. sections — разделы правки по
// порядку, без повторов
func lineDiff(oldText, newText, label string) (diff string, sections []string) {
	a := splitLines(oldText)
	b := splitLines(newText)
	oldHeadings := headingIndex(a)
	newHeadings := headingIndex(b)

	var out strings.Builder
	seen := map[string]bool{}
	matcher := difflib.NewMatcher(a, b)
	for _, group := range matcher.GetGroupedOpCodes(0) {
		if out.Len() == 0 {
			fmt.Fprintf(&out, "--- %s (в контексте)\n+++ %s (на диске)\n", label, label)
		}
		first, last := group[0], group[len(group)-1]
		section := sectionAt(b, newHeadings, first.J1)
		if first.J1 == last.J2 {
			// Только удаление: раздел по старому тексту
			section = sectionAt(a, oldHeadings, first.I1)
		}
		header := fmt.Sprintf("@@ -%s +%s @@", unifiedRange(first.I1, last.I2), unifiedRange(first.J1, last.J2))
		if section != "" {
			header += " " + section
			if !seen[section] {
				seen[section] = true
				sections = append(sections, section)
			}
		}
		out.WriteString(header + "\n")
		for _, op := range group {
			if op.Tag == 'r' || op.Tag == 'd' {
				for _, line := range a[op.I1:op.I2] {
					out.WriteString("-" + line + "\n")
				}
			}
			if op.Tag == 'r' || op.Tag == 'i' {
				for _, line := range b[op.J1:op.J2] {
					out.WriteString("+" + line + "\n")
				}
			}
		}
	}
	return out.String(), sections
}

// splitLines делит текст на строки без переводов строки
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// unifiedRange — диапазон строк куска в записи unified diff (с единицы)
func unifiedRange(start, stop int) string {
	length := stop - start
	switch length {
	case 0:
		// Пустой диапазон указывает на строку перед ним
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, length)
}

// headingIndex — номера строк-заголовков вне блоков кода
func headingIndex(lines []string) []int {
	var headings []int
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && isHeading(lines, i) {
			headings = append(headings, i)
		}
	}
	return headings
}

// headingMaxRunes — длиннее строка без # заголовком раздела не считается
const headingMaxRunes = 80

// isHeading — заголовок markdown (# …) или раздел в стиле CLAUDE.md:
// короткая строка после пустой, за которой идёт список
func isHeading(lines []string, i int) bool {
	line := lines[i]
	if strings.HasPrefix(line, "#") {
		return headingText(line) != ""
	}
	if strings.TrimSpace(line) == "" || strings.ContainsAny(line[:1], "-*+>| \t0123456789`") {
		return false
	}
	if utf8.RuneCountInString(line) > headingMaxRunes {
		return false
	}
	if i > 0 && strings.TrimSpace(lines[i-1]) != "" {
		return false
	}
	return i+1 < len(lines) && (strings.HasPrefix(lines[i+1], "- ") || strings.HasPrefix(lines[i+1], "* "))
}

// headingText — текст заголовка без решёток и пробелов
func headingText(line string) string {
	return strings.TrimSpace(strings.TrimLeft(line, "#"))
}

// sectionAt — раздел, в котором стоит строка line: ближайший заголовок не
// ниже её. Строка до первого заголовка — без раздела
func sectionAt(lines []string, headings []int, line int) string {
	section := ""
	for _, h := range headings {
		if h > line {
			break
		}
		section = headingText(lines[h])
	}
	return section
}
