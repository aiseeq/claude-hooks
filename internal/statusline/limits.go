package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WeeklyLimit — доля недельного лимита подписки, как её отдаёт Claude Code
// в rate_limits.seven_day. Поле приходит только подписчикам Pro и Max и только
// после первого ответа API в сессии. Поля — указатели: пропавшее значение
// отличается от нуля, а ноль процентов — настоящий замер
type WeeklyLimit struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

// weeklyUsage — проверенный замер недели: обе величины пришли
type weeklyUsage struct {
	used     float64
	resetsAt time.Time
}

// usage проверяет замер. Без поля seven_day, без любой из величин или с
// отрицательной долей замера нет: ни снимок, ни полоса не строятся по нулям
func (l *WeeklyLimit) usage() (weeklyUsage, bool) {
	if l == nil || l.UsedPercentage == nil || l.ResetsAt == nil || *l.ResetsAt <= 0 || *l.UsedPercentage < 0 {
		return weeklyUsage{}, false
	}
	return weeklyUsage{used: *l.UsedPercentage, resetsAt: time.Unix(*l.ResetsAt, 0)}, true
}

// limitsSnapshot — последний снимок недельного лимита на диске. Его читает
// страница Сна (sleepstats): другого машинного источника процента у Claude
// Code нет, а статусная строка получает его при каждой отрисовке
type limitsSnapshot struct {
	At       string  `json:"at"`
	SevenDay float64 `json:"sevenDayUsedPercentage"`
	ResetsAt string  `json:"sevenDayResetsAt"`
}

// LimitsFile — путь снимка. Каталог состояния, а не времени выполнения:
// снимок нужен и после перезагрузки, пока ни одна сессия не открыта
func LimitsFile() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no home directory for the limits snapshot: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-hooks", "limits.json"), nil
}

// recordWeeklyLimit пишет снимок по пути из LimitsFile
func recordWeeklyLimit(week weeklyUsage, now time.Time) error {
	path, err := LimitsFile()
	if err != nil {
		return err
	}
	return saveWeeklyLimit(path, week, now)
}

// saveWeeklyLimit записывает снимок атомарно: страница может читать файл
// в момент, когда статусная строка его пишет
func saveWeeklyLimit(path string, week weeklyUsage, now time.Time) error {
	data, err := json.Marshal(limitsSnapshot{
		At:       now.Format(time.RFC3339),
		SevenDay: week.used,
		ResetsAt: week.resetsAt.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("failed to encode the limits snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create the limits directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".limits-*.json")
	if err != nil {
		return fmt.Errorf("cannot create the limits snapshot: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("cannot write the limits snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("cannot write the limits snapshot: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("cannot replace the limits snapshot: %w", err)
	}
	return nil
}
