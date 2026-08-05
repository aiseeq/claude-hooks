package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
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

// maxPayloadFileSize файл больше этого размера телом комментария не является
const maxPayloadFileSize = 1 << 20

// jiraStyleMarker признак текста, написанного агентом мимо правил write-as-user
type jiraStyleMarker struct {
	regexp      *regexp.Regexp
	description string
}

var jiraStyleMarkers = []jiraStyleMarker{
	{regexp.MustCompile("—"), "длинное тире"},
	{regexp.MustCompile(`\\u2014`), "длинное тире в escape-форме"},
	{regexp.MustCompile(`\*\*[^*\n]+\*\*`), "markdown-жирный"},
	{regexp.MustCompile(`(?m)^#{1,6}\s`), "markdown-заголовок"},
}

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
func (t *JiraStyleTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() || input.ToolName != "Bash" || input.Command == "" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	if !jiraCommentEndpoint.MatchString(input.Command) || !jiraWriteRequest.MatchString(input.Command) {
		return &core.ValidationResult{IsValid: true}, nil
	}

	texts := []string{input.Command}
	for _, m := range jiraPayloadFile.FindAllStringSubmatch(input.Command, -1) {
		path := m[1] + m[2] + m[3]
		if body, ok := readPayloadFile(path); ok {
			texts = append(texts, body)
		}
	}

	var violations []core.Violation
	for _, marker := range jiraStyleMarkers {
		for _, text := range texts {
			if !marker.regexp.MatchString(text) {
				continue
			}
			violations = append(violations, core.Violation{
				Type:       "jira_comment_style",
				Message:    fmt.Sprintf("В комментарии для Jira %s — признак текста мимо правил write-as-user", marker.description),
				Suggestion: "Перепиши комментарий по скиллу write-as-user: без длинных тире, без markdown, разговорным текстом",
				Severity:   core.LevelCritical,
				Line:       1,
			})
			break
		}
	}

	return &core.ValidationResult{
		IsValid:    len(violations) == 0,
		Violations: violations,
	}, nil
}

// readPayloadFile читает файл с телом запроса, пропуская несуществующие и большие
func readPayloadFile(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" {
		return "", false
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, "~/") {
		path = home + path[1:]
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxPayloadFileSize {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}
