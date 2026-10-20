package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveWeeklyLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-hooks", "limits.json")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	limit := WeeklyLimit{UsedPercentage: 38, ResetsAt: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC).Unix()}
	if err := saveWeeklyLimit(path, limit, now); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got limitsSnapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SevenDay != 38 {
		t.Errorf("доля недели %v, ожидалось 38", got.SevenDay)
	}
	if at, _ := time.Parse(time.RFC3339, got.At); !at.Equal(now) {
		t.Errorf("время снимка %q", got.At)
	}
	if resets, _ := time.Parse(time.RFC3339, got.ResetsAt); resets.Unix() != limit.ResetsAt {
		t.Errorf("сброс %q", got.ResetsAt)
	}
	// временные файлы не остаются рядом со снимком
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("в каталоге %d файлов, ожидался один снимок", len(entries))
	}
}

// Без rate_limits (API-ключ, первый ход сессии) поле остаётся nil и снимок не пишется
func TestInputWithoutRateLimits(t *testing.T) {
	var input Input
	if err := json.Unmarshal([]byte(`{"session_id":"s"}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.RateLimits.SevenDay != nil {
		t.Error("без rate_limits ожидался nil")
	}
	if err := json.Unmarshal([]byte(`{"rate_limits":{"seven_day":{"used_percentage":38.5,"resets_at":1793000000}}}`), &input); err != nil {
		t.Fatal(err)
	}
	if l := input.RateLimits.SevenDay; l == nil || l.UsedPercentage != 38.5 || l.ResetsAt != 1793000000 {
		t.Errorf("rate_limits разобран неверно: %+v", l)
	}
}
