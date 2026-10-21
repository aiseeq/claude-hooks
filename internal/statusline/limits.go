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
	At       time.Time    `json:"at"`
	SevenDay float64      `json:"sevenDayUsedPercentage"`
	ResetsAt time.Time    `json:"sevenDayResetsAt"`
	Weeks    []weekRecord `json:"weeks"`
}

// weekRecord — неделя одной учётки. At — когда доля впервые дошла до
// SevenDay: по этим моментам считается цена процента. SeenAt — последняя
// отрисовка, подтвердившая долю. Session — сессия, первой увидевшая долю.
// Времена — с точностью до секунды (RFC 3339, как в прежнем снимке)
type weekRecord struct {
	ResetsAt time.Time `json:"sevenDayResetsAt"`
	SevenDay float64   `json:"sevenDayUsedPercentage"`
	At       time.Time `json:"at"`
	SeenAt   time.Time `json:"seenAt"`
	Session  string    `json:"session,omitempty"`
}

// limitStep — строка журнала: доля недели выросла или началась новая неделя
type limitStep struct {
	At       time.Time `json:"at"`
	ResetsAt time.Time `json:"sevenDayResetsAt"`
	SevenDay float64   `json:"sevenDayUsedPercentage"`
	Session  string    `json:"session,omitempty"`
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

// recordWeeklyLimit сводит замер сессии со снимком по пути из LimitsFile и
// возвращает свежайшую известную долю той же недели
func recordWeeklyLimit(week weeklyUsage, session string, now time.Time) (weeklyUsage, weekVerdict, error) {
	path, err := LimitsFile()
	if err != nil {
		return week, weekRaised, err
	}
	return mergeWeeklyLimit(path, week, session, now)
}

// mergeWeeklyLimit сводит замер со снимком под блокировкой: сессии рисуются
// одновременно, и без неё одна затирала бы выросшую долю другой
func mergeWeeklyLimit(path string, week weeklyUsage, session string, now time.Time) (weeklyUsage, weekVerdict, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return week, weekRaised, fmt.Errorf("cannot create the limits directory: %w", err)
	}
	unlock, err := lockFile(filepath.Join(dir, ".limits.lock"))
	if err != nil {
		return week, weekRaised, err
	}
	defer unlock()

	snapshot, err := loadLimits(path)
	if err != nil {
		return week, weekRaised, err
	}
	merged, verdict := snapshot.merge(week, session, now)
	if verdict == weekRaised {
		step := limitStep{At: now.Truncate(time.Second), ResetsAt: merged.resetsAt, SevenDay: merged.used, Session: session}
		if err := appendStep(limitsHistory(path), step); err != nil {
			return merged, verdict, err
		}
	}
	return merged, verdict, saveLimits(path, snapshot)
}

// merge вносит замер в неделю его сброса, убирает прошедшие недели и
// пересчитывает верхние поля
func (s *limitsSnapshot) merge(week weeklyUsage, session string, now time.Time) (weeklyUsage, weekVerdict) {
	stamp := now.Truncate(time.Second)
	kept := s.Weeks[:0]
	for _, rec := range s.Weeks {
		if rec.ResetsAt.After(now) {
			kept = append(kept, rec)
		}
	}
	s.Weeks = kept

	merged, verdict := week, weekRaised
	found := false
	for i := range s.Weeks {
		rec := &s.Weeks[i]
		if !rec.ResetsAt.Equal(week.resetsAt) {
			continue
		}
		found = true
		switch {
		case week.used > rec.SevenDay:
			rec.SevenDay, rec.At, rec.SeenAt, rec.Session = week.used, stamp, stamp, session
		case week.used == rec.SevenDay:
			rec.SeenAt = stamp
			verdict = weekConfirmed
		default:
			merged.used = rec.SevenDay
			verdict = weekStale
		}
		break
	}
	if !found {
		s.Weeks = append(s.Weeks, weekRecord{
			ResetsAt: week.resetsAt,
			SevenDay: week.used,
			At:       stamp,
			SeenAt:   stamp,
			Session:  session,
		})
	}
	s.pickLatest()
	return merged, verdict
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

// ReadLimits читает снимок для сводки: все недели и журнал шагов
func ReadLimits() (path string, weeks []weekRecord, steps []limitStep, err error) {
	path, err = LimitsFile()
	if err != nil {
		return "", nil, nil, err
	}
	return readLimitsAt(path)
}

// readLimitsAt читает снимок и журнал по пути снимка
func readLimitsAt(path string) (string, []weekRecord, []limitStep, error) {
	snapshot, err := loadLimits(path)
	if err != nil {
		return path, nil, nil, err
	}
	steps, err := readSteps(limitsHistory(path))
	return path, snapshot.Weeks, steps, err
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

// weekdays — короткие имена дней недели для сводки
var weekdays = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// shortTime пишет время местным: «пн 26.10 11:00»
func shortTime(at time.Time) string {
	at = at.Local()
	return weekdays[at.Weekday()] + " " + at.Format("02.01 15:04")
}

// WriteLimits печатает сводку для мастеров и калибровки: неделю каждой
// учётки и, при steps, журнал шагов одной таблицей с табуляцией
func WriteLimits(w io.Writer, steps bool) error {
	path, weeks, journal, err := ReadLimits()
	if err != nil {
		return err
	}
	if steps {
		fmt.Fprintln(w, "at\tsevenDayResetsAt\tsevenDayUsedPercentage\tsession")
		for _, step := range journal {
			fmt.Fprintf(w, "%s\t%s\t%g\t%s\n", step.At.Format(time.RFC3339), step.ResetsAt.Format(time.RFC3339), step.SevenDay, step.Session)
		}
		return nil
	}
	if len(weeks) == 0 {
		fmt.Fprintf(w, "Замеров недели нет: %s пуст или его нет\n", path)
		return nil
	}
	latest := latestWeek(weeks)
	fmt.Fprintf(w, "Неделя Claude по учёткам, учётка — свой сброс недели (%s):\n", path)
	for i, rec := range weeks {
		mark := ""
		if i == latest {
			mark = "  ← тратит сейчас"
		}
		fmt.Fprintf(w, "сброс %s  %g%%  с %s, видно %s%s\n",
			shortTime(rec.ResetsAt), rec.SevenDay, shortTime(rec.At), shortTime(rec.SeenAt), mark)
	}
	fmt.Fprintf(w, "Шагов в журнале: %d (claude-hooks limits --steps)\n", len(journal))
	return nil
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
