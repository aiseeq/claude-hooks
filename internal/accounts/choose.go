package accounts

import (
	"fmt"
	"time"
)

// Пороги «пустой» учётки. Неделя: на 100% запросы отклоняются, а строка
// статуса долю из ответа 429 не получает (последняя удачная доля так и
// висит), поэтому порог должен сработать на последней удачной доле. Строка
// статуса отдаёт долю целым числом: на пороге 99.5 переключение срабатывало
// только на целом 100, когда запросы уже отклонялись (22.10: 13:52:52 доля
// 99, 14:00:11 — 100 и 429), порог 99 срабатывает на целом 99 (пользователь
// 22.10: «Сместим на пол-процента планку?»). Пять часов: окно меньше, запрос
// в нём весомее — 95%
const (
	WeekEmptyPercent     = 99.0
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
	return loginOf(a).usableAt(now)
}

// login — что о входе учётки нужно правилу выбора: прогноз пула разбирает
// токены один раз, а не на каждом шаге
type login struct {
	alive bool
	// broken — токены учётки не разобрать
	broken error
	// ends — конец цепочки токенов, ноль — неизвестен
	ends time.Time
}

func loginOf(a *Account) login {
	if a.Dead != "" {
		return login{}
	}
	o, err := a.oauth()
	if err != nil {
		// Токены не разобрать: вход не годится, причина — при сверке хранилища
		return login{broken: err}
	}
	if o.cleared() {
		return login{}
	}
	l := login{alive: true}
	if o.family() > 0 {
		l.ends = time.UnixMilli(o.family())
	}
	return l
}

// usableAt — вход жив и не истекает в ближайший час после now
func (l login) usableAt(now time.Time) bool {
	return l.broken == nil && l.alive && (l.ends.IsZero() || l.ends.After(now.Add(loginMargin)))
}

// states собирает состояние живых учёток в порядке хранилища
func states(s *Store, usage map[string]Usage, now time.Time) []state {
	return statesWith(s, usage, now, func(a *Account) bool { return eligible(a, now) })
}

// statesWith — states с проверкой входа от вызывающего
func statesWith(s *Store, usage map[string]Usage, now time.Time, usable func(*Account) bool) []state {
	var out []state
	for i := range s.Accounts {
		a := &s.Accounts[i]
		if !usable(a) {
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
	best, all := pick(s, usage, now)
	if best == nil {
		if active := s.byUUID(s.Active); active != nil {
			return active, "все учётки пусты"
		}
		return nil, "нет живых учёток"
	}
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

// pick — правило выбора без причины: из непустых живых учёток — с ближайшим
// сбросом недели, равные сбросы — активная. nil — непустых нет. Вторым —
// все живые учётки. Его же прогоняет прогноз пула
func pick(s *Store, usage map[string]Usage, now time.Time) (*state, []state) {
	return pickFrom(s, states(s, usage, now))
}

// pickFrom — pick по уже собранным состояниям
func pickFrom(s *Store, all []state) (*state, []state) {
	var best *state
	for i := range all {
		st := &all[i]
		if st.empty != "" {
			continue
		}
		if best == nil || st.resets.Before(best.resets) ||
			st.resets.Equal(best.resets) && st.account.UUID == s.Active && best.account.UUID != s.Active {
			best = st
		}
	}
	return best, all
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
