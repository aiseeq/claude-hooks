package statusline

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aiseeq/claude-hooks/internal/accounts"
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

// limit возвращает замер в форме входа строки статуса
func (w weeklyUsage) limit() *WeeklyLimit {
	used, resets := w.used, w.resetsAt.Unix()
	return &WeeklyLimit{UsedPercentage: &used, ResetsAt: &resets}
}

// limitsSnapshot — недельный лимит на диске. Его читают страница Сна
// (sleepstats), мастера и калибровка ёмкости: другого машинного источника
// процента у Claude Code нет, а строка статуса получает его при каждой отрисовке.
//
// Учётки на входе строки статуса нет, а сессии рисуются вперемешку. Поэтому
// неделя хранится по своему сбросу: у двух подписок разные недели (рабочая
// сбрасывается в понедельник, личная в субботу), и каждая неделя — отдельная
// запись в Weeks. Внутри недели доля только растёт, поэтому запись держит
// наибольшую: сессия, давно не ходившая в API, рисует старую долю (21.10 в
// 03:32 одна сессия записала 57% между 61% других), и это не откат.
//
// Верхние поля — неделя, доля которой выросла последней: учётка, что тратит
// сейчас. Их форма прежняя, чтобы читатели снимка не менялись
type limitsSnapshot struct {
	At        time.Time    `json:"at"`
	SevenDay  float64      `json:"sevenDayUsedPercentage"`
	ResetsAt  time.Time    `json:"sevenDayResetsAt"`
	Weeks     []weekRecord `json:"weeks"`
	FiveHours []fiveRecord `json:"fiveHours,omitempty"`
}

// weekRecord — неделя одной учётки. At — когда доля впервые дошла до
// SevenDay: по этим моментам считается цена процента. SeenAt — последняя
// отрисовка, подтвердившая долю. Session — сессия, первой увидевшая долю.
// Account — почта учётки (хранилище accounts): неделя, которой снимок ещё не
// видел, отходит учётке, чьи токены в этот момент у Claude Code. Пусто —
// учётка не опознана (снимок до хранилища учёток).
// Времена — с точностью до секунды (RFC 3339, как в прежнем снимке)
type weekRecord struct {
	ResetsAt time.Time `json:"sevenDayResetsAt"`
	SevenDay float64   `json:"sevenDayUsedPercentage"`
	At       time.Time `json:"at"`
	SeenAt   time.Time `json:"seenAt"`
	Session  string    `json:"session,omitempty"`
	Account  string    `json:"account,omitempty"`
}

// fiveRecord — пятичасовое окно учётки: последнее известное
type fiveRecord struct {
	Account  string    `json:"account"`
	ResetsAt time.Time `json:"resetsAt"`
	Used     float64   `json:"usedPercentage"`
	SeenAt   time.Time `json:"seenAt"`
}

// limitStep — строка журнала: доля недели выросла или началась новая неделя
type limitStep struct {
	At       time.Time `json:"at"`
	ResetsAt time.Time `json:"sevenDayResetsAt"`
	SevenDay float64   `json:"sevenDayUsedPercentage"`
	Session  string    `json:"session,omitempty"`
	Account  string    `json:"account,omitempty"`
}

// sample — замер одной отрисовки или одного ответа API
type sample struct {
	// week — неделя; nil — недели в замере нет (учётка ещё не тратила)
	week *weeklyUsage
	// five — пятичасовое окно; nil — нет
	five    *weeklyUsage
	session string
	// account — для строки статуса учётка, чьи токены сейчас у Claude Code:
	// ей отходит неделя, которой снимок ещё не видел. Для замера API —
	// учётка, чьим токеном он сделан
	account string
	// exact — учётка замера известна точно (замер API), а не выведена
	exact bool
}

// mergeResult — итог сведения замера
type mergeResult struct {
	// merged — свежайшая известная доля недели замера
	merged  weeklyUsage
	verdict weekVerdict
	// owner — учётка недели замера, пусто — не опознана
	owner    string
	snapshot *limitsSnapshot
}

