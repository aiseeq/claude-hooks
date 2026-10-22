package accounts

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// Три учётки: активная почти пуста, личная с поздним сбросом, третья не
// начата. При 3% в час пул кончается: 3.5 ч активной, по 33 ч личной и
// третьей; первым вернётся сброс активной
func TestForecastPoolRunsDry(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &Store{Active: "a", Accounts: []Account{account("a", 20*day, now), account("b", 20*day, now), account("c", 20*day, now)}}
	usage := map[string]Usage{
		"a@example.com": {Week: Window{Used: 96, ResetsAt: now.Add(4 * day)}},
		"b@example.com": {Week: Window{Used: 0, ResetsAt: now.Add(6 * day)}},
	}
	f := s.Forecast(usage, 3, now)
	if f.Pace != PaceOver {
		t.Fatalf("прогноз %v, ожидался over", f.Pace)
	}
	// 3.5/3 + 99.5/3 + 99.5/3 ≈ 67.5 ч
	if want := now.Add(67*time.Hour + 30*time.Minute); f.DryAt.Sub(want).Abs() > 20*time.Minute {
		t.Errorf("пул пуст %v, ожидалось около %v", f.DryAt, want)
	}
	if f.RefillEmail != "a@example.com" || !f.RefillAt.Equal(now.Add(4*day)) {
		t.Errorf("первый сброс: %s %v", f.RefillEmail, f.RefillAt)
	}
	var order []string
	for _, sp := range f.Plan {
		order = append(order, sp.Email)
	}
	if want := []string{"a@example.com", "b@example.com", "c@example.com", "", "a@example.com"}; len(order) < len(want) || !slices.Equal(order[:len(want)], want) {
		t.Errorf("план: %v, ожидалось начало %v", order, want)
	}
}

// Одна неначатая учётка: 0.5% в час — 84% за неделю, хватает; с запасом в
// четверть — 105%, не хватает: впритык
func TestForecastPoolNear(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &Store{Active: "a", Accounts: []Account{account("a", 20*day, now)}}
	if f := s.Forecast(nil, 0.5, now); f.Pace != PaceNear || !f.DryAt.IsZero() {
		t.Errorf("0.5%%/ч: %v, пуст %v", f.Pace, f.DryAt)
	}
	if f := s.Forecast(nil, 0.3, now); f.Pace != PaceSafe {
		t.Errorf("0.3%%/ч: %v", f.Pace)
	}
}

// Разные тарифы помечаются: доли недели Max 5x и 20x несопоставимы
func TestForecastMixedTiers(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tiered := func(uuid, tier string) Account {
		a := account(uuid, 20*day, now)
		var m map[string]any
		if err := json.Unmarshal(a.OAuth, &m); err != nil {
			t.Fatal(err)
		}
		m["rateLimitTier"] = tier
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		a.OAuth = raw
		return a
	}
	s := &Store{Active: "a", Accounts: []Account{tiered("a", "default_claude_max_20x"), tiered("b", "default_claude_max_20x")}}
	if s.Forecast(nil, 1, now).MixedTiers {
		t.Error("одинаковые тарифы помечены разными")
	}
	s.Accounts = append(s.Accounts, tiered("c", "default_claude_max_5x"))
	if !s.Forecast(nil, 1, now).MixedTiers {
		t.Error("разные тарифы не помечены")
	}
}
