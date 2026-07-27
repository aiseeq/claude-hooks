package statusline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/desktop"
)

// ANSI-последовательности. Строка статуса должна читаться боковым зрением,
// поэтому имя проекта выводится плашкой, а не обычным текстом.
//
// Цвета заданы палитрой из 256 значений, а не восьмицветной: базовые цвета
// терминала темы переопределяют, и жёлтый фон выходит грязно-коричневым
const (
	reset  = "\033[0m"
	dim    = "\033[2;37m"
	green  = "\033[38;5;77m"
	yellow = "\033[38;5;214m"
	red    = "\033[38;5;203m"
	cyan   = "\033[38;5;81m"

	badgeBlue  = "\033[1;38;5;231;48;5;33m"
	badgeAmber = "\033[1;38;5;16;48;5;214m"
	badgeRed   = "\033[1;38;5;231;48;5;196m"
)

// Пороги заполнения контекста, при которых меняется цвет полосы
const (
	contextWarnPercent     = 60
	contextCriticalPercent = 85
	contextBarSegments     = 10
)

// Input описывает данные, которые Claude Code передаёт строке статуса
type Input struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`

	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`

	Workspace struct {
		CurrentDir  string `json:"current_dir"`
		ProjectDir  string `json:"project_dir"`
		GitWorktree string `json:"git_worktree"`
	} `json:"workspace"`

	ContextWindow struct {
		UsedPercentage float64 `json:"used_percentage"`
	} `json:"context_window"`

	Cost struct {
		LinesAdded   int `json:"total_lines_added"`
		LinesRemoved int `json:"total_lines_removed"`
	} `json:"cost"`

	Effort struct {
		Level string `json:"level"`
	} `json:"effort"`
}

// Render читает данные Claude Code и возвращает строку статуса.
// Попутно обновляется заголовок окна: строка статуса видна только в активном
// окне, а по заголовку сессию видно в панели задач и в переключателе окон
func Render(ctx context.Context, stdin io.Reader) (string, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read status line input: %w", err)
	}

	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return "", fmt.Errorf("failed to parse status line input: %w", err)
	}

	line, title := build(ctx, input)
	desktop.SetTerminalTitle(title)

	return line, nil
}

// build собирает строку статуса и заголовок окна
func build(ctx context.Context, input Input) (string, string) {
	dir := workingDir(input)
	state := core.LoadSessionState(input.SessionID)
	git := ReadGitStatus(ctx, dir)

	var output strings.Builder
	output.WriteString(projectBadge(dir, state, input.ContextWindow.UsedPercentage))
	output.WriteString(" ")
	output.WriteString(dim + shortenPath(dir) + reset)

	if worktree := input.Workspace.GitWorktree; worktree != "" {
		output.WriteString(dim + " ⑂" + worktree + reset)
	}

	for _, part := range details(input, git) {
		output.WriteString(dim + " · " + reset + part)
	}

	return output.String(), terminalTitle(dir, git, state, input.ContextWindow.UsedPercentage)
}

// terminalTitle собирает заголовок окна: состояние, проект и ветка.
// Значок состояния идёт первым — по нему окно находится взглядом в панели задач
func terminalTitle(dir string, git GitStatus, state core.SessionState, contextUsed float64) string {
	marker := "🔵"
	switch {
	case contextUsed >= contextCriticalPercent:
		marker = "🔴"
	case state == core.StateWaiting:
		marker = "❓"
	case state == core.StateDone:
		marker = "✅"
	}

	title := marker + " " + strings.ToUpper(core.ProjectNameForDir(dir))
	if git.IsRepo {
		title += " · " + git.Branch
	}
	return title
}

// projectBadge рисует имя проекта плашкой, цвет которой показывает состояние.
// Переполненный контекст важнее прочего: скоро сожмётся история
func projectBadge(dir string, state core.SessionState, contextUsed float64) string {
	badge := badgeBlue
	switch {
	case contextUsed >= contextCriticalPercent:
		badge = badgeRed
	case state == core.StateWaiting:
		badge = badgeAmber
	}

	name := strings.ToUpper(core.ProjectNameForDir(dir))
	return badge + " " + name + " " + reset
}

// details собирает вторую строку из доступных сведений
func details(input Input, git GitStatus) []string {
	var parts []string

	if git.IsRepo {
		parts = append(parts, gitSummary(git))
	}

	if model := shortModel(input.Model.DisplayName); model != "" {
		if effort := input.Effort.Level; effort != "" {
			model += "·" + effort
		}
		parts = append(parts, cyan+model+reset)
	}

	if used := input.ContextWindow.UsedPercentage; used > 0 {
		parts = append(parts, contextBar(used))
	}

	if added, removed := input.Cost.LinesAdded, input.Cost.LinesRemoved; added > 0 || removed > 0 {
		parts = append(parts, fmt.Sprintf("%s+%d%s %s−%d%s", green, added, reset, red, removed, reset))
	}

	return parts
}

// shortModel убирает уточнение в скобках: «Opus 5 (1M context)» занимает
// в одной строке заметное место, а размер контекста и так виден по полосе
func shortModel(model string) string {
	if open := strings.Index(model, " ("); open > 0 {
		model = model[:open]
	}
	return strings.TrimSpace(model)
}

// gitSummary описывает ветку, незакоммиченные изменения и расхождение с remote
func gitSummary(git GitStatus) string {
	branchColor := green
	if !git.RepoClean {
		branchColor = yellow
	}

	summary := branchColor + git.Branch + reset
	if git.Detached {
		summary = yellow + "⚠ " + git.Branch + reset
	}

	if git.Changed > 0 {
		summary += fmt.Sprintf("%s ●%d%s", yellow, git.Changed, reset)
	}
	if git.Ahead > 0 {
		summary += fmt.Sprintf("%s ↑%d%s", cyan, git.Ahead, reset)
	}
	if git.Behind > 0 {
		summary += fmt.Sprintf("%s ↓%d%s", red, git.Behind, reset)
	}

	return summary
}

// contextBar рисует полосу заполнения контекстного окна
func contextBar(used float64) string {
	if used > 100 {
		used = 100
	}

	filled := int(used) * contextBarSegments / 100
	color := green
	switch {
	case used >= contextCriticalPercent:
		color = red
	case used >= contextWarnPercent:
		color = yellow
	}

	bar := strings.Repeat("▓", filled) + strings.Repeat("░", contextBarSegments-filled)
	return fmt.Sprintf("%sctx %s%s %.0f%%%s", dim, color, bar, used, reset)
}

// workingDir выбирает каталог, который описывает сессию
func workingDir(input Input) string {
	for _, candidate := range []string{input.Workspace.CurrentDir, input.CWD, input.Workspace.ProjectDir} {
		if candidate != "" {
			return candidate
		}
	}

	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// shortenPath заменяет домашний каталог тильдой
func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	if relative, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(relative, "..") {
		if relative == "." {
			return "~"
		}
		return filepath.Join("~", relative)
	}
	return path
}