// weekVerdict — что отрисовка сделала с записью недели
type weekVerdict int

const (
	// weekRaised — доля выросла или неделя новая: шаг идёт в журнал
	weekRaised weekVerdict = iota
	// weekConfirmed — доля та же
	weekConfirmed
	// weekStale — доля меньше записанной: сессия видит старый ответ API
	weekStale
)

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

// limitsHistory — журнал шагов рядом со снимком. Строка — шаг в 1% одной
// учётки, около сотни строк в неделю на учётку, поэтому журнал не обрезается
func limitsHistory(snapshot string) string {
	return filepath.Join(filepath.Dir(snapshot), "limits-history.jsonl")
}

// recordLimits сводит замер со снимком по пути из LimitsFile
func recordLimits(smp sample, now time.Time) (mergeResult, error) {
	path, err := LimitsFile()
	if err != nil {
		return mergeResult{verdict: weekRaised}, err
	}
	return mergeLimits(path, smp, now)
}

// mergeLimits сводит замер со снимком под блокировкой: сессии рисуются
// одновременно, и без неё одна затирала бы выросшую долю другой
func mergeLimits(path string, smp sample, now time.Time) (mergeResult, error) {
	res := mergeResult{verdict: weekRaised}
	if smp.week != nil {
		res.merged = *smp.week
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("cannot create the limits directory: %w", err)
	}
	unlock, err := lockFile(filepath.Join(dir, ".limits.lock"))
	if err != nil {
		return res, err
	}
	defer unlock()

	snapshot, err := loadLimits(path)
	if err != nil {
		return res, err
	}
	res = snapshot.merge(smp, now)
	res.snapshot = snapshot
	if smp.week != nil && res.verdict == weekRaised {
		step := limitStep{At: now.Truncate(time.Second), ResetsAt: res.merged.resetsAt, SevenDay: res.merged.used, Session: smp.session, Account: res.owner}
		if err := appendStep(limitsHistory(path), step); err != nil {
			return res, err
		}
	}
	return res, saveLimits(path, snapshot)
}

// merge вносит замер в неделю его сброса и пятичасовое окно его учётки,
// убирает прошедшие окна и пересчитывает верхние поля
func (s *limitsSnapshot) merge(smp sample, now time.Time) mergeResult {
	stamp := now.Truncate(time.Second)
	kept := s.Weeks[:0]
	for _, rec := range s.Weeks {
		if rec.ResetsAt.After(now) {
			kept = append(kept, rec)
		}
	}
	s.Weeks = kept
	keptFive := s.FiveHours[:0]
	for _, rec := range s.FiveHours {
		if rec.ResetsAt.After(now) {
			keptFive = append(keptFive, rec)
		}
	}
	s.FiveHours = keptFive

	res := mergeResult{verdict: weekRaised, owner: smp.account}
	if smp.week != nil {
		res.merged, res.verdict, res.owner = s.mergeWeek(*smp.week, smp, stamp)
		s.pickLatest()
	}
	if smp.five != nil && res.owner != "" {
		s.mergeFive(res.owner, *smp.five, stamp)
	}
	return res
}

