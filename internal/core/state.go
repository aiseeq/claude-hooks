package core

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionState описывает, чем занята сессия Claude Code.
// Строка статуса рисуется отдельным процессом и сама этого знать не может,
// поэтому состояние записывают хуки
type SessionState string

const (
	StateWorking SessionState = "working"
	StateWaiting SessionState = "waiting"
	StateDone    SessionState = "done"
)

// stateTTL определяет, как долго запись считается актуальной.
// Сессии завершаются без уведомления, поэтому старые файлы просто устаревают
const stateTTL = 24 * time.Hour

// SaveSessionState запоминает состояние сессии.
// Ошибки не возвращаются: строка статуса — не повод ломать работу хука
func SaveSessionState(sessionID string, state SessionState) {
	if sessionID == "" {
		return
	}

	path, err := sessionStatePath(sessionID)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	_ = os.WriteFile(path, []byte(state), 0o644)
	cleanupStaleStates(filepath.Dir(path))
}

// LoadSessionState читает состояние сессии. Для неизвестной сессии
// возвращается StateWorking: раз хук ещё не отработал, работа идёт
func LoadSessionState(sessionID string) SessionState {
	if sessionID == "" {
		return StateWorking
	}

	path, err := sessionStatePath(sessionID)
	if err != nil {
		return StateWorking
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return StateWorking
	}

	switch SessionState(strings.TrimSpace(string(data))) {
	case StateWaiting:
		return StateWaiting
	case StateDone:
		return StateDone
	default:
		return StateWorking
	}
}

// sessionStatePath возвращает путь к файлу состояния сессии
func sessionStatePath(sessionID string) (string, error) {
	// Имя сессии приходит извне и в путь попадать не должно
	safeID := filepath.Base(strings.TrimSpace(sessionID))
	if safeID == "" || safeID == "." || safeID == string(filepath.Separator) {
		return "", os.ErrInvalid
	}

	return filepath.Join(stateDir(), safeID), nil
}

// stateDir возвращает каталог для состояний сессий.
// Каталог времени выполнения очищается при перезагрузке — это как раз то,
// что нужно недолговечным записям
func stateDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "claude-hooks", "sessions")
}

// cleanupStaleStates удаляет записи завершившихся сессий
func cleanupStaleStates(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	deadline := time.Now().Add(-stateTTL)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.ModTime().After(deadline) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}
