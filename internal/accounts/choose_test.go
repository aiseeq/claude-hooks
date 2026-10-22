package accounts

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// account — учётка для правила выбора: вход живёт ещё loginLeft
func account(uuid string, loginLeft time.Duration, now time.Time) Account {
	raw, err := json.Marshal(map[string]any{
		"accessToken": "a", "refreshToken": "r-" + uuid,
		"expiresAt": now.Add(time.Hour).UnixMilli(), "refreshTokenExpiresAt": now.Add(loginLeft).UnixMilli(),
	})
	if err != nil {
		panic(err)
	}
	return Account{UUID: uuid, Email: uuid + "@example.com", OAuth: raw}
}

func TestChoose(t *testing.T) {
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	week := func(used float64, in time.Duration) Usage {
		return Usage{Week: Window{Used: used, ResetsAt: now.Add(in)}}
	}
	tests := []struct {
		name   string
		active string
		dead   string
		short  string
		usage  map[string]Usage
		want   string
		reason string
	}{
		{
			name: "ближайший сброс среди непустых", active: "a",
			usage: map[string]Usage{"a": week(30, 5*day), "b": week(90, 2*day), "c": week(10, 3*day)},
			want:  "b", reason: "ближе сброс",
		},
		{
			name: "98% — ещё не пуста", active: "b",
			usage: map[string]Usage{"a": week(30, 5*day), "b": week(98, 2*day), "c": week(10, 3*day)},
			want:  "b", reason: "активная",
		},
		{
			name: "активная пуста — следующая по сбросу", active: "b",
			usage: map[string]Usage{"a": week(30, 5*day), "b": week(100, 2*day), "c": week(10, 3*day)},
			want:  "c", reason: "пуста",
		},
		{
			name: "неначатая неделя — сброс через полное окно", active: "a",
			usage: map[string]Usage{"a": week(99.5, 5*day), "b": week(97, 6*day)},
			want:  "b", reason: "пуста",
		},
		{
			name: "целые 99 строки статуса — пуста", active: "a",
			usage: map[string]Usage{"a": week(99, 5*day), "b": week(40, 6*day)},
			want:  "b", reason: "пуста",
		},
		{
			name: "неизвестная неделя проигрывает известной", active: "a",
			usage: map[string]Usage{"a": week(100, day), "c": week(20, 6*day)},
			want:  "c",
		},
		{
			name: "прошедший сброс — неделя неизвестна", active: "a",
			usage: map[string]Usage{"a": week(10, 6*day), "b": week(99, -time.Hour)},
			want:  "a",
		},
		{
			name: "равные сбросы — остаётся активная", active: "c",
			usage: map[string]Usage{"a": week(10, 2*day), "b": week(10, 2*day), "c": week(10, 2*day)},
			want:  "c", reason: "активная",
		},
		{
			name: "все пусты — остаётся активная", active: "a",
			usage: map[string]Usage{"a": week(100, day), "b": week(100, 2*day), "c": week(99.5, 3*day)},
			want:  "a", reason: "все учётки пусты",
		},
		{
			name: "пять часов на исходе — пустая", active: "a",
			usage: map[string]Usage{
				"a": {Week: Window{Used: 10, ResetsAt: now.Add(day)}, FiveHour: Window{Used: 96, ResetsAt: now.Add(time.Hour)}},
				"b": week(10, 2*day), "c": week(10, 3*day),
			},
			want: "b", reason: "пять часов",
		},
		{
			name: "мёртвый вход не выбирается", active: "a", dead: "b",
			usage: map[string]Usage{"a": week(99, 5*day), "b": week(10, day), "c": week(10, 3*day)},
			want:  "c",
		},
		{
			name: "истекающий вход не выбирается", active: "a", short: "b",
			usage: map[string]Usage{"a": week(50, 5*day), "b": week(10, day), "c": week(10, 6*day)},
			want:  "a",
		},
		{
			name: "мёртвая активная — уход с неё", active: "a", dead: "a",
			usage: map[string]Usage{"a": week(10, day), "b": week(10, 2*day), "c": week(10, 3*day)},
			want:  "b", reason: "не годится",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{Active: tt.active}
			for _, id := range []string{"a", "b", "c"} {
				left := 20 * day
				if id == tt.short {
					left = 30 * time.Minute
				}
				a := account(id, left, now)
				if id == tt.dead {
					a.Dead = "тест"
				}
				s.Accounts = append(s.Accounts, a)
			}
			usage := map[string]Usage{}
			for id, u := range tt.usage {
				usage[id+"@example.com"] = u
			}
			got, reason := choose(s, usage, now)
			if got == nil || got.UUID != tt.want {
				t.Fatalf("выбрана %v (%s), ожидалась %s", got, reason, tt.want)
			}
			if !strings.Contains(reason, tt.reason) {
				t.Errorf("причина %q, ожидалось %q", reason, tt.reason)
			}
		})
	}
}

// Неизвестная неделя неактивной учётки спрашивается не чаще usageStale
func TestNeedsUsage(t *testing.T) {
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	s := &Store{Active: "a", Accounts: []Account{account("a", 20*day, now), account("b", 20*day, now), account("c", 20*day, now)}}
	s.Accounts[2].UsageCheckedAt = now.Add(-time.Minute)
	usage := map[string]Usage{"b@example.com": {Week: Window{Used: 1, ResetsAt: now.Add(-time.Second)}}}
	got := needsUsage(s, usage, now)
	if len(got) != 1 || got[0].UUID != "b" {
		t.Errorf("спросить расход: %v", fmt.Sprint(got))
	}
}

func TestShortTime(t *testing.T) {
	at := time.Date(2026, 10, 26, 11, 0, 0, 0, time.Local)
	if got := ShortTime(at); got != "пн 26.10 11:00" {
		t.Errorf("ShortTime: %q", got)
	}
}

// Имя учётки — домен почты, при общем домене — почта целиком
func TestShortName(t *testing.T) {
	s := &Store{Accounts: []Account{{Email: "al@company.example"}, {Email: "me@protonmail.com"}, {Email: "me+2@gmail.com"}}}
	for email, want := range map[string]string{"al@company.example": "company.example", "me+2@gmail.com": "gmail.com", "odd": "odd"} {
		if got := s.ShortName(email); got != want {
			t.Errorf("ShortName(%q) = %q, ожидалось %q", email, got, want)
		}
	}
	s.Accounts = append(s.Accounts, Account{Email: "other@gmail.com"})
	if got := s.ShortName("me+2@gmail.com"); got != "me+2@gmail.com" {
		t.Errorf("общий домен: %q", got)
	}
}
