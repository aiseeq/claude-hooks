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
	week := weeklyUsage{used: 38, resetsAt: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)}
	if err := saveWeeklyLimit(path, week, now); err != nil {
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
	if resets, _ := time.Parse(time.RFC3339, got.ResetsAt); !resets.Equal(week.resetsAt) {
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
	week, ok := input.RateLimits.SevenDay.usage()
	if !ok || week.used != 38.5 || week.resetsAt.Unix() != 1793000000 {
		t.Errorf("rate_limits разобран неверно: %+v", week)
	}
}

// Неполный замер не превращается в нули: без любой величины замера нет,
// а ноль процентов при известном сбросе — настоящий замер
func TestWeeklyUsageRequiresBothValues(t *testing.T) {
	tests := []struct {
		name string
		json string
		ok   bool
	}{
		{name: "нет rate_limits", json: `{}`, ok: false},
		{name: "нет seven_day", json: `{"rate_limits":{}}`, ok: false},
		{name: "нет resets_at", json: `{"rate_limits":{"seven_day":{"used_percentage":51}}}`, ok: false},
		{name: "нет used_percentage", json: `{"rate_limits":{"seven_day":{"resets_at":1793000000}}}`, ok: false},
		{name: "resets_at ноль", json: `{"rate_limits":{"seven_day":{"used_percentage":51,"resets_at":0}}}`, ok: false},
		{name: "отрицательная доля", json: `{"rate_limits":{"seven_day":{"used_percentage":-1,"resets_at":1793000000}}}`, ok: false},
		{name: "ноль процентов", json: `{"rate_limits":{"seven_day":{"used_percentage":0,"resets_at":1793000000}}}`, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input Input
			if err := json.Unmarshal([]byte(tt.json), &input); err != nil {
				t.Fatal(err)
			}
			if _, ok := input.RateLimits.SevenDay.usage(); ok != tt.ok {
				t.Errorf("usage() ok = %v, ожидалось %v", ok, tt.ok)
			}
		})
	}
}
