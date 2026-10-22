package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// WriteGuardTool отклоняет запись файла через here-doc, тело которого
// Claude Code примет за команду rm по возможно пустой переменной и остановит
// вызов запросом пользователю («Dangerous rm operation detected»), который не
// снимается и в bypassPermissions. Сама запись через Bash (>, >>, tee,
// here-doc, sed -i) в bypassPermissions запроса не даёт, в какую бы папку она
// ни шла: ~, $VAR, вне рабочей папки, .git и .claude.
//
// Правило повторяет Claude Code 2.1.305 (проверено прогоном команд через
// claude -p, таблица в тесте). Проверка rm делит команду на верхнеуровневые
// команды; составную ({ … }, ( … ), if, for, while, until, case, функцию)
// она берёт целиком, вместе с телами here-doc внутри неё, и режет текст на
// куски по переводам строк, ; | & && без учёта here-doc и кавычек. Кусок вида
// rm -rf $X/* из такого тела спросит пользователя так же, как настоящая
// команда. Команду длиннее 10000 символов Claude Code не делит вовсе, и тогда
// так же проверяются тела всех её here-doc.
type WriteGuardTool struct {
	*BaseTool
	rm *RmGuardTool
}

// NewWriteGuardTool создает проверку тел here-doc
func NewWriteGuardTool(config core.ToolConfig, logger core.Logger) (*WriteGuardTool, error) {
	rm, err := NewRmGuardTool(core.ToolConfig{Enabled: true}, logger)
	if err != nil {
		return nil, fmt.Errorf("rm check for write_guard: %w", err)
	}
	return &WriteGuardTool{
		BaseTool: NewBaseTool("write_guard", config.Enabled, []string{"Bash"}, logger),
		rm:       rm,
	}, nil
}

const writeGuardHeader = "Тело here-doc Claude Code проверит как команды и спросит пользователя, вызов будет ждать ответа:"

// claudeSplitLimit длина команды (в единицах UTF-16, как length в JS), сверх
// которой Claude Code не делит её на команды и проверяет текст целиком
const claudeSplitLimit = 10000

var (
	backtickSpan = regexp.MustCompile("`[^`]*`")
	flatSubst    = regexp.MustCompile(`\$\([^()]*\)`)
	flatParens   = regexp.MustCompile(`\([^()]*\)`)
	pieceSplit   = regexp.MustCompile(`[;|\n\r]|&&`)
	redirToken   = regexp.MustCompile(`^[0-9&]*[<>]`)
	bareRedirOp  = regexp.MustCompile(`^(?:[0-9]+|&)?(?:>>?[|&]?|<<?<?|<>)$`)
)

// ValidateTool проверяет тела here-doc в команде Bash
func (t *WriteGuardTool) ValidateTool(_ context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	cmd := input.Command
	if !t.IsEnabled() || input.ToolName != "Bash" || !strings.Contains(cmd, "<<") || !strings.Contains(cmd, "rm") || !strings.Contains(cmd, "$") {
		return &core.ValidationResult{IsValid: true}, nil
	}
	script := &shScript{}
	parseShell(cmd, false, script)
	whole := core.UTF16Len(cmd) > claudeSplitLimit
	vars := t.rm.trackVars(script, input.CWD)
	var hits []string
	for _, b := range script.bodies {
		if b.compound || whole {
			hits = append(hits, t.bodyHits(b.text, vars)...)
		}
	}
	if len(hits) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}
	hits = deduplicateStrings(hits)
	where := "here-doc внутри { … }, ( … ), if, for, while, case или функции Claude Code читает как команды"
	if whole {
		where = "команду длиннее 10000 символов Claude Code не делит на команды и читает тела всех here-doc как команды"
	}
	reason := fmt.Sprintf("строка %s в теле here-doc похожа на rm по переменной без защиты: %s. Создай файл инструментом Write, правь Edit, а в Bash оставь только команду, которая его использует",
		strings.Join(hits, ", "), where)
	t.Logger().Debug("heredoc body read as rm denied", "hits", len(hits))
	violation := core.NewViolation("write_guard", writeGuardHeader, reason, core.LevelCritical, 0, 0)
	return core.NewValidationResult(false, []core.Violation{violation}, []string{reason}), nil
}

// bodyHits куски тела, которые Claude Code примет за rm по возможно пустой
// переменной; тело режется так же, как его режет Claude Code
func (t *WriteGuardTool) bodyHits(body string, vars *rmVars) []string {
	mark := string(substMark)
	s := strings.ReplaceAll(body, "\\\n", " ")
	s = backtickSpan.ReplaceAllString(s, mark)
	for pass := 0; pass < 16; pass++ {
		prev := s
		s = flatParens.ReplaceAllString(flatSubst.ReplaceAllString(s, mark), mark)
		if s == prev {
			break
		}
	}
	var hits []string
	for _, piece := range pieceSplit.Split(loneAmpersands(s), -1) {
		words := pieceWords(piece)
		if len(t.rm.checkCommand(words, vars, inlineNone)) > 0 {
			hits = append(hits, "`"+strings.TrimSpace(strings.ReplaceAll(piece, mark, "$(…)"))+"`")
		}
	}
	return hits
}

// loneAmpersands заменяет & фонового запуска на ;, не трогая &&, &> и >&
func loneAmpersands(s string) string {
	r := []rune(s)
	for i, c := range r {
		if c != '&' {
			continue
		}
		prevOK := i == 0 || !strings.ContainsRune("<>&", r[i-1])
		nextOK := i+1 == len(r) || !strings.ContainsRune("<>&", r[i+1])
		if prevOK && nextOK {
			r[i] = ';'
		}
	}
	return string(r)
}

// pieceWords слова куска по пробелам, как их видит Claude Code: без
// ведущих { и ключевых слов, перенаправления помечены
func pieceWords(piece string) []shWord {
	fields := strings.Fields(strings.TrimLeft(piece, " \t"+string(substMark)))
	for len(fields) > 0 && (fields[0] == "{" || rmKeywords[fields[0]]) {
		fields = fields[1:]
	}
	words := make([]shWord, 0, len(fields))
	redirNext := false
	for _, f := range fields {
		w := shWord{raw: f, text: f, redirect: redirNext}
		redirNext = false
		if redirToken.MatchString(f) {
			w.redirect = true
			redirNext = bareRedirOp.MatchString(f)
		}
		words = append(words, w)
	}
	return words
}
