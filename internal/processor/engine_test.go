package processor

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newEngine(t *testing.T, config *core.Config) *Engine {
	t.Helper()

	engine, err := New(config, testLogger(t))
	if err != nil {
		t.Fatalf("не удалось создать процессор: %v", err)
	}
	return engine
}

func TestEngine_BlocksJiraCommentStyle(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Tools: map[string]core.ToolConfig{
			"jira_style": {Enabled: true},
		},
	})

	response, err := engine.ProcessPreToolUse(context.Background(), &core.ToolInput{
		ToolName: "Bash",
		Command:  "curl -X POST https://x.atlassian.net/rest/api/3/issue/PROJ-1/comment -d '{\"body\":\"готово — проверь\"}'",
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionBlock {
		t.Errorf("ожидалась блокировка комментария, получено %s", response.Action)
	}
}

func TestEngine_AllowsOrdinaryCommand(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Tools: map[string]core.ToolConfig{
			"jira_style": {Enabled: true},
		},
	})

	response, err := engine.ProcessPreToolUse(context.Background(), &core.ToolInput{
		ToolName: "Bash",
		Command:  "echo 'сборка — готово'",
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionAllow {
		t.Errorf("команда не для Jira проходит, получено %s", response.Action)
	}
}

func TestEngine_DisabledToolsAreNotCreated(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Tools: map[string]core.ToolConfig{
			"jira_style": {Enabled: false},
		},
	})

	if len(engine.tools) != 0 {
		t.Errorf("выключенная проверка не должна создаваться, создано %d", len(engine.tools))
	}
}

func TestDeduplicate(t *testing.T) {
	got := deduplicate([]string{"a", "b", "a", "c", "b"})
	expected := []string{"a", "b", "c"}

	if len(got) != len(expected) {
		t.Fatalf("получено %v, ожидалось %v", got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("позиция %d: получено %q, ожидалось %q", i, got[i], expected[i])
		}
	}
}

// testLogger — логгер для тестов пакета: пишет в stderr, который go test
// показывает только при провале
func testLogger(t *testing.T) core.Logger {
	t.Helper()
	logger, err := core.NewLogger(core.LoggerConfig{Level: "debug"})
	if err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
	return logger
}
