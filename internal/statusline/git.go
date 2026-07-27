package statusline

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gitTimeout ограничивает опрос репозитория: строка статуса рисуется часто,
// и подвисший git не должен задерживать вывод
const gitTimeout = 300 * time.Millisecond

// GitStatus описывает состояние репозитория
type GitStatus struct {
	Branch    string
	Changed   int
	Ahead     int
	Behind    int
	IsRepo    bool
	Detached  bool
	Worktree  string
	RepoClean bool
}

// ReadGitStatus собирает данные о репозитории в указанном каталоге
func ReadGitStatus(ctx context.Context, dir string) GitStatus {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	status := GitStatus{}

	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return status
	}
	status.IsRepo = true
	status.Branch = branch

	if branch == "HEAD" {
		// Отделённая HEAD: короткий хеш понятнее слова HEAD
		status.Detached = true
		if hash, err := gitOutput(ctx, dir, "rev-parse", "--short", "HEAD"); err == nil {
			status.Branch = hash
		}
	}

	// Неотслеживаемые файлы игнорируются: в больших деревьях их обход заметно дороже
	if porcelain, err := gitOutput(ctx, dir, "status", "--porcelain", "--untracked-files=no"); err == nil {
		if porcelain != "" {
			status.Changed = len(strings.Split(porcelain, "\n"))
		}
		status.RepoClean = status.Changed == 0
	}

	if counts, err := gitOutput(ctx, dir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD"); err == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			status.Behind, _ = strconv.Atoi(fields[0])
			status.Ahead, _ = strconv.Atoi(fields[1])
		}
	}

	return status
}

// gitOutput выполняет команду git и возвращает её вывод без крайних пробелов
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
