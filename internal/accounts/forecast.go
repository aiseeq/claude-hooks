package accounts

import "time"

// Прогноз пула (пользователь 22.10: «с 3-мя подписками можно пересчитать
// темп?»): нынешний темп расхода прогоняется вперёд по правилу ротации —
// тратится учётка, которую выбрал бы pick, пустая сменяется следующей, у
// каждой учётки в её сброс доля обнуляется, неначатая неделя начинается с
// первым расходом. Пятичасовые окна в прогнозе не участвуют: они
// сбрасываются через часы и на недельный запас не влияют
const (
	// PoolHorizon — горизонт прогноза: за неделю каждая учётка сбросится
	// хотя бы раз, дальше прогноз повторяет себя
	PoolHorizon = weekWindow
	// poolStep — шаг прогона: 10 минут — 1008 шагов на неделю, ошибка в
	// моменте опустения — не больше шага
	poolStep = 10 * time.Minute
	// poolMargin — запас: пул, который кончился бы при темпе на четверть
	// выше, — впритык. То же, что прежний порог одной учётки: прогноз на
	// сброс больше 80% (1/0.8 = 1.25)
	poolMargin = 1.25
)

// Pace — прогноз пула
type Pace int

const (
	// PaceSafe — пул не кончится за горизонт и при темпе с запасом
	PaceSafe Pace = iota
	// PaceNear — при нынешнем темпе хватит, при темпе с запасом — нет
	PaceNear
	// PaceOver — будет момент, когда все учётки пусты
	PaceOver
)

// String — имя для JSON и журнала
func (p Pace) String() string {
	switch p {
	case PaceNear:
		return "near"
	case PaceOver:
		return "over"
	default:
		return "safe"
	}
}

