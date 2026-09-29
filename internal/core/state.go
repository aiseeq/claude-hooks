package core

import (
	"errors"
	"fmt"
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
	// StatePaused — Claude остановился, но ждёт фоновые задачи или будильник
	// и вернётся к работе сам. Для человека это всё ещё работа
	StatePaused SessionState = "paused"
)

// stateTTL определяет, как долго запись считается актуальной.
// Сессии завершаются без уведомления, поэтому старые файлы просто устаревают
const stateTTL = 24 * time.Hour

// SaveSessionState запоминает состояние сессии и попутно вычищает записи
// давно завершившихся сессий. Пустой идентификатор — событие без сессии,
// запоминать нечего
func SaveSessionState(sessionID string, state SessionState) error {
	if sessionID == "" {
		return nil
	}

	path, err := sessionStatePath(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create session state dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(state), 0o644); err != nil {
		return fmt.Errorf("cannot write session state: %w", err)
	}
	return cleanupStaleStates(filepath.Dir(path))
}

// LoadSessionState читает состояние сессии. Для неизвестной сессии
// возвращается StateWorking: раз хук ещё не отработал, работа идёт
func LoadSessionState(sessionID string) (SessionState, error) {
	if sessionID == "" {
		return StateWorking, nil
	}

	path, err := sessionStatePath(sessionID)
	if err != nil {
		return StateWorking, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StateWorking, nil
		}
		return StateWorking, fmt.Errorf("cannot read session state: %w", err)
	}

	switch SessionState(strings.TrimSpace(string(data))) {
	case StateWaiting:
		return StateWaiting, nil
	case StateDone:
		return StateDone, nil
	case StatePaused:
		return StatePaused, nil
	default:
		return StateWorking, nil
	}
}

// sessionStatePath возвращает путь к файлу состояния сессии
func sessionStatePath(sessionID string) (string, error) {
	// Имя сессии приходит извне и в путь попадать не должно
	safeID := filepath.Base(strings.TrimSpace(sessionID))
	if safeID == "" || safeID == "." || safeID == string(filepath.Separator) {
		return "", fmt.Errorf("invalid session id %q: %w", sessionID, os.ErrInvalid)
	}

	return filepath.Join(stateDir(), safeID), nil
}

// stateDir возвращает каталог для состояний сессий
func stateDir() string {
	return filepath.Join(RuntimeDir(), "sessions")
}

// RuntimeDir возвращает каталог для недолговечных файлов claude-hooks.
// Каталог времени выполнения очищается при перезагрузке — это как раз то,
// что нужно состояниям сессий и блокировкам
func RuntimeDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "claude-hooks")
}

// cleanupStaleStates удаляет записи завершившихся сессий. Записи, которые
// не удалось убрать, перечисляются в ошибке; свежие пропускаются
func cleanupStaleStates(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("cannot list session states: %w", err)
	}

	deadline := time.Now().Add(-stateTTL)
	var errs []error
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			// Запись исчезла между чтением каталога и опросом: чистить нечего
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		if info.ModTime().After(deadline) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
