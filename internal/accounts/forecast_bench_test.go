package accounts

import (
	"testing"
	"time"
)

func BenchmarkForecast(b *testing.B) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &Store{Active: "a", Accounts: []Account{account("a", 20*day, now), account("b", 20*day, now), account("c", 20*day, now)}}
	usage := map[string]Usage{"a@example.com": {Week: Window{Used: 96, ResetsAt: now.Add(4 * day)}}}
	for i := 0; i < b.N; i++ {
		s.Forecast(usage, 0.6, now)
	}
}
