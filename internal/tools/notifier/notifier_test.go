package notifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newNotifier(t *testing.T, config core.ToolConfig) *Tool {
	t.Helper()

	tool, err := New(config, testLogger(t))
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}
	return tool
}

func TestNotifierTool_IgnoresToolOperations(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: true})

	for _, toolName := range []string{"Write", "Edit", "Bash"} {
		t.Run(toolName, func(t *testing.T) {
			result, err := tool.ValidateTool(context.Background(), &core.ToolInput{ToolName: toolName})
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}
			if !result.IsValid || len(result.Suggestions) > 0 {
				t.Error("notifier обрабатывает только события сессии")
			}
		})
	}
}

func TestNotifierTool_HandlesSessionEvents(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: true, Sound: false, Desktop: false})

	for _, event := range []string{core.EventStop, core.EventNotification} {
		t.Run(event, func(t *testing.T) {
			result, err := tool.ValidateTool(context.Background(), &core.ToolInput{
				ToolName: event,
				CWD:      "/home/user/work/my-project",
				Message:  "Claude needs your permission to use Bash",
			})
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}
			if len(result.Suggestions) == 0 {
				t.Fatalf("событие %s должно приводить к уведомлению", event)
			}
			if !strings.Contains(result.Suggestions[0], "my-project") {
				t.Errorf("имя проекта не определено: %q", result.Suggestions[0])
			}
		})
	}
}

func TestNotifierTool_SkipsRepeatedReminders(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: true, Sound: false, Desktop: false})

	tests := []struct {
		name     string
		previous core.SessionState
		event    string
		notified bool
	}{
		// Пока Claude ждёт, Claude Code напоминает о себе тем же событием
		{name: "первый вопрос", previous: core.StateWorking, event: core.EventNotification, notified: true},
		{name: "напоминание о вопросе", previous: core.StateWaiting, event: core.EventNotification, notified: false},
		{name: "завершение работы", previous: core.StateWorking, event: core.EventStop, notified: true},
		{name: "напоминание после завершения", previous: core.StateDone, event: core.EventNotification, notified: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := core.WithPreviousState(context.Background(), tt.previous)
			result, err := tool.ValidateTool(ctx, &core.ToolInput{
				ToolName: tt.event,
				CWD:      "/home/user/work/my-project",
			})
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if notified := len(result.Suggestions) > 0; notified != tt.notified {
				t.Errorf("уведомление отправлено = %v, ожидалось %v", notified, tt.notified)
			}
		})
	}
}

func TestNotifierTool_BuildAlert(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{
		Enabled:         true,
		Sound:           true,
		Desktop:         true,
		ActivateOnClick: true,
	})

	t.Run("вопрос показывает текст запроса", func(t *testing.T) {
		alert, title, ok := tool.buildAlert(&core.ToolInput{
			ToolName: core.EventNotification,
			Message:  "Claude needs your permission to use Bash",
		}, "my-project")

		if !ok {
			t.Fatal("событие должно обрабатываться")
		}
		if alert.Message != "Claude needs your permission to use Bash" {
			t.Errorf("текст запроса не подставлен: %q", alert.Message)
		}
		if !strings.Contains(title, "ждёт ответа") {
			t.Errorf("заголовок окна: %q", title)
		}
		// Без списка процессов уведомление останется без действия по клику
		if len(alert.ActivatePIDs) == 0 {
			t.Error("процессы для активации окна не определены")
		}
	})

	t.Run("завершение работы без текста запроса", func(t *testing.T) {
		alert, title, ok := tool.buildAlert(&core.ToolInput{ToolName: core.EventStop}, "my-project")

		if !ok {
			t.Fatal("событие должно обрабатываться")
		}
		if !strings.Contains(alert.Message, "my-project") {
			t.Errorf("сообщение: %q", alert.Message)
		}
		if !strings.Contains(title, "готово") {
			t.Errorf("заголовок окна: %q", title)
		}
	})

	t.Run("посторонние операции игнорируются", func(t *testing.T) {
		if _, _, ok := tool.buildAlert(&core.ToolInput{ToolName: "Write"}, "my-project"); ok {
			t.Error("Write не является событием сессии")
		}
	})
}

func TestNotifierTool_ActivationDisabled(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: true, Desktop: true, ActivateOnClick: false})

	alert, _, ok := tool.buildAlert(&core.ToolInput{ToolName: core.EventStop}, "my-project")
	if !ok {
		t.Fatal("событие должно обрабатываться")
	}
	if len(alert.ActivatePIDs) != 0 {
		t.Error("при выключенной активации список процессов должен быть пуст")
	}
}

