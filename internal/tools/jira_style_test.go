package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newJiraStyleTool(t *testing.T) *JiraStyleTool {
	t.Helper()
	tool, err := NewJiraStyleTool(core.ToolConfig{Enabled: true}, testLogger(t))
	if err != nil {
		t.Fatalf("NewJiraStyleTool: %v", err)
	}
	return tool
}

func validateJiraCommand(t *testing.T, command string) *core.ValidationResult {
	t.Helper()
	result, err := newJiraStyleTool(t).ValidateTool(context.Background(), &core.ToolInput{
		ToolName: "Bash",
		Command:  command,
	})
	if err != nil {
		t.Fatalf("ValidateTool: %v", err)
	}
	return result
}

func TestJiraStyleBlocksEmDashInInlinePayload(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -s -X POST --data-binary '{"body":"Готово — выкачено"}' "$JIRA_BASE_URL/rest/api/3/issue/SI-123/comment"`)
	if result.IsValid {
		t.Fatal("длинное тире в теле комментария должно блокироваться")
	}
}

func TestJiraStyleBlocksEscapedEmDash(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -X PUT -d '{"text":"a — b"}' https://x.atlassian.net/rest/api/3/issue/SI-1/comment/42`)
	if result.IsValid {
		t.Fatal("длинное тире в escape-форме должно блокироваться")
	}
}

func TestJiraStyleBlocksMarkdownInPayloadFile(t *testing.T) {
	payload := filepath.Join(t.TempDir(), "comment.json")
	if err := os.WriteFile(payload, []byte(`{"body":"**Итог:** сделано"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := validateJiraCommand(t,
		`curl -s -X POST --data-binary @`+payload+` "$JIRA_BASE_URL/rest/api/3/issue/SI-123/comment"`)
	if result.IsValid {
		t.Fatal("markdown в файле с телом комментария должен блокироваться")
	}
}

func TestJiraStyleAllowsPlainComment(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -s -X POST --data-binary '{"body":"Выкачено на UAT, версия 2.0.123"}' "$JIRA_BASE_URL/rest/api/3/issue/SI-123/comment"`)
	if !result.IsValid {
		t.Fatalf("обычный комментарий не должен блокироваться: %+v", result.Violations)
	}
}

func TestJiraStyleIgnoresNonCommentRequests(t *testing.T) {
	result := validateJiraCommand(t,
		`echo "тире — в обычной команде" && curl "$JIRA_BASE_URL/rest/api/3/issue/SI-123?fields=summary"`)
	if !result.IsValid {
		t.Fatal("команда без комментария в Jira не должна блокироваться")
	}
}

func TestJiraStyleIgnoresReadingComments(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -s "$JIRA_BASE_URL/rest/api/3/issue/SI-123/comment" | python3 render.py # — сверка`)
	if result.IsValid {
		return
	}
	// GET за комментариями не содержит тела, но тире в остальной команде не повод блокировать чтение
	t.Fatal("чтение комментариев не должно блокироваться")
}
