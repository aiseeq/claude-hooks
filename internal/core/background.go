package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// abandonedTaskAge — сколько фоновая задача может висеть без отчёта, прежде
// чем Claude перестанет её «ждать». Намеренно вечная задача (dev-сервер,
// watcher) иначе глушила бы уведомления до конца сессии; честно-долгая
// задача сверх срока в худшем случае даёт один лишний звонок
const abandonedTaskAge = 2 * time.Hour

// tasksFileSuffix отличает файл учёта задач от файла состояния той же сессии
const tasksFileSuffix = ".tasks"

// ActiveBackgroundTasks отбирает задачи, которых Claude ещё ждёт: всё из
// списка Claude Code, кроме висящих дольше abandonedTaskAge. Время первого
// появления задачи хранится рядом с состоянием сессии, исчезнувшие из списка
// задачи забываются. При сбое учёта возвращается весь список вместе с
// ошибкой: Claude Code сам сказал, что задачи живы
func ActiveBackgroundTasks(sessionID string, tasks []BackgroundTask, now time.Time) ([]BackgroundTask, error) {
	if sessionID == "" {
		return tasks, nil
	}

	statePath, err := sessionStatePath(sessionID)
	if err != nil {
		return tasks, err
	}
	path := statePath + tasksFileSuffix

	if len(tasks) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return tasks, fmt.Errorf("cannot forget background tasks: %w", err)
		}
		return tasks, nil
	}

	seen, err := loadFirstSeen(path)
	if err != nil {
		return tasks, err
	}

	current := make(map[string]time.Time, len(tasks))
	active := make([]BackgroundTask, 0, len(tasks))
	for _, task := range tasks {
		first, known := seen[task.ID]
		if !known {
			first = now
		}
		current[task.ID] = first
		if now.Sub(first) < abandonedTaskAge {
			active = append(active, task)
		}
	}

	if err := saveFirstSeen(path, current); err != nil {
		return tasks, err
	}
	return active, nil
}

// loadFirstSeen читает время первого появления задач. Отсутствующий файл —
// задач ещё не видели
func loadFirstSeen(path string) (map[string]time.Time, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]time.Time{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read background tasks: %w", err)
	}

	seen := map[string]time.Time{}
	if err := json.Unmarshal(data, &seen); err != nil {
		return nil, fmt.Errorf("cannot parse background tasks %s: %w", path, err)
	}
	return seen, nil
}

// saveFirstSeen запоминает время первого появления задач
func saveFirstSeen(path string, seen map[string]time.Time) error {
	data, err := json.Marshal(seen)
	if err != nil {
		return fmt.Errorf("cannot encode background tasks: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create session state dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("cannot write background tasks: %w", err)
	}
	return nil
}