// mergeWeek вносит неделю. Неделя ищется по сбросу: у учёток разные недели.
// Совпал сброс у двух учёток — берётся запись учётки замера. Возвращает
// свежайшую долю недели, вердикт и учётку недели
func (s *limitsSnapshot) mergeWeek(week weeklyUsage, smp sample, stamp time.Time) (weeklyUsage, weekVerdict, string) {
	idx := -1
	for i, rec := range s.Weeks {
		if !rec.ResetsAt.Equal(week.resetsAt) {
			continue
		}
		if smp.exact && rec.Account != smp.account && rec.Account != "" {
			continue
		}
		if idx < 0 || rec.Account == smp.account {
			idx = i
		}
	}
	if idx < 0 {
		s.Weeks = append(s.Weeks, weekRecord{
			ResetsAt: week.resetsAt,
			SevenDay: week.used,
			At:       stamp,
			SeenAt:   stamp,
			Session:  smp.session,
			Account:  smp.account,
		})
		return week, weekRaised, smp.account
	}
	rec := &s.Weeks[idx]
	if rec.Account == "" {
		// Неделя из снимка до хранилища учёток: её тратила учётка, что и
		// сейчас у Claude Code (при установке была одна неделя одной учётки)
		rec.Account = smp.account
	}
	merged := week
	verdict := weekRaised
	switch {
	case week.used > rec.SevenDay:
		rec.SevenDay, rec.At, rec.SeenAt, rec.Session = week.used, stamp, stamp, smp.session
	case week.used == rec.SevenDay:
		rec.SeenAt = stamp
		verdict = weekConfirmed
	default:
		merged.used = rec.SevenDay
		verdict = weekStale
	}
	return merged, verdict, rec.Account
}

// mergeFive вносит пятичасовое окно учётки: внутри окна доля только растёт,
// окно с более поздним сбросом сменяет прежнее, более раннее — старый ответ
func (s *limitsSnapshot) mergeFive(account string, five weeklyUsage, stamp time.Time) {
	for i := range s.FiveHours {
		rec := &s.FiveHours[i]
		if rec.Account != account {
			continue
		}
		switch {
		case five.resetsAt.After(rec.ResetsAt):
			rec.ResetsAt, rec.Used, rec.SeenAt = five.resetsAt, five.used, stamp
		case five.resetsAt.Equal(rec.ResetsAt):
			rec.Used, rec.SeenAt = max(rec.Used, five.used), stamp
		}
		return
	}
	s.FiveHours = append(s.FiveHours, fiveRecord{Account: account, ResetsAt: five.resetsAt, Used: five.used, SeenAt: stamp})
}

// pickLatest ставит в верхние поля неделю с последним ростом доли
func (s *limitsSnapshot) pickLatest() {
	i := latestWeek(s.Weeks)
	if i < 0 {
		s.At, s.SevenDay, s.ResetsAt = time.Time{}, 0, time.Time{}
		return
	}
	s.At, s.SevenDay, s.ResetsAt = s.Weeks[i].At, s.Weeks[i].SevenDay, s.Weeks[i].ResetsAt
}

// loadLimits читает снимок. Файла нет — снимок пустой. Снимок прежней формы
// (без weeks) становится первой неделей: доля в нём уже записана
func loadLimits(path string) (*limitsSnapshot, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &limitsSnapshot{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the limits snapshot: %w", err)
	}
	var s limitsSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("cannot parse the limits snapshot %s: %w", path, err)
	}
	if len(s.Weeks) == 0 && !s.ResetsAt.IsZero() {
		s.Weeks = []weekRecord{{ResetsAt: s.ResetsAt, SevenDay: s.SevenDay, At: s.At, SeenAt: s.At}}
	}
	return &s, nil
}

// readLimits читает снимок для сводки: снимок и журнал шагов
func readLimits() (string, *limitsSnapshot, []limitStep, error) {
	path, err := LimitsFile()
	if err != nil {
		return "", nil, nil, err
	}
	return readLimitsAt(path)
}

// readLimitsAt читает снимок и журнал по пути снимка
func readLimitsAt(path string) (string, *limitsSnapshot, []limitStep, error) {
	snapshot, err := loadLimits(path)
	if err != nil {
		return path, nil, nil, err
	}
	steps, err := readSteps(limitsHistory(path))
	return path, snapshot, steps, err
}

// saveLimits записывает снимок атомарно: страница может читать файл
// в момент, когда строка статуса его пишет
func saveLimits(path string, s *limitsSnapshot) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("failed to encode the limits snapshot: %w", err)
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

