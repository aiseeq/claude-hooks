package statusline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Рабочая неделя сбрасывается в понедельник, личная — в субботу
var (
	workResets     = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	personalResets = time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC)
)

func readSnapshot(t *testing.T, path string) limitsSnapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got limitsSnapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func merge(t *testing.T, path string, used float64, resets time.Time, session string, now time.Time) (weeklyUsage, weekVerdict) {
	t.Helper()
	merged, verdict, err := mergeWeeklyLimit(path, weeklyUsage{used: used, resetsAt: resets}, session, now)
	if err != nil {
		t.Fatal(err)
	}
	return merged, verdict
}

func TestMergeWeeklyLimitFirstSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-hooks", "limits.json")
	now := time.Date(2026, 10, 20, 12, 0, 0, 123456789, time.UTC)
	merged, verdict := merge(t, path, 38, workResets, "s1", now)
	if verdict != weekRaised || merged.used != 38 {
		t.Errorf("первый замер: %v, %+v", verdict, merged)
	}
	// верхние поля читает sleepstats как RFC 3339 с секундами: форма прежняя
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"at":"2026-10-20T12:00:00Z","sevenDayUsedPercentage":38,"sevenDayResetsAt":"2026-10-26T10:00:00Z"`; !strings.Contains(string(raw), want) {
		t.Errorf("верхние поля изменили форму: %s", raw)
	}
	got := readSnapshot(t, path)
	if got.SevenDay != 38 {
		t.Errorf("доля недели %v, ожидалось 38", got.SevenDay)
	}
	if at := got.At; !at.Equal(now.Truncate(time.Second)) {
		t.Errorf("время снимка %v", got.At)
	}
	if resets := got.ResetsAt; !resets.Equal(workResets) {
		t.Errorf("сброс %q", got.ResetsAt)
	}
	if len(got.Weeks) != 1 || got.Weeks[0].Session != "s1" {
		t.Errorf("недели: %+v", got.Weeks)
	}
	// рядом со снимком только журнал шагов и блокировка, временных файлов нет
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		switch entry.Name() {
		case "limits.json", "limits-history.jsonl", ".limits.lock":
		default:
			t.Errorf("лишний файл рядом со снимком: %s", entry.Name())
		}
	}
}

// 21.10 03:32: сессия, давно не ходившая в API, нарисовала 57% между 61%
// других. Снимок и её полоса остаются на 61%
func TestMergeWeeklyLimitStaleSessionKeepsMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.json")
	base := time.Date(2026, 10, 21, 1, 31, 13, 0, time.UTC)
	merge(t, path, 61, workResets, "demu", base)
	merged, verdict := merge(t, path, 57, workResets, "git-admin", base.Add(47*time.Second))
	if verdict != weekStale || merged.used != 61 {
		t.Errorf("старая доля: %v, полоса %v, ожидалось 61", verdict, merged.used)
	}
	if _, verdict := merge(t, path, 61, workResets, "demu", base.Add(80*time.Second)); verdict != weekConfirmed {
		t.Errorf("та же доля: %v", verdict)
	}
	got := readSnapshot(t, path)
	if got.SevenDay != 61 || len(got.Weeks) != 1 {
		t.Fatalf("снимок: %+v", got)
	}
	rec := got.Weeks[0]
	if at := rec.At; !at.Equal(base) {
		t.Errorf("момент шага сдвинулся: %v", rec.At)
	}
	if seen := rec.SeenAt; !seen.Equal(base.Add(80 * time.Second)) {
		t.Errorf("последнее подтверждение %v", rec.SeenAt)
	}
	_, _, steps, err := readLimitsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].SevenDay != 61 {
		t.Errorf("в журнале только рост: %+v", steps)
	}
}

// Две учётки пишут в один снимок вперемешку: у каждой своя неделя, ни одна
// не затирает другую, верхние поля — учётка с последним ростом
func TestMergeWeeklyLimitTwoAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.json")
	base := time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)
	merge(t, path, 60, workResets, "work-1", base)
	merge(t, path, 4, personalResets, "personal-1", base.Add(time.Minute))
	if merged, verdict := merge(t, path, 61, workResets, "work-2", base.Add(2*time.Minute)); verdict != weekRaised || merged.used != 61 {
		t.Errorf("рост рабочей: %v, %v", verdict, merged.used)
	}
	if merged, verdict := merge(t, path, 4, personalResets, "personal-1", base.Add(3*time.Minute)); verdict != weekConfirmed || merged.used != 4 {
		t.Errorf("личная показывает свою долю: %v, %v", verdict, merged.used)
	}

	got := readSnapshot(t, path)
	if len(got.Weeks) != 2 {
		t.Fatalf("ожидались две недели: %+v", got.Weeks)
	}
	byReset := map[int64]float64{}
	for _, rec := range got.Weeks {
		byReset[rec.ResetsAt.Unix()] = rec.SevenDay
	}
	if byReset[workResets.Unix()] != 61 || byReset[personalResets.Unix()] != 4 {
		t.Errorf("доли по учёткам: %v", byReset)
	}
	if resets := got.ResetsAt; !resets.Equal(workResets) || got.SevenDay != 61 {
		t.Errorf("верхние поля — рабочая с последним ростом: %v %q", got.SevenDay, got.ResetsAt)
	}

	merge(t, path, 5, personalResets, "personal-1", base.Add(4*time.Minute))
	if got := readSnapshot(t, path); got.SevenDay != 5 {
		t.Errorf("после роста личной верхние поля — личная: %+v", got)
	}
}

// Сводка называет обе учётки и помечает тратящую сейчас
func TestWriteLimitsTwoAccounts(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path, err := LimitsFile()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	workNext, personalNext := base.Add(5*24*time.Hour).Truncate(time.Hour), base.Add(3*24*time.Hour).Truncate(time.Hour)
	merge(t, path, 61, workNext, "work", base)
	merge(t, path, 4, personalNext, "personal", base.Add(time.Minute))

	var out strings.Builder
	if err := WriteLimits(&out, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "61%") || !strings.Contains(text, "4%  с") {
		t.Errorf("в сводке нет обеих учёток:\n%s", text)
	}
	if lines := strings.Split(text, "\n"); !strings.Contains(lines[2], "4%") || !strings.Contains(lines[2], "тратит сейчас") {
		t.Errorf("тратящая сейчас — личная:\n%s", text)
	}

	out.Reset()
	if err := WriteLimits(&out, true); err != nil {
		t.Fatal(err)
	}
	if rows := strings.Split(strings.TrimSpace(out.String()), "\n"); len(rows) != 3 || !strings.HasSuffix(rows[1], "\t61\twork") {
		t.Errorf("журнал шагов:\n%s", out.String())
	}
}

// Прошедшая неделя уходит из снимка, новая неделя той же учётки — новая запись
func TestMergeWeeklyLimitDropsPastWeek(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.json")
	merge(t, path, 99, workResets, "s", workResets.Add(-time.Hour))
	next := workResets.Add(weekWindow)
	merged, verdict := merge(t, path, 1, next, "s", workResets.Add(time.Hour))
	if verdict != weekRaised || merged.used != 1 {
		t.Errorf("новая неделя: %v, %v", verdict, merged.used)
	}
	got := readSnapshot(t, path)
	if len(got.Weeks) != 1 || got.SevenDay != 1 {
		t.Errorf("прошедшая неделя осталась: %+v", got)
	}
}

// Снимок прежней формы (без weeks) становится первой неделей
func TestMergeWeeklyLimitLegacySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.json")
	legacy := `{"at":"2026-10-21T03:31:13+02:00","sevenDayUsedPercentage":61,"sevenDayResetsAt":"2026-10-26T11:00:00+01:00"}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, verdict := merge(t, path, 57, workResets, "s", time.Date(2026, 10, 21, 1, 32, 0, 0, time.UTC))
	if verdict != weekStale || merged.used != 61 {
		t.Errorf("прежний снимок не учтён: %v, %v", verdict, merged.used)
	}
}

