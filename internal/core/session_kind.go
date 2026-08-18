package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrintModeSession сообщает, что хук вызвала неинтерактивная сессия `claude -p`.
//
// Такие сессии порождает автоматика: пост-пасс hedwig гоняет через `claude -p`
// арбитра расшифровки партиями, и каждая партия — отдельная сессия, которая
// на выходе дёргает Stop. Человек на неё не смотрит и звонка не ждёт: для него
// это середина работы, которую он запустил одной командой.
//
// Режим определяется по командной строке процесса claude — его pid Claude Code
// кладёт в CLAUDE_PID. Отсутствие терминала признаком не годится: в IDE его
// нет и у интерактивной сессии. Ошибка означает, что командную строку прочитать
// не удалось и режим неизвестен
func PrintModeSession() (bool, error) {
	return printModeCmdline(os.Getenv("CLAUDE_PID"))
}

// printModeCmdline читает аргументы процесса через /proc (Linux).
// Пустой pid — сессия без CLAUDE_PID, считается интерактивной
func printModeCmdline(pid string) (bool, error) {
	if pid == "" {
		return false, nil
	}
	// Имя пришло из окружения: в путь оно попадать не должно
	safe := filepath.Base(strings.TrimSpace(pid))
	raw, err := os.ReadFile(filepath.Join("/proc", safe, "cmdline"))
	if err != nil {
		return false, fmt.Errorf("cannot read cmdline of CLAUDE_PID %q: %w", pid, err)
	}
	for _, arg := range strings.Split(string(raw), "\x00") {
		if arg == "-p" || arg == "--print" {
			return true, nil
		}
	}
	return false, nil
}
