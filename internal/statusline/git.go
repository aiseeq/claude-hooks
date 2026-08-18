package statusline

import (
	"context"
	"errors"
	"fmt"
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

// ReadGitStatus собирает данные о репозитории в указанном каталоге.
// Каталог вне репозитория — не ошибка (IsRepo=false); ошибка означает, что
// git ответил неожиданно, и вернувшийся статус может быть неполным
func ReadGitStatus(ctx context.Context, dir string) (GitStatus, error) {
	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	status := GitStatus{}

	branch, err := gitOutput(gitCtx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// Ненулевой код — git сам сказал, что репозитория тут нет
		if isGitFailure(err) {
			return status, nil
		}
		return status, err
	}
	status.IsRepo = true
	status.Branch = branch

	if branch == "HEAD" {
		// Отделённая HEAD: короткий хеш понятнее слова HEAD
		status.Detached = true
		hash, err := gitOutput(gitCtx, dir, "rev-parse", "--short", "HEAD")
		if err != nil {
			return status, err
		}
		status.Branch = hash
	}

	// Неотслеживаемые файлы игнорируются: в больших деревьях их обход заметно дороже
	porcelain, err := gitOutput(gitCtx, dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return status, err
	}
	if porcelain != "" {
		status.Changed = len(strings.Split(porcelain, "\n"))
	}
	status.RepoClean = status.Changed == 0

	counts, err := gitOutput(gitCtx, dir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
	if err != nil {
		// Ветка без upstream — обычное дело, считать нечего
		if isGitFailure(err) {
			return status, nil
		}
		return status, err
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return status, fmt.Errorf("unexpected rev-list output %q", counts)
	}
	if status.Behind, err = strconv.Atoi(fields[0]); err != nil {
		return status, fmt.Errorf("unexpected rev-list output %q: %w", counts, err)
	}
	if status.Ahead, err = strconv.Atoi(fields[1]); err != nil {
		return status, fmt.Errorf("unexpected rev-list output %q: %w", counts, err)
	}

	return status, nil
}

// isGitFailure отличает ненулевой код завершения git (нет репозитория, нет
// upstream) от сбоя запуска: отсутствующего бинаря или истёкшего таймаута
func isGitFailure(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

// gitOutput выполняет команду git и возвращает её вывод без крайних пробелов
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}