// Оба события заявлены как поддерживаемые: иначе движок не вызовет инструмент
func TestNotifierTool_SupportedTools(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: true})

	supported := strings.Join(tool.SupportedTools(), ",")
	for _, event := range []string{core.EventStop, core.EventNotification} {
		if !strings.Contains(supported, event) {
			t.Errorf("событие %s не заявлено: %s", event, supported)
		}
	}
}

func TestNotifierTool_Disabled(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: false})

	result, err := tool.ValidateTool(context.Background(), &core.ToolInput{ToolName: "Stop"})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if len(result.Suggestions) > 0 {
		t.Error("выключенный инструмент не должен отправлять уведомления")
	}
}

func TestNotifierTool_ProjectName(t *testing.T) {
	tool := newNotifier(t, core.ToolConfig{Enabled: true})
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Проекты вне ~/work и вложенные каталоги: имя определяется по факту, а не по шаблону пути
	for _, dir := range []string{"work/claude-hooks", "git/life", "work/saga/frontend/admin-app"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatalf("не удалось создать каталог: %v", err)
		}
	}
	encodedHome := strings.ReplaceAll(home, "/", "-")

	tests := []struct {
		name     string
		input    core.ToolInput
		expected string
	}{
		{
			name:     "рабочая директория сессии приоритетна",
			input:    core.ToolInput{CWD: filepath.Join(home, "git/life")},
			expected: "life",
		},
		{
			name:     "домашний каталог вместо имени пользователя",
			input:    core.ToolInput{CWD: home},
			expected: "~",
		},
		{
			name:     "корень файловой системы",
			input:    core.ToolInput{CWD: "/"},
			expected: "/",
		},
		{
			name:     "дефис в имени проекта не является разделителем",
			input:    core.ToolInput{TranscriptPath: "/p/" + encodedHome + "-work-claude-hooks/s.jsonl"},
			expected: "claude-hooks",
		},
		{
			name:     "проект вне рабочего каталога",
			input:    core.ToolInput{TranscriptPath: "/p/" + encodedHome + "-git-life/s.jsonl"},
			expected: "life",
		},
		{
			// Родительский каталог различает ~/work/saga/frontend и ~/work/glint/frontend
			name:     "вложенный проект показывается вместе с родителем",
			input:    core.ToolInput{TranscriptPath: "/p/" + encodedHome + "-work-saga-frontend-admin-app/s.jsonl"},
			expected: "frontend/admin-app",
		},
		{
			name:     "проект первого уровня остаётся без родителя",
			input:    core.ToolInput{CWD: filepath.Join(home, "work/claude-hooks")},
			expected: "claude-hooks",
		},
		{
			name:     "сессия из домашнего каталога",
			input:    core.ToolInput{TranscriptPath: "/p/" + encodedHome + "/s.jsonl"},
			expected: "~",
		},
		{
			name:     "удалённый каталог: дефис считается разделителем",
			input:    core.ToolInput{TranscriptPath: "/p/-var-lib-service/s.jsonl"},
			expected: "service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tool.ProjectName(&tt.input); got != tt.expected {
				t.Errorf("ожидалось %q, получено %q", tt.expected, got)
			}
		})
	}
}

func TestDecodeProjectDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my-app", "sub"), 0o755); err != nil {
		t.Fatalf("не удалось создать каталог: %v", err)
	}
	encodedRoot := strings.ReplaceAll(root, "/", "-")

	tests := map[string]string{
		encodedRoot + "-my-app":     filepath.Join(root, "my-app"),
		encodedRoot + "-my-app-sub": filepath.Join(root, "my-app", "sub"),
		"":                          "",
		"-":                         "",
	}

	for encoded, expected := range tests {
		t.Run(encoded, func(t *testing.T) {
			if got := decodeProjectDir(encoded); got != expected {
				t.Errorf("ожидалось %q, получено %q", expected, got)
			}
		})
	}
}

// Путь может содержать каталог, чьё имя начинается с дефиса: /tmp/x/-home-user
func TestDecodeProjectDir_LeadingDashInName(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "-dashed", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("не удалось создать каталог: %v", err)
	}

	encoded := strings.ReplaceAll(root, "/", "-") + "--dashed-app"
	if got := decodeProjectDir(encoded); got != nested {
		t.Errorf("ожидалось %q, получено %q", nested, got)
	}
}

// Напоминание об ожидании глушится при живых фоновых задачах, запрос
// разрешения — никогда
func TestIsIdleReminder(t *testing.T) {
	if !IsIdleReminder("Claude is waiting for your input") {
		t.Error("минутное напоминание не распознано")
	}
	if IsIdleReminder("Claude needs your permission to use Bash") {
		t.Error("запрос разрешения не должен считаться напоминанием")
	}
	if IsIdleReminder("") {
		t.Error("пустое сообщение не напоминание")
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
