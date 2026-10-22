package questioncheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Until — конец эксперимента: после 30.10 23:59 по местному хук ничего не
// делает. Рамка пользователя 22.10 17:42 CEST (сессия lead): «Проверка
// вопросов моделью, неделю, в моей сессии и у work-admin?» — «да. Главное
// потом не забыть сделать вывод и принять окончательное решение»
var Until = time.Date(untilYear, time.October, 31, 0, 0, 0, 0, time.Local)

// untilYear — год конца эксперимента
const untilYear = 2026

// Target — сессия, в которой проверка работает: имя сессии Claude Code и
// каталог, из которого она запущена (по нему назван каталог проекта
// транскрипта)
type Target struct {
	Name string
	Dir  string
}

// DefaultTargets — lead (~) и work-admin (~/work); остальным сессиям хук —
// no-op без вызова модели
func DefaultTargets(home string) []Target {
	return []Target{
		{Name: "lead", Dir: home},
		{Name: "work-admin", Dir: filepath.Join(home, "work")},
	}
}

// sessionRecord — запись живой сессии ~/.claude/sessions/<pid>.json, которую
// Claude Code ведёт для каждого процесса: имя, заданное /rename или --name
type sessionRecord struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	CWD       string `json:"cwd"`
}

// SessionName читает имя сессии процесса claude из ~/.claude/sessions/<pid>.json.
// Pid хук получает в CLAUDE_PID. Пустой pid — сессия без имени: признак
// не прочитан, и это ошибка — решает вызывающий
func SessionName(claudeDir, pid string) (string, error) {
	pid = strings.TrimSpace(pid)
	if pid == "" {
		return "", fmt.Errorf("CLAUDE_PID is empty")
	}
	// Значение пришло из окружения: в путь оно попадает только именем файла
	path := filepath.Join(claudeDir, "sessions", filepath.Base(pid)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read session record: %w", err)
	}
	var record sessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return "", fmt.Errorf("parse session record %s: %w", path, err)
	}
	return record.Name, nil
}

// MatchTarget находит цель по имени сессии и сверяет её с каталогом проекта
// транскрипта: имя сессии одно, а каталог проекта подтверждает, что это та же
// работа (lead в ~, work-admin в ~/work). Расхождение — не цель
func MatchTarget(targets []Target, name, transcriptPath string) (Target, bool) {
	project := filepath.Base(filepath.Dir(transcriptPath))
	for _, target := range targets {
		if target.Name == name && project == ProjectDirName(target.Dir) {
			return target, true
		}
	}
	return Target{}, false
}

// ProjectDirName — имя каталога проекта в ~/.claude/projects: путь, в
// котором Claude Code заменяет «/» и «.» на «-»
func ProjectDirName(dir string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(filepath.Clean(dir))
}
