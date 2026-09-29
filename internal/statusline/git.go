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

// slowStatusThreshold — статус дольше этого значит, что индекс устарел: git
// заново читает файлы, чьи отметки времени разошлись с индексом (checkout,
// сборка, переписавшая файлы). В обычном состоянии большой репозиторий
// отвечает за десятки миллисекунд
const slowStatusThreshold = 150 * time.Millisecond

// GitStatus описывает состояние репозитория
type GitStatus struct {
	Branch   string
	Changed  int
	Ahead    int
	Behind   int
	IsRepo   bool
	Detached bool
	// Counted — изменения и расхождение с remote посчитаны. Без этого Changed
	// и Ahead/Behind нулевые не потому, что дерево чистое, а потому, что git
	// не успел ответить
	Counted bool
	// Slow — статус не уложился в slowStatusThreshold или в таймаут: индекс
	// пора обновить, см. StartIndexRefresh
	Slow bool
}

// ReadGitStatus собирает данные о репозитории в указанном каталоге.
// Каталог вне репозитория — не ошибка (IsRepo=false); ошибка означает, что
// git ответил неожиданно или не успел, и вернувшийся статус может быть неполным
func ReadGitStatus(ctx context.Context, dir string) (GitStatus, error) {
	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	status := GitStatus{}

	// Имя ветки отдельным быстрым вызовом: оно нужно и тогда, когда подсчёт
	// изменений не уложится в таймаут
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

	// Один вызов даёт и изменения, и расхождение с upstream. Индекс не
	// пишется (--no-optional-locks): строка статуса не должна отнимать
	// index.lock у git-команд человека и Claude. Неотслеживаемые файлы
	// пропускаются: в больших деревьях их обход заметно дороже
	start := time.Now()
	porcelain, err := gitOutput(gitCtx, dir, "--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=no")
	if err != nil {
		status.Slow = gitCtx.Err() != nil
		return status, err
	}
	status.Slow = time.Since(start) > slowStatusThreshold

	if status.Changed, status.Ahead, status.Behind, err = parseStatusV2(porcelain); err != nil {
		return status, err
	}
	status.Counted = true
	return status, nil
}

// parseStatusV2 разбирает `git status --porcelain=v2 --branch`: строки
// заголовка начинаются с «#», остальные — изменённые записи. Расхождение с
// upstream приходит строкой «# branch.ab +A -B»; у ветки без upstream её нет
func parseStatusV2(output string) (changed, ahead, behind int, err error) {
	if output == "" {
		return 0, 0, 0, nil
	}

	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "#") {
			changed++
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "branch.ab" {
			continue
		}
		if len(fields) != 4 {
			return 0, 0, 0, fmt.Errorf("unexpected branch.ab line %q", line)
		}
		if ahead, err = strconv.Atoi(strings.TrimPrefix(fields[2], "+")); err != nil {
			return 0, 0, 0, fmt.Errorf("unexpected branch.ab line %q: %w", line, err)
		}
		if behind, err = strconv.Atoi(strings.TrimPrefix(fields[3], "-")); err != nil {
			return 0, 0, 0, fmt.Errorf("unexpected branch.ab line %q: %w", line, err)
		}
	}

	return changed, ahead, behind, nil
}

// isGitFailure отличает ненулевой код завершения git (нет репозитория) от
// сбоя запуска: отсутствующего бинаря или истёкшего таймаута
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
