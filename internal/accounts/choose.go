package accounts

import (
	"fmt"
	"sort"
	"time"
)

// Пороги «пустой» учётки. Неделя: на 100% запросы отклоняются, а строка
// статуса долю из ответа 429 не получает (последняя удачная доля так и
// висит), поэтому порог должен сработать на последней удачной доле. Строка
// статуса отдаёт долю целым числом, 100% в ней — около 99.5% на деле:
// неделя используется до конца (пользователь 22.10: «давай до 100%
// исплоьзовать… вот в этот момент можно переключать»), порог 99.5 ловит и
// целые 100 строки статуса, и дробную долю API. Пять часов: окно меньше, запрос в нём
// весомее — 95%
const (
	WeekEmptyPercent     = 99.5
	FiveHourEmptyPercent = 95
)

// weekWindow — недельное окно: неначатая неделя кончится не раньше
const weekWindow = 168 * time.Hour

// loginMargin — вход, чья цепочка токенов истекает раньше, не выбирается:
// переключение на него оборвётся сразу после
const loginMargin = time.Hour

// Usage — расход учётки по снимку строки статуса (по почте учётки)
type Usage struct {
	Email    string
	Week     Window
	FiveHour Window
}

// state — что ротация знает об учётке сейчас
type state struct {
	account *Account
	week    Window
	// weekKnown — сброс недели известен и ещё не прошёл
	weekKnown bool
	// resets — сброс для сравнения: неизвестная неделя считается
	// неначатой и кончится через полное окно
	resets time.Time
	empty  string
}

// eligible — вход учётки жив и не истекает в ближайший час
func eligible(a *Account, now time.Time) bool {
	if a.Dead != "" {
		return false
	}
	o, err := a.oauth()
	if err != nil || o.cleared() {
		return false
	}
	return o.family() == 0 || time.UnixMilli(o.family()).After(now.Add(loginMargin))
}

// states собирает состояние живых учёток в порядке хранилища
func states(s *Store, usage map[string]Usage, now time.Time) []state {
	var out []state
	for i := range s.Accounts {
		a := &s.Accounts[i]
		if !eligible(a, now) {
			continue
		}
		u := usage[a.Email]
		st := state{account: a, resets: now.Add(weekWindow)}
		if u.Week.ResetsAt.After(now) {
			st.week, st.weekKnown, st.resets = u.Week, true, u.Week.ResetsAt
		}
		switch {
		case st.weekKnown && st.week.Used >= WeekEmptyPercent:
			st.empty = fmt.Sprintf("неделя %.0f%%", st.week.Used)
		case u.FiveHour.ResetsAt.After(now) && u.FiveHour.Used >= FiveHourEmptyPercent:
			st.empty = fmt.Sprintf("пять часов %.0f%% до %s", u.FiveHour.Used, ShortTime(u.FiveHour.ResetsAt))
		}
		out = append(out, st)
	}
	return out
}

// choose выбирает учётку по правилу пользователя (22.10): из непустых —
// с ближайшим сбросом недели, чтобы её остаток не сгорел. Равные сбросы —
// остаётся активная. Все пусты — остаётся активная: лучше нет.
// Возвращает выбранную учётку (nil — выбрать не из чего) и причину
func choose(s *Store, usage map[string]Usage, now time.Time) (*Account, string) {
	all := states(s, usage, now)
	var candidates []state
	for _, st := range all {
		if st.empty == "" {
			candidates = append(candidates, st)
		}
	}
	if len(candidates) == 0 {
		if active := s.byUUID(s.Active); active != nil {
			return active, "все учётки пусты"
		}
		return nil, "нет живых учёток"
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.resets.Equal(b.resets) {
			return a.resets.Before(b.resets)
		}
		return a.account.UUID == s.Active && b.account.UUID != s.Active
	})
	best := candidates[0]
	if best.account.UUID == s.Active {
		return best.account, "активная — ближайший сброс"
	}
	reason := "неделя не начата"
	if best.weekKnown {
		reason = fmt.Sprintf("сброс %s, неделя %.0f%%", ShortTime(best.resets), best.week.Used)
	}
	if active := s.byUUID(s.Active); active != nil {
		for _, st := range all {
			if st.account == active && st.empty != "" {
				return best.account, fmt.Sprintf("%s пуста (%s); %s", active.Email, st.empty, reason)
			}
		}
		if !eligible(active, now) {
			return best.account, fmt.Sprintf("вход %s не годится; %s", active.Email, reason)
		}
	}
	return best.account, "ближе сброс: " + reason
}

// usageStale — расход неактивной учётки с неизвестной неделей не спрашивался
// дольше этого: неделя могла начаться или сброситься
const usageStale = 30 * time.Minute

// needsUsage — неактивные живые учётки, чья неделя неизвестна: без их доли
// и сброса правило выбора слепо
func needsUsage(s *Store, usage map[string]Usage, now time.Time) []*Account {
	var out []*Account
	for _, st := range states(s, usage, now) {
		a := st.account
		if a.UUID == s.Active || st.weekKnown || now.Sub(a.UsageCheckedAt) < usageStale {
			continue
		}
		out = append(out, a)
	}
	return out
}

// weekdays — короткие имена дней недели
var weekdays = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// ShortTime пишет время местным: «пн 26.10 11:00»
func ShortTime(at time.Time) string {
	at = at.Local()
	return weekdays[at.Weekday()] + " " + at.Format("02.01 15:04")
}
