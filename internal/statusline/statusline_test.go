package statusline

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// ansiPattern убирает оформление: проверяется содержимое, а не escape-коды
var ansiPattern = regexp.MustCompile(`\033\[[0-9;]*m`)

func plain(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func TestProjectBadgeShowsState(t *testing.T) {
	tests := []struct {
		name        string
		state       core.SessionState
		contextUsed float64
		expected    string
	}{
		{name: "работа", state: core.StateWorking, contextUsed: 10, expected: badgeBlue},
		{name: "ожидание ответа", state: core.StateWaiting, contextUsed: 10, expected: badgeAmber},
		{name: "работа завершена", state: core.StateDone, contextUsed: 10, expected: badgeBlue},
		// Переполненный контекст важнее прочего: скоро сожмётся история
		{name: "контекст важнее ожидания", state: core.StateWaiting, contextUsed: 90, expected: badgeRed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			badge := projectBadge("/home/user/work/demo", tt.state, tt.contextUsed)
			if !strings.HasPrefix(badge, tt.expected) {
				t.Errorf("ожидался цвет %q, получено %q", tt.expected, badge)
			}
			if got := plain(badge); got != " DEMO " {
				t.Errorf("ожидалось %q, получено %q", " DEMO ", got)
			}
		})
	}
}

func TestContextBar(t *testing.T) {
	tests := []struct {
		used     float64
		expected string
	}{
		{used: 0, expected: "ctx ░░░░░░░░░░ 0%"},
		{used: 47, expected: "ctx ▓▓▓▓░░░░░░ 47%"},
		{used: 100, expected: "ctx ▓▓▓▓▓▓▓▓▓▓ 100%"},
		// Заявленное переполнение не должно рисовать полосу длиннее шкалы
		{used: 140, expected: "ctx ▓▓▓▓▓▓▓▓▓▓ 100%"},
	}

	for _, tt := range tests {
		if got := plain(contextBar(tt.used)); got != tt.expected {
			t.Errorf("contextBar(%v) = %q, ожидалось %q", tt.used, got, tt.expected)
		}
	}
}

func TestGitSummary(t *testing.T) {
	tests := []struct {
		name     string
		git      GitStatus
		expected string
	}{
		{
			name:     "чистый репозиторий",
			git:      GitStatus{IsRepo: true, Branch: "main", RepoClean: true},
			expected: "main",
		},
		{
			name:     "изменения и расхождение с remote",
			git:      GitStatus{IsRepo: true, Branch: "feature", Changed: 3, Ahead: 2, Behind: 1},
			expected: "feature ●3 ↑2 ↓1",
		},
		{
			name:     "отделённая HEAD",
			git:      GitStatus{IsRepo: true, Branch: "a1b2c3d", Detached: true},
			expected: "⚠ a1b2c3d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plain(gitSummary(tt.git)); got != tt.expected {
				t.Errorf("ожидалось %q, получено %q", tt.expected, got)
			}
		})
	}
}

func TestTerminalTitle(t *testing.T) {
	git := GitStatus{IsRepo: true, Branch: "main"}

	tests := []struct {
		name        string
		git         GitStatus
		state       core.SessionState
		contextUsed float64
		expected    string
	}{
		{name: "работа", git: git, state: core.StateWorking, expected: "🔵 DEMO · main"},
		{name: "ожидание ответа", git: git, state: core.StateWaiting, expected: "❓ DEMO · main"},
		{name: "работа завершена", git: git, state: core.StateDone, expected: "✅ DEMO · main"},
		{name: "контекст на исходе", git: git, state: core.StateWorking, contextUsed: 90, expected: "🔴 DEMO · main"},
		{name: "каталог вне репозитория", git: GitStatus{}, state: core.StateWorking, expected: "🔵 DEMO"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalTitle("/home/user/work/demo", tt.git, tt.state, tt.contextUsed)
			if got != tt.expected {
				t.Errorf("ожидалось %q, получено %q", tt.expected, got)
			}
		})
	}
}

func TestDetails(t *testing.T) {
	input := Input{}
	input.Model.DisplayName = "Opus 5"
	input.Effort.Level = "high"
	input.ContextWindow.UsedPercentage = 47
	input.Cost.LinesAdded = 412
	input.Cost.LinesRemoved = 87

	parts := details(input, GitStatus{IsRepo: true, Branch: "main", RepoClean: true})

	expected := []string{"main", "Opus 5·high", "ctx ▓▓▓▓░░░░░░ 47%", "+412 −87"}
	if len(parts) != len(expected) {
		t.Fatalf("ожидалось %d элементов, получено %d: %q", len(expected), len(parts), parts)
	}
	for i, want := range expected {
		if got := plain(parts[i]); got != want {
			t.Errorf("элемент %d: ожидалось %q, получено %q", i, want, got)
		}
	}
}

func TestShortModel(t *testing.T) {
	tests := map[string]string{
		"Opus 5 (1M context)": "Opus 5",
		"Opus 5":              "Opus 5",
		"":                    "",
	}

	for model, expected := range tests {
		if got := shortModel(model); got != expected {
			t.Errorf("shortModel(%q) = %q, ожидалось %q", model, got, expected)
		}
	}
}

func TestBuildFitsSingleLine(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	input := Input{CWD: t.TempDir()}
	input.Model.DisplayName = "Opus 5 (1M context)"
	input.ContextWindow.UsedPercentage = 47

	line, title := build(context.Background(), input)

	if strings.Contains(line, "\n") {
		t.Errorf("строка статуса должна умещаться в одну строку: %q", line)
	}
	if strings.Contains(plain(line), "1M context") {
		t.Errorf("уточнение модели должно убираться: %q", plain(line))
	}
	if !strings.HasPrefix(title, "🔵 ") {
		t.Errorf("заголовок должен начинаться со значка состояния: %q", title)
	}
}

func TestDetailsSkipsMissingData(t *testing.T) {
	// Claude Code передаёт не все поля: пустые сведения не должны давать пустых разделителей
	if parts := details(Input{}, GitStatus{}); len(parts) != 0 {
		t.Errorf("ожидался пустой список, получено %q", parts)
	}
}

func TestWorkingDirPrefersSessionDir(t *testing.T) {
	input := Input{CWD: "/from/cwd"}
	input.Workspace.CurrentDir = "/from/workspace"
	input.Workspace.ProjectDir = "/from/project"

	if got := workingDir(input); got != "/from/workspace" {
		t.Errorf("ожидалось /from/workspace, получено %q", got)
	}

	input.Workspace.CurrentDir = ""
	if got := workingDir(input); got != "/from/cwd" {
		t.Errorf("ожидалось /from/cwd, получено %q", got)
	}

	input.CWD = ""
	if got := workingDir(input); got != "/from/project" {
		t.Errorf("ожидалось /from/project, получено %q", got)
	}
}

func TestShortenPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		path     string
		expected string
	}{
		{path: home, expected: "~"},
		{path: filepath.Join(home, "work/demo"), expected: "~/work/demo"},
		{path: "/etc/nginx", expected: "/etc/nginx"},
	}

	for _, tt := range tests {
		if got := shortenPath(tt.path); got != tt.expected {
			t.Errorf("shortenPath(%q) = %q, ожидалось %q", tt.path, got, tt.expected)
		}
	}
}
