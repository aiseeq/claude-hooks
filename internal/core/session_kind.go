package core

import (
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
// нет и у интерактивной сессии.
func PrintModeSession() bool {
	return printModeCmdline(os.Getenv("CLAUDE_PID"))
}

// printModeCmdline читает аргументы процесса через /proc (Linux).
func printModeCmdline(pid string) bool {
	if pid == "" {
		return false
	}
	// Имя пришло из окружения: в путь оно попадать не должно
	safe := filepath.Base(strings.TrimSpace(pid))
	raw, err := os.ReadFile(filepath.Join("/proc", safe, "cmdline"))
	if err != nil {
		return false
	}
	for _, arg := range strings.Split(string(raw), "\x00") {
		if arg == "-p" || arg == "--print" {
			return true
		}
	}
	return false
}
