package notifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newNotifier(t *testing.T, config core.ToolConfig) *Notifier {
	t.Helper()
	return New(config, testLogger(t))
}

func TestNotifier_BuildAlert(t *testing.T) {
	notifier := newNotifier(t, core.ToolConfig{
		Enabled:         true,
		Sound:           true,
		Desktop:         true,
		ActivateOnClick: true,
	})

	t.Run("вопрос показывает текст запроса", func(t *testing.T) {
		alert, title, err := notifier.buildAlert(core.EventNotification, &core.ToolInput{
			Message: "Claude needs your permission to use Bash",
		}, "my-project")
		if err != nil {
			t.Fatalf("событие должно обрабатываться: %v", err)
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
		alert, title, err := notifier.buildAlert(core.EventStop, &core.ToolInput{}, "my-project")
		if err != nil {
			t.Fatalf("событие должно обрабатываться: %v", err)
		}
		if !strings.Contains(alert.Message, "my-project") {
			t.Errorf("сообщение: %q", alert.Message)
		}
		if !strings.Contains(title, "готово") {
			t.Errorf("заголовок окна: %q", title)
		}
	})

	t.Run("посторонние события — ошибка", func(t *testing.T) {
		if _, _, err := notifier.buildAlert("Write", &core.ToolInput{}, "my-project"); err == nil {
			t.Error("Write не является событием сессии")
		}
	})
}

func TestNotifier_ActivationDisabled(t *testing.T) {
	notifier := newNotifier(t, core.ToolConfig{Enabled: true, Desktop: true, ActivateOnClick: false})

	alert, _, err := notifier.buildAlert(core.EventStop, &core.ToolInput{}, "my-project")
	if err != nil {
		t.Fatalf("событие должно обрабатываться: %v", err)
	}
	if len(alert.ActivatePIDs) != 0 {
		t.Error("при выключенной активации список процессов должен быть пуст")
	}
}

// Без звука и уведомления Announce не запускает фоновый процесс и не падает
func TestNotifier_AnnounceSilent(t *testing.T) {
	t.Setenv("KONSOLE_DBUS_SERVICE", "")
	notifier := newNotifier(t, core.ToolConfig{Enabled: true})

	decision := Decision{State: core.StateDone, Alert: true}
	if err := notifier.Announce(core.EventStop, &core.ToolInput{CWD: "/tmp/p"}, decision); err != nil {
		t.Errorf("Announce: %v", err)
	}
	if err := notifier.Announce(core.EventStop, &core.ToolInput{}, Decision{State: core.StatePaused}); err != nil {
		t.Errorf("пауза ничего не объявляет: %v", err)
	}
}

func TestNotifierTool_ProjectName(t *testing.T) {
	notifier := newNotifier(t, core.ToolConfig{Enabled: true})
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Проекты вне ~/work и вложенные каталоги: имя определяется по факту, а не по шаблону пути
	for _, dir := range []string{"work/claude-hooks", "git/life", "work/projecta/frontend/admin-app"} {
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
			// Агент сделал cd в подпапку чужого проекта: вкладка называет сессию по каталогу запуска
			name:     "каталог запуска приоритетнее текущего",
			input:    core.ToolInput{CWD: filepath.Join(home, "work/projecta/frontend"), TranscriptPath: "/p/" + encodedHome + "-work-claude-hooks/s.jsonl"},
			expected: "claude-hooks",
		},
		{
			name:     "без транскрипта берётся текущий каталог",
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
			// Родительский каталог различает ~/work/projecta/frontend и ~/work/glint/frontend
			name:     "вложенный проект показывается вместе с родителем",
			input:    core.ToolInput{TranscriptPath: "/p/" + encodedHome + "-work-projecta-frontend-admin-app/s.jsonl"},
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
			if got := notifier.ProjectName(&tt.input); got != tt.expected {
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
