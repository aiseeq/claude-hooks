package validators

import "strings"

// StripNonCode заменяет пробелами содержимое комментариев и строковых литералов,
// сохраняя длину строк и их количество. Позиции найденных совпадений остаются
// верными, а упоминание конструкции в комментарии или в тексте сообщения
// перестает считаться её использованием
func StripNonCode(content string) string {
	var result strings.Builder
	result.Grow(len(content))

	inBlockComment := false
	inRawString := false

	for i, line := range strings.Split(content, "\n") {
		if i > 0 {
			result.WriteByte('\n')
		}
		var stripped string
		stripped, inBlockComment, inRawString = stripLine(line, inBlockComment, inRawString)
		result.WriteString(stripped)
	}

	return result.String()
}

// stripLine обрабатывает одну строку, возвращая её очищенную версию
// и состояние многострочных конструкций для следующей строки
func stripLine(line string, inBlockComment, inRawString bool) (string, bool, bool) {
	out := []byte(line)

	for i := 0; i < len(line); i++ {
		switch {
		case inBlockComment:
			if strings.HasPrefix(line[i:], "*/") {
				out[i], out[i+1] = ' ', ' '
				i++
				inBlockComment = false
			} else {
				out[i] = ' '
			}

		case inRawString:
			if line[i] == '`' {
				inRawString = false
			} else {
				out[i] = ' '
			}

		case strings.HasPrefix(line[i:], "//") || line[i] == '#':
			// Комментарий до конца строки
			for j := i; j < len(line); j++ {
				out[j] = ' '
			}
			return string(out), false, false

		case strings.HasPrefix(line[i:], "/*"):
			out[i], out[i+1] = ' ', ' '
			i++
			inBlockComment = true

		case line[i] == '`':
			inRawString = true

		case line[i] == '"' || line[i] == '\'':
			quote := line[i]
			i++
			for ; i < len(line); i++ {
				if line[i] == '\\' && i+1 < len(line) {
					out[i], out[i+1] = ' ', ' '
					i++
					continue
				}
				if line[i] == quote {
					break
				}
				out[i] = ' '
			}
		}
	}

	return string(out), inBlockComment, inRawString
}
