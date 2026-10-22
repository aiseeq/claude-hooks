package statusline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Темп — сумма шагов за окно на часы истории. Неделя, начатая до журнала,
// даёт только базу; начатая после — считается от нуля
func TestPoolRate(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(4 * 24 * time.Hour)             // неделя началась 3 дня назад, до журнала
	fresh := now.Add(7*24*time.Hour - 2*time.Hour) // началась 2 ч назад
	steps := []limitStep{
		{At: now.Add(-20 * time.Hour), ResetsAt: old, SevenDay: 60},
		{At: now.Add(-10 * time.Hour), ResetsAt: old, SevenDay: 70},
		{At: now.Add(-time.Hour), ResetsAt: old, SevenDay: 80},
		{At: now.Add(-30 * time.Minute), ResetsAt: fresh, SevenDay: 4},
	}
	rate, span, err := poolRate(steps, now)
	if err != nil {
		t.Fatal(err)
	}
	if span != 20*time.Hour || rate != 24.0/20 {
		t.Errorf("темп %v%%/ч за %v, ожидалось 1.2%%/ч за 20 ч", rate, span)
	}
	if _, _, err := poolRate(steps[2:], now); !errors.Is(err, errShortHistory) {
		t.Errorf("короткая история: %v", err)
	}
}

// Хвост большого журнала читается с первой целой строки
func TestReadRecentStepsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits-history.jsonl")
	line := `{"at":"2026-10-22T10:00:00Z","sevenDayResetsAt":"2026-10-26T10:00:00Z","sevenDayUsedPercentage":5}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, historyTail/len(line)+50)), 0o644); err != nil {
		t.Fatal(err)
	}
	steps, err := readRecentSteps(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(steps); n == 0 || n > historyTail/len(line) {
		t.Errorf("шагов в хвосте: %d", n)
	}
}