// Сессии рисуются одновременно: наибольшая доля не теряется
func TestMergeWeeklyLimitConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.json")
	now := time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(used float64) {
			defer wg.Done()
			if _, _, err := mergeWeeklyLimit(path, weeklyUsage{used: used, resetsAt: workResets}, "s", now); err != nil {
				errs <- err
			}
		}(float64(i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := readSnapshot(t, path); got.SevenDay != 39 {
		t.Errorf("доля %v, ожидалась наибольшая 39", got.SevenDay)
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

// Сессия со старым ответом API рисует свежую долю своей недели, а не свою
func TestRenderStaleSessionShowsRecordedWeek(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("KONSOLE_DBUS_SERVICE", "")
	t.Setenv("KONSOLE_DBUS_SESSION", "")
	resets := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Hour).Unix()
	render := func(session string, used int) string {
		t.Helper()
		input := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"rate_limits":{"seven_day":{"used_percentage":%d,"resets_at":%d}}}`,
			session, t.TempDir(), used, resets)
		line, err := Render(context.Background(), strings.NewReader(input), testLogger(t))
		if err != nil {
			t.Fatal(err)
		}
		return plain(line)
	}
	if line := render("fresh", 61); !strings.Contains(line, "нед ▓▓▓▓▓▓░░░░ 61%") {
		t.Errorf("свежая сессия: %q", line)
	}
	if line := render("stale", 57); !strings.Contains(line, "61%") || strings.Contains(line, "57%") {
		t.Errorf("старая сессия должна показать 61%%: %q", line)
	}
}
