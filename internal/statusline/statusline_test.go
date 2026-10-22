package statusline

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

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
			git:      GitStatus{IsRepo: true, Branch: "main", Counted: true},
			expected: "main",
		},
		{
			name:     "изменения и расхождение с remote",
			git:      GitStatus{IsRepo: true, Branch: "feature", Changed: 3, Ahead: 2, Behind: 1, Counted: true},
			expected: "feature ●3 ↑2 ↓1",
		},
		{
			name:     "git не успел посчитать",
			git:      GitStatus{IsRepo: true, Branch: "main"},
			expected: "main …",
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
	tests := []struct {
		name        string
		state       core.SessionState
		contextUsed float64
		expected    string
	}{
		{name: "работа", state: core.StateWorking, expected: "🔵 demo"},
		{name: "ожидание ответа", state: core.StateWaiting, expected: "🟡 demo"},
		{name: "работа завершена", state: core.StateDone, expected: "✅ demo"},
		{name: "состояние не прочиталось: нейтральный значок", state: core.StateUnknown, expected: "🔵 demo"},
		{name: "контекст на исходе", state: core.StateWorking, contextUsed: 90, expected: "🔴 demo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalTitle("/home/user/work/demo", tt.state, tt.contextUsed)
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

	parts := details(input, GitStatus{IsRepo: true, Branch: "main", Counted: true}, weekBar{label: "нед"}, time.Now())

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

	line, title := build(context.Background(), input, weekBar{label: "нед"}, time.Now(), testLogger(t))

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
	if parts := details(Input{}, GitStatus{}, weekBar{label: "нед"}, time.Now()); len(parts) != 0 {
		t.Errorf("ожидался пустой список, получено %q", parts)
	}
}

func TestSessionDirIsLaunchDir(t *testing.T) {
	input := Input{CWD: "/from/cwd"}
	input.Workspace.CurrentDir = "/from/current"
	input.Workspace.ProjectDir = "/from/project"

	// Агент ушёл в подпапку чужого проекта: имя и git остаются от каталога запуска
	if got := sessionDir(input); got != "/from/project" {
		t.Errorf("ожидалось /from/project, получено %q", got)
	}
	if got := gitDir(input); got != "/from/project" {
		t.Errorf("git вне worktree: ожидалось /from/project, получено %q", got)
	}

	input.Workspace.GitWorktree = "feature"
	if got := gitDir(input); got != "/from/current" {
		t.Errorf("git в worktree: ожидалось /from/current, получено %q", got)
	}
	if got := sessionDir(input); got != "/from/project" {
		t.Errorf("имя в worktree: ожидалось /from/project, получено %q", got)
	}

	input.Workspace.ProjectDir = ""
	if got := sessionDir(input); got != "/from/cwd" {
		t.Errorf("без project_dir: ожидалось /from/cwd, получено %q", got)
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

// Сброс 26.10 10:00Z — окно с 19.10 10:00Z
var weekResets = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func TestWeeklyBar(t *testing.T) {
	windowStart := weekResets.Add(-weekWindow)
	tests := []struct {
		name     string
		used     float64
		now      time.Time
		color    string
		expected string
	}{
		{
			// 51% за 30 часов: темп 1,7%/ч, к сбросу ~286%
			name: "кончится до сброса", used: 51, now: time.Date(2026, 10, 20, 15, 58, 0, 0, time.UTC),
			color: red, expected: "нед ▓▓▓▓▓░░░░░ 51%",
		},
		{
			// 30% за половину недели: к сбросу 60%
			name: "хватит с запасом", used: 30, now: windowStart.Add(84 * time.Hour),
			color: green, expected: "нед ▓▓▓░░░░░░░ 30%",
		},
		{
			// 40% за 72 часа: к сбросу ~93%
			name: "хватит впритык", used: 40, now: windowStart.Add(72 * time.Hour),
			color: yellow, expected: "нед ▓▓▓▓░░░░░░ 40%",
		},
		{
			name: "сразу после сброса ничего не потрачено", used: 0, now: windowStart,
			color: green, expected: "нед ░░░░░░░░░░ 0%",
		},
		{
			// Темп за нулевое время не делится: потраченное сразу — кончится
			name: "сразу после сброса уже потрачено", used: 3, now: windowStart,
			color: red, expected: "нед ░░░░░░░░░░ 3%",
		},
		{
			name: "часы машины раньше начала окна", used: 3, now: windowStart.Add(-time.Hour),
			color: red, expected: "нед ░░░░░░░░░░ 3%",
		},
		{
			// Сброс прошёл, замер старый: прогноз — то, что уже потрачено
			name: "замер после сброса", used: 40, now: weekResets.Add(2 * time.Hour),
			color: green, expected: "нед ▓▓▓▓░░░░░░ 40%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := weeklyBar(weekBar{label: "нед"}, weeklyUsage{used: tt.used, resetsAt: weekResets}, tt.now)
			if plain(got) != tt.expected {
				t.Errorf("получено %q, ожидалось %q", plain(got), tt.expected)
			}
			if !strings.Contains(got, tt.color+"▓") && !strings.Contains(got, tt.color+"░") {
				t.Errorf("полоса не того цвета: %q", got)
			}
		})
	}
}

// Границы цвета: 80% ещё зелёный, 100% ещё жёлтый
func TestClassifyPace(t *testing.T) {
	tests := []struct {
		name      string
		projected float64
		expected  pace
	}{
		{name: "с запасом", projected: 50, expected: paceSafe},
		{name: "ровно 80", projected: 80, expected: paceSafe},
		{name: "чуть выше 80", projected: 80.01, expected: paceNear},
		{name: "ровно 100", projected: 100, expected: paceNear},
		{name: "чуть выше 100", projected: 100.01, expected: paceOver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPace(tt.projected); got != tt.expected {
				t.Errorf("classifyPace(%v) = %v, ожидалось %v", tt.projected, got, tt.expected)
			}
		})
	}
}

func TestDetailsWeekly(t *testing.T) {
	now := time.Date(2026, 10, 20, 15, 58, 0, 0, time.UTC)
	used, resets := 51.0, weekResets.Unix()

	input := Input{}
	input.ContextWindow.UsedPercentage = 47
	input.RateLimits.SevenDay = &WeeklyLimit{UsedPercentage: &used, ResetsAt: &resets}
	parts := details(input, GitStatus{}, weekBar{label: "нед"}, now)
	expected := []string{"ctx ▓▓▓▓░░░░░░ 47%", "нед ▓▓▓▓▓░░░░░ 51%"}
	if len(parts) != len(expected) {
		t.Fatalf("ожидалось %q, получено %q", expected, parts)
	}
	for i, want := range expected {
		if got := plain(parts[i]); got != want {
			t.Errorf("элемент %d: ожидалось %q, получено %q", i, want, got)
		}
	}

	// Без seven_day или без resets_at блока нет совсем, нулей вместо него тоже
	for name, limit := range map[string]*WeeklyLimit{
		"нет seven_day": nil,
		"нет resets_at": {UsedPercentage: &used},
		"нет процента":  {ResetsAt: &resets},
	} {
		input.RateLimits.SevenDay = limit
		for _, part := range details(input, GitStatus{}, weekBar{label: "нед"}, now) {
			if strings.Contains(plain(part), "нед") {
				t.Errorf("%s: блок недели не должен выводиться, получено %q", name, plain(part))
			}
		}
	}
}