// Spell — отрезок прогноза: кто тратит. Email пуст — все учётки пусты
type Spell struct {
	Email string    `json:"email"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

// Point — доля недели учётки в момент
type Point struct {
	At   time.Time `json:"at"`
	Used float64   `json:"used"`
}

// Track — путь доли учётки в прогнозе: точки изломов (начало и конец
// расхода, сброс), между ними доля меняется линейно; Resets — сбросы недели
// на горизонте, включая недели, которые прогноз начал
type Track struct {
	Email  string      `json:"email"`
	Points []Point     `json:"points"`
	Resets []time.Time `json:"resets"`
}

// Forecast — прогноз пула при темпе Rate (% недели одной учётки в час)
type Forecast struct {
	Rate float64 `json:"ratePerHour"`
	Pace Pace    `json:"-"`
	// DryAt — первый момент, когда все учётки пусты; ноль — за горизонт нет
	DryAt time.Time `json:"dryAt,omitzero"`
	// RefillAt и RefillEmail — ближайший после DryAt сброс недели и его учётка
	RefillAt    time.Time `json:"refillAt,omitzero"`
	RefillEmail string    `json:"refillEmail,omitempty"`
	Plan        []Spell   `json:"plan"`
	Tracks      []Track   `json:"tracks"`
	// MixedTiers — тарифы учёток разные (Max 5x и 20x): доли недели
	// несопоставимы, а прогноз считает их равными (22.10 у всех трёх 20x)
	MixedTiers bool `json:"mixedTiers"`
}

// Forecast прогоняет темп rate вперёд по правилу ротации от расхода usage
func (s *Store) Forecast(usage map[string]Usage, rate float64, now time.Time) Forecast {
	f := Forecast{Rate: rate, MixedTiers: s.mixedTiers()}
	run := s.simulate(usage, rate, now)
	f.DryAt, f.RefillAt, f.RefillEmail, f.Plan, f.Tracks = run.dryAt, run.refillAt, run.refillEmail, run.plan, run.tracks
	switch {
	case !f.DryAt.IsZero():
		f.Pace = PaceOver
	case !s.simulate(usage, rate*poolMargin, now).dryAt.IsZero():
		f.Pace = PaceNear
	}
	return f
}

// poolRun — итог одного прогона
type poolRun struct {
	dryAt, refillAt time.Time
	refillEmail     string
	plan            []Spell
	tracks          []Track
}

// simulate — один прогон пула шагами poolStep
func (s *Store) simulate(usage map[string]Usage, rate float64, now time.Time) poolRun {
	// Копия: прогон меняет активную, хранилище — нет
	sim := &Store{Active: s.Active, Accounts: s.Accounts}
	logins := make(map[string]login, len(s.Accounts))
	for i := range s.Accounts {
		logins[s.Accounts[i].UUID] = loginOf(&s.Accounts[i])
	}
	weeks := map[string]Window{}
	for email, u := range usage {
		if u.Week.ResetsAt.After(now) {
			weeks[email] = u.Week
		}
	}
	var run poolRun
	tracks := make(map[string]*Track, len(s.Accounts))
	for i := range s.Accounts {
		a := &s.Accounts[i]
		if !logins[a.UUID].usableAt(now) {
			continue
		}
		tr := &Track{Email: a.Email, Points: []Point{{At: now, Used: weeks[a.Email].Used}}}
		if w, ok := weeks[a.Email]; ok {
			tr.Resets = append(tr.Resets, w.ResetsAt)
		}
		tracks[a.Email] = tr
	}
	mark := func(email string, at time.Time, used float64) {
		if tr := tracks[email]; tr != nil {
			tr.Points = append(tr.Points, Point{At: at, Used: used})
		}
	}
	spender := ""
	end := now.Add(PoolHorizon)
	for t := now; t.Before(end); t = t.Add(poolStep) {
		cur := make(map[string]Usage, len(weeks))
		for email, w := range weeks {
			if !w.ResetsAt.After(t) {
				mark(email, w.ResetsAt, w.Used)
				mark(email, w.ResetsAt, 0)
				delete(weeks, email)
				continue
			}
			cur[email] = Usage{Email: email, Week: w}
		}
		at := t
		best, all := pickFrom(sim, statesWith(sim, cur, t, func(a *Account) bool { return logins[a.UUID].usableAt(at) }))
		if best == nil {
			if spender != "" {
				mark(spender, t, weeks[spender].Used)
				spender = ""
			}
			if run.dryAt.IsZero() {
				run.dryAt = t
				run.refillAt, run.refillEmail = nextReset(all)
			}
			run.plan = extend(run.plan, "", t)
			continue
		}
		a := best.account
		sim.Active = a.UUID
		w, ok := weeks[a.Email]
		if !ok {
			w = Window{ResetsAt: t.Add(weekWindow)}
			if tr := tracks[a.Email]; tr != nil {
				tr.Resets = append(tr.Resets, w.ResetsAt)
			}
		}
		if spender != a.Email {
			if spender != "" {
				mark(spender, t, weeks[spender].Used)
			}
			mark(a.Email, t, w.Used)
			spender = a.Email
		}
		w.Used += rate * poolStep.Hours()
		weeks[a.Email] = w
		run.plan = extend(run.plan, a.Email, t)
	}
	for i := range s.Accounts {
		if tr := tracks[s.Accounts[i].Email]; tr != nil {
			tr.Points = append(tr.Points, Point{At: end, Used: weeks[tr.Email].Used})
			run.tracks = append(run.tracks, *tr)
		}
	}
	return run
}

// nextReset — ближайший сброс недели среди живых учёток
func nextReset(all []state) (time.Time, string) {
	var at time.Time
	email := ""
	for _, st := range all {
		if st.weekKnown && (at.IsZero() || st.resets.Before(at)) {
			at, email = st.resets, st.account.Email
		}
	}
	return at, email
}

// extend продлевает последний отрезок плана или начинает новый
func extend(plan []Spell, email string, t time.Time) []Spell {
	if n := len(plan); n > 0 && plan[n-1].Email == email {
		plan[n-1].To = t.Add(poolStep)
		return plan
	}
	return append(plan, Spell{Email: email, From: t, To: t.Add(poolStep)})
}

// mixedTiers — у живых учёток разные тарифы
func (s *Store) mixedTiers() bool {
	tier := ""
	for i := range s.Accounts {
		o, err := s.Accounts[i].oauth()
		if err != nil || o.RateLimitTier == "" || s.Accounts[i].Dead != "" {
			continue
		}
		if tier != "" && o.RateLimitTier != tier {
			return true
		}
		tier = o.RateLimitTier
	}
	return false
}