// appendStep дописывает шаг в журнал одной записью
func appendStep(path string, step limitStep) error {
	data, err := json.Marshal(step)
	if err != nil {
		return fmt.Errorf("failed to encode the limits step: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("cannot open the limits history: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("cannot write the limits history: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot write the limits history: %w", err)
	}
	return nil
}

// readSteps читает журнал шагов. Журнала нет — шагов нет
func readSteps(path string) ([]limitStep, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the limits history: %w", err)
	}
	defer f.Close()
	var steps []limitStep
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		var step limitStep
		if err := json.Unmarshal(scanner.Bytes(), &step); err != nil {
			return steps, fmt.Errorf("limits history %s line %d: %w", path, line, err)
		}
		steps = append(steps, step)
	}
	if err := scanner.Err(); err != nil {
		return steps, fmt.Errorf("cannot read the limits history: %w", err)
	}
	return steps, nil
}

// lockFile берёт исключительную блокировку файла и возвращает её снятие
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open the limits lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot lock the limits snapshot: %w", err)
	}
	return func() {
		// Закрытие файла снимает flock
		f.Close()
	}, nil
}

// WriteLimits печатает сводку для мастеров и калибровки: учётки хранилища
// по почте с неделей, пятью часами и сроком входа, неопознанные недели по
// сбросу, а при steps — журнал шагов одной таблицей с табуляцией
func WriteLimits(w io.Writer, steps bool) error {
	path, snapshot, journal, err := readLimits()
	if err != nil {
		return err
	}
	if steps {
		fmt.Fprintln(w, "at\tsevenDayResetsAt\tsevenDayUsedPercentage\tsession\taccount")
		for _, step := range journal {
			fmt.Fprintf(w, "%s\t%s\t%g\t%s\t%s\n", step.At.Format(time.RFC3339), step.ResetsAt.Format(time.RFC3339), step.SevenDay, step.Session, step.Account)
		}
		return nil
	}
	paths, err := accounts.DefaultPaths()
	if err != nil {
		return err
	}
	store, err := accounts.Load(paths)
	if err != nil {
		return err
	}
	now := time.Now()
	infos := store.Infos()
	weeks := snapshot.Weeks
	if len(weeks) == 0 && len(infos) == 0 {
		fmt.Fprintf(w, "Замеров недели нет: %s пуст или его нет\n", path)
		return nil
	}
	latest := latestWeek(weeks)
	shown := map[int]bool{}
	if len(infos) > 0 {
		fmt.Fprintf(w, "Подписки Claude (%s, %s):\n", paths.Store(), path)
		for _, info := range infos {
			writeAccount(w, info, snapshot, shown, now)
		}
		auto := "включено"
		if store.AutoOff {
			auto = "выключено (claude-hooks account auto on)"
		}
		fmt.Fprintf(w, "Автопереключение: %s; учётка пуста с %d%% недели или %d%% пяти часов, из непустых — ближайший сброс недели\n",
			auto, accounts.WeekEmptyPercent, accounts.FiveHourEmptyPercent)
	}
	unknown := false
	for i, rec := range weeks {
		if shown[i] {
			continue
		}
		if !unknown {
			if len(infos) > 0 {
				fmt.Fprintln(w, "Недели неопознанных учёток (по сбросу):")
			} else {
				fmt.Fprintf(w, "Неделя Claude по учёткам, учётка — свой сброс недели (%s):\n", path)
			}
			unknown = true
		}
		mark := ""
		if i == latest {
			mark = "  ← тратит сейчас"
		}
		fmt.Fprintf(w, "сброс %s  %g%%  с %s, видно %s%s\n",
			accounts.ShortTime(rec.ResetsAt), rec.SevenDay, accounts.ShortTime(rec.At), accounts.ShortTime(rec.SeenAt), mark)
	}
	fmt.Fprintf(w, "Шагов в журнале: %d (claude-hooks limits --steps)\n", len(journal))
	return nil
}

