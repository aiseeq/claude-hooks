package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// jiraCommentEndpoint запрос, создающий или редактирующий комментарий в Jira
var jiraCommentEndpoint = regexp.MustCompile(`rest/api/[23]/issue/[^/\s"']+/comment`)

// jiraPayloadFile ссылка на файл с телом запроса: -d @f, --data-binary @f, --data '@f'
var jiraPayloadFile = regexp.MustCompile(`(?:-d|--data(?:-binary|-raw)?)[=\s]+(?:'@([^']+)'|"@([^"]+)"|@([^\s'"]+))`)

// jiraWriteRequest признак записи: тело запроса или явный POST/PUT.
// Чтение комментариев (GET) проверять нельзя — тире в остальной команде не относится к Jira
var jiraWriteRequest = regexp.MustCompile(`(?i)--data(?:-binary|-raw)?\b|(?:^|\s)-d[=\s]|-X\s*(?:POST|PUT)\b|--request\s*(?:POST|PUT)\b`)

// sedSubstitution замена s/старое/новое/ в sed или perl с разделителем
// /, | или #: одна из групп - старый текст
var sedSubstitution = regexp.MustCompile(`(?:^|[\s'"])s(?:/((?:[^/\\\n]|\\.)*)/|\|((?:[^|\\\n]|\\.)*)\||#((?:[^#\\\n]|\\.)*)#)`)

// stripSedPatterns убирает из команды старый текст замен sed: правка
// комментария после блокировки ('s/три проекта/3 проекта/') несёт в шаблоне
// именно то, что заменяется. Новый текст остаётся и проверяется
func stripSedPatterns(command string) string {
	var b strings.Builder
	last := 0
	for _, m := range sedSubstitution.FindAllStringSubmatchIndex(command, -1) {
		for group := 1; group <= 3; group++ {
			if start := m[2*group]; start >= 0 {
				b.WriteString(command[last:start])
				last = m[2*group+1]
			}
		}
	}
	b.WriteString(command[last:])
	return b.String()
}

// maxPayloadFileSize файл больше этого размера телом комментария не является
const maxPayloadFileSize = 1 << 20

// JiraStyleTool блокирует отправку комментария в Jira с маркерами агентского стиля
type JiraStyleTool struct {
	*BaseTool
}

// NewJiraStyleTool создает валидатор стиля Jira-комментариев
func NewJiraStyleTool(config core.ToolConfig, logger core.Logger) (*JiraStyleTool, error) {
	return &JiraStyleTool{
		BaseTool: NewBaseTool("jira_style", config.Enabled, []string{"Bash"}, logger),
	}, nil
}

// ValidateTool проверяет команду, постящую комментарий в Jira
func (t *JiraStyleTool) ValidateTool(_ context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() || input.ToolName != "Bash" || input.Command == "" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	if !jiraCommentEndpoint.MatchString(input.Command) || !jiraWriteRequest.MatchString(input.Command) {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Текст комментария - раскодированные строки JSON (тело в -d '...',
	// heredoc или генераторе в той же команде) и файлы тела -d @path.
	// Остальная команда - код: шаблон grep, assert, комментарий shell. Левая
	// часть замены sed - старый текст, который команда и убирает
	command := stripSedPatterns(input.Command)
	texts := jsonStrings(command)
	var bodies []string
	for _, m := range jiraPayloadFile.FindAllStringSubmatch(command, -1) {
		path := m[1] + m[2] + m[3]
		body, err := readPayloadFile(path)
		if err != nil {
			// Файл тела часто создаётся этой же командой или назван через
			// переменную shell ($S/c.json): до запуска его нет. Тогда текст
			// берётся из самой команды, пропуск файла виден в логе
			t.Logger().Info("jira payload file unavailable, checking the command text only",
				"path", path, "error", err)
			continue
		}
		texts = append(texts, jsonStrings(body)...)
		bodies = append(bodies, body)
	}
	// Строки JSON первыми: фрагмент в подсказке из них читается без
	// JSON-обвязки
	texts = append(texts, bodies...)
	everywhere := append(slices.Clone(texts), command)

	var violations []core.Violation
	var suggestions []string
	for _, marker := range jiraStyleMarkers {
		scope := everywhere
		if marker.textOnly {
			scope = texts
		}
		for _, text := range scope {
			fragment := marker.find(text)
			if fragment == "" {
				continue
			}
			suggestion := fmt.Sprintf("%s: %q, нужно: %s", marker.description, strings.TrimSpace(fragment), marker.fix)
			violations = append(violations, core.NewViolation(
				"jira_comment_style",
				// Сообщение одно на все нарушения: ответ хука показывает
				// первое, а список найденного идёт подсказками
				"Комментарий для Jira написан мимо правил формы write-as-user, исправить:",
				suggestion,
				core.LevelCritical,
				0,
				0,
			))
			suggestions = append(suggestions, suggestion)
			break
		}
	}
	if len(suggestions) > 0 {
		suggestions = append(suggestions, "Перепиши комментарий по скиллу write-as-user и отправь заново")
	}

	return core.NewValidationResult(len(violations) == 0, violations, suggestions), nil
}

// readPayloadFile читает файл с телом запроса. Ошибка называет причину, по
// которой файл не годится: stdin вместо файла, каталог, слишком большой,
// нечитаемый
func readPayloadFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" {
		return "", fmt.Errorf("payload comes from stdin, not a file")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ~ in %q: %w", path, err)
		}
		path = home + path[1:]
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat payload file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxPayloadFileSize {
		return "", fmt.Errorf("%s is %d bytes, larger than %d", path, info.Size(), maxPayloadFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read payload file: %w", err)
	}
	return string(data), nil
}
