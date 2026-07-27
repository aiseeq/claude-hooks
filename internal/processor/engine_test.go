package processor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newEngine(t *testing.T, config *core.Config) *Engine {
	t.Helper()

	engine, err := New(config, core.NewTestLogger())
	if err != nil {
		t.Fatalf("не удалось создать процессор: %v", err)
	}
	return engine
}

func writeInput(t *testing.T, filePath, content string) *core.ToolInput {
	t.Helper()

	raw, err := json.Marshal(map[string]string{"file_path": filePath, "content": content})
	if err != nil {
		t.Fatalf("не удалось собрать tool_input: %v", err)
	}

	return &core.ToolInput{
		ToolName:  "Write",
		ToolInput: raw,
		FilePath:  filePath,
		Content:   content,
	}
}

func TestEngine_BlocksCriticalViolation(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Validators: map[string]core.ValidatorConfig{
			"secrets": {Enabled: true},
		},
	})

	response, err := engine.ProcessPreToolUse(context.Background(),
		writeInput(t, "/project/internal/config.go", `key := "sk_live_sample-key-value-0000"`))
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionBlock {
		t.Errorf("ожидалась блокировка, получено %s", response.Action)
	}
	if response.Level != core.LevelCritical {
		t.Errorf("ожидался критический уровень, получен %s", response.Level)
	}
	if response.Message == "" {
		t.Error("сообщение должно объяснять причину блокировки")
	}
}

func TestEngine_AllowsCleanCode(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Validators: map[string]core.ValidatorConfig{
			"secrets":      {Enabled: true},
			"runtime_exit": {Enabled: true, GoFilesOnly: true},
		},
	})

	response, err := engine.ProcessPreToolUse(context.Background(),
		writeInput(t, "/project/internal/service.go", "package service\n\nfunc Value() int { return 42 }\n"))
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionAllow {
		t.Errorf("ожидалось разрешение, получено %s (%+v)", response.Action, response.Violations)
	}
}

func TestEngine_DisabledValidatorsAreNotCreated(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Validators: map[string]core.ValidatorConfig{
			"secrets": {Enabled: false},
		},
	})

	if len(engine.validators) != 0 {
		t.Errorf("выключенный валидатор не должен создаваться, создано %d", len(engine.validators))
	}
}

func TestEngine_ValidatorsSkipNonFileOperations(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Validators: map[string]core.ValidatorConfig{
			"secrets": {Enabled: true},
		},
		Tools: map[string]core.ToolConfig{
			"bash": {Enabled: true},
		},
	})

	response, err := engine.ProcessPreToolUse(context.Background(), &core.ToolInput{
		ToolName: "Bash",
		Command:  "echo 'sk_live_sample-key-value-0000'",
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionAllow {
		t.Errorf("валидаторы содержимого не применяются к Bash, получено %s", response.Action)
	}
}

func TestEngine_BlocksDangerousBashCommand(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Tools: map[string]core.ToolConfig{
			"bash": {Enabled: true},
		},
	})

	response, err := engine.ProcessPreToolUse(context.Background(), &core.ToolInput{
		ToolName: "Bash",
		Command:  "rm -rf /",
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionBlock {
		t.Errorf("ожидалась блокировка команды, получено %s", response.Action)
	}
}

// Stop-хук должен получать данные сессии: имя проекта берется из них
func TestEngine_ProcessStopKeepsSessionData(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Tools: map[string]core.ToolConfig{
			"notifier": {Enabled: true, Sound: false, Desktop: false},
		},
	})

	response, err := engine.ProcessStop(context.Background(), &core.ToolInput{
		CWD:            "/home/user/work/my-project",
		TranscriptPath: "/home/user/.claude/projects/-home-user-work-my-project/session.jsonl",
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionAllow {
		t.Errorf("stop-хук ничего не блокирует, получено %s", response.Action)
	}
	if len(response.Suggestions) == 0 {
		t.Fatal("notifier должен сообщить об отправке уведомления")
	}
	if got := response.Suggestions[0]; !strings.Contains(got, "my-project") {
		t.Errorf("имя проекта не определено: %q", got)
	}
}

// Запрос разрешения уведомляет так же, как завершение работы
func TestEngine_ProcessNotification(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Tools: map[string]core.ToolConfig{
			"notifier": {Enabled: true, Sound: false, Desktop: false},
		},
	})

	response, err := engine.ProcessNotification(context.Background(), &core.ToolInput{
		CWD:     "/home/user/work/my-project",
		Message: "Claude needs your permission to use Bash",
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionAllow {
		t.Errorf("уведомление ничего не блокирует, получено %s", response.Action)
	}
	if len(response.Suggestions) == 0 {
		t.Fatal("notifier должен сообщить об отправке уведомления")
	}
	if !strings.Contains(response.Suggestions[0], "my-project") {
		t.Errorf("имя проекта не определено: %q", response.Suggestions[0])
	}
}

// Валидаторы содержимого к событиям сессии не применяются
func TestEngine_NotificationSkipsValidators(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Validators: map[string]core.ValidatorConfig{
			"secrets": {Enabled: true},
		},
	})

	response, err := engine.ProcessNotification(context.Background(), &core.ToolInput{
		Message: `sk_live_sample-key-value-0000`,
	})
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}
	if response.Action != core.HookActionAllow {
		t.Errorf("ожидалось разрешение, получено %s", response.Action)
	}
}

func TestEngine_PostToolUseDoesNotRunContentValidators(t *testing.T) {
	engine := newEngine(t, &core.Config{
		Validators: map[string]core.ValidatorConfig{
			"secrets": {Enabled: true},
		},
	})

	response, err := engine.ProcessPostToolUse(context.Background(),
		writeInput(t, "/project/internal/config.go", `key := "sk_live_sample-key-value-0000"`))
	if err != nil {
		t.Fatalf("обработка не удалась: %v", err)
	}

	if response.Action != core.HookActionAllow {
		t.Errorf("после записи файла блокировать нечего, получено %s", response.Action)
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