// writeAccount печатает учётку: почта, неделя, пять часов, срок входа
func writeAccount(w io.Writer, info accounts.Info, snapshot *limitsSnapshot, shown map[int]bool, now time.Time) {
	mark := ""
	if info.Active {
		mark = "  ← сейчас у Claude Code"
	}
	fmt.Fprintf(w, "%s%s\n", info.Email, mark)
	week := -1
	for i, rec := range snapshot.Weeks {
		if rec.Account == info.Email && (week < 0 || rec.ResetsAt.After(snapshot.Weeks[week].ResetsAt)) {
			week = i
		}
	}
	if week >= 0 {
		rec := snapshot.Weeks[week]
		shown[week] = true
		fmt.Fprintf(w, "  неделя %g%%, сброс %s (доля с %s, видно %s)\n",
			rec.SevenDay, accounts.ShortTime(rec.ResetsAt), accounts.ShortTime(rec.At), accounts.ShortTime(rec.SeenAt))
	} else {
		fmt.Fprintln(w, "  неделя неизвестна или не начата")
	}
	for _, rec := range snapshot.FiveHours {
		if rec.Account == info.Email && rec.ResetsAt.After(now) {
			fmt.Fprintf(w, "  пять часов %g%%, сброс %s\n", rec.Used, accounts.ShortTime(rec.ResetsAt))
		}
	}
	switch {
	case info.Dead != "":
		fmt.Fprintf(w, "  вход мёртв: %s\n", info.Dead)
	case !info.LoginExpires.IsZero():
		left := info.LoginExpires.Sub(now)
		note := ""
		if left < 3*24*time.Hour {
			note = " — скоро /login"
		}
		fmt.Fprintf(w, "  вход до %s%s\n", accounts.ShortTime(info.LoginExpires), note)
	}
}

// latestWeek — номер недели с последним ростом доли, -1 без недель
func latestWeek(weeks []weekRecord) int {
	latest := -1
	for i, rec := range weeks {
		if latest < 0 || rec.At.After(weeks[latest].At) {
			latest = i
		}
	}
	return latest
}

// accountUsage — расход учёток по снимку: свежайшая неделя и пять часов
// каждой опознанной учётки
func accountUsage(s *limitsSnapshot, now time.Time) map[string]accounts.Usage {
	out := map[string]accounts.Usage{}
	for _, rec := range s.Weeks {
		if rec.Account == "" || !rec.ResetsAt.After(now) {
			continue
		}
		u := out[rec.Account]
		if rec.ResetsAt.After(u.Week.ResetsAt) {
			u.Email, u.Week = rec.Account, accounts.Window{Used: rec.SevenDay, ResetsAt: rec.ResetsAt}
		}
		out[rec.Account] = u
	}
	for _, rec := range s.FiveHours {
		if rec.Account == "" || !rec.ResetsAt.After(now) {
			continue
		}
		u := out[rec.Account]
		u.Email, u.FiveHour = rec.Account, accounts.Window{Used: rec.Used, ResetsAt: rec.ResetsAt}
		out[rec.Account] = u
	}
	return out
}

// SnapshotLimits — снимок строки статуса для ротации учёток: расход учёток
// и запись расхода, спрошенного у API
type SnapshotLimits struct{}

// Usage читает расход учёток из снимка
func (SnapshotLimits) Usage() (map[string]accounts.Usage, error) {
	path, err := LimitsFile()
	if err != nil {
		return nil, err
	}
	snapshot, err := loadLimits(path)
	if err != nil {
		return nil, err
	}
	return accountUsage(snapshot, time.Now()), nil
}

// Record вписывает расход учётки, спрошенный у API её токеном: учётка
// известна точно. Окно без сброса (неделя не начата) не пишется
func (SnapshotLimits) Record(email string, week, fiveHour accounts.Window, now time.Time) error {
	smp := sample{session: "api", account: email, exact: true}
	if !week.ResetsAt.IsZero() {
		smp.week = &weeklyUsage{used: week.Used, resetsAt: week.ResetsAt}
	}
	if !fiveHour.ResetsAt.IsZero() {
		smp.five = &weeklyUsage{used: fiveHour.Used, resetsAt: fiveHour.ResetsAt}
	}
	if smp.week == nil && smp.five == nil {
		return nil
	}
	_, err := recordLimits(smp, now)
	return err
}
