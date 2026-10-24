package statusline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiseeq/claude-hooks/internal/accounts"
)

// rotationCall — запуск ротации, который строка статуса попросила
type rotationCall struct {
	store *accounts.Store
	usage map[string]accounts.Usage
	args  []string
}

// isolateRender отделяет Render от настоящих каталогов: состояние, Claude
// Code и запуск ротации. Возвращает запуски ротации
func isolateRender(t *testing.T) *[]rotationCall {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("KONSOLE_DBUS_SERVICE", "")
	t.Setenv("KONSOLE_DBUS_SESSION", "")
	var calls []rotationCall
	saved := startRotation
	startRotation = func(_ accounts.Paths, s *accounts.Store, usage map[string]accounts.Usage, _ time.Time, _ string, args ...string) (string, error) {
		calls = append(calls, rotationCall{store: s, usage: usage, args: args})
		return "", nil
	}
	t.Cleanup(func() { startRotation = saved })
	return &calls
}

// writeStore кладёт хранилище двух учёток, активна рабочая
func writeStore(t *testing.T, autoOff bool) {
	t.Helper()
	paths, err := accounts.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	login := func(name string) string {
		return fmt.Sprintf(`{"accessToken":"a-%s","refreshToken":"r-%s","expiresAt":%d,"refreshTokenExpiresAt":%d}`,
			name, name, time.Now().Add(time.Hour).UnixMilli(), time.Now().Add(20*24*time.Hour).UnixMilli())
	}
	store := fmt.Sprintf(`{"active":"u-work","autoOff":%t,"accounts":[
		{"uuid":"u-work","email":"work@example.com","claudeAiOauth":%s,"savedAt":"2026-10-22T08:00:00Z"},
		{"uuid":"u-personal","email":"personal@example.com","claudeAiOauth":%s,"savedAt":"2026-10-22T08:00:00Z"}]}`,
		autoOff, login("work"), login("personal"))
	if err := os.WriteFile(paths.Store(), []byte(store), 0o600); err != nil {
		t.Fatal(err)
	}
}

func renderInput(t *testing.T, session string, week int, weekResets time.Time, five int, fiveResets time.Time) string {
	t.Helper()
	input := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"rate_limits":{"seven_day":{"used_percentage":%d,"resets_at":%d},"five_hour":{"used_percentage":%d,"resets_at":%d}}}`,
		session, t.TempDir(), week, weekResets.Unix(), five, fiveResets.Unix())
	line, err := Render(strings.NewReader(input), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	return plain(line)
}

// Неделя, которой снимок не видел, отходит активной учётке; полоса
// называет учётку недели, ротация получает расход по почте
func TestRenderAttributesWeekToActiveAccount(t *testing.T) {
	calls := isolateRender(t)
	writeStore(t, false)
	now := time.Now()
	workNext := now.Add(4 * 24 * time.Hour).Truncate(time.Hour)
	fiveNext := now.Add(2 * time.Hour).Truncate(time.Minute)

	if line := renderInput(t, "s1", 40, workNext, 12, fiveNext); !strings.Contains(line, "work@example.com") || !strings.Contains(line, "40%") || strings.Contains(line, "нед") {
		t.Errorf("полоса недели: %q", line)
	}
	path, err := LimitsFile()
	if err != nil {
		t.Fatal(err)
	}
	snap := readSnapshot(t, path)
	if len(snap.Weeks) != 1 || snap.Weeks[0].Account != "work@example.com" {
		t.Fatalf("неделя не приписана рабочей: %+v", snap.Weeks)
	}
	if len(snap.FiveHours) != 1 || snap.FiveHours[0].Account != "work@example.com" || snap.FiveHours[0].Used != 12 {
		t.Errorf("пять часов: %+v", snap.FiveHours)
	}
	_, _, steps, err := readLimitsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Account != "work@example.com" {
		t.Errorf("шаг журнала без учётки: %+v", steps)
	}
	if len(*calls) != 1 {
		t.Fatalf("ротация запрошена %d раз", len(*calls))
	}
	call := (*calls)[0]
	if u := call.usage["work@example.com"]; u.Week.Used != 40 || u.FiveHour.Used != 12 {
		t.Errorf("расход для ротации: %+v", call.usage)
	}
	if strings.Join(call.args, " ") != "account rotate" {
		t.Errorf("подкоманда ротации: %v", call.args)
	}

	// Сессия со старым ответом прежней учётки рисует её неделю — по сбросу,
	// хотя активна уже другая
	paths, err := accounts.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.Store())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Store(), []byte(strings.Replace(string(data), `"active":"u-work"`, `"active":"u-personal"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if line := renderInput(t, "s2", 40, workNext, 12, fiveNext); !strings.Contains(line, "work@example.com") {
		t.Errorf("старая сессия: %q", line)
	}
	personalNext := now.Add(6 * 24 * time.Hour).Truncate(time.Hour)
	if line := renderInput(t, "s3", 1, personalNext, 1, fiveNext.Add(time.Hour)); !strings.Contains(line, "personal@example.com") {
		t.Errorf("новая неделя активной: %q", line)
	}
	snap = readSnapshot(t, path)
	if len(snap.Weeks) != 2 || snap.Weeks[1].Account != "personal@example.com" {
		t.Errorf("недели после переключения: %+v", snap.Weeks)
	}

	var out strings.Builder
	if err := WriteLimits(&out, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"work@example.com\n  неделя 40%", "пять часов 12%", "personal@example.com  ← сейчас у Claude Code\n  неделя 1%", "Автопереключение: включено"} {
		if !strings.Contains(text, want) {
			t.Errorf("в сводке нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "r-work") || strings.Contains(text, "a-work") {
		t.Error("сводка печатает токен")
	}
}

// Одна учётка или без хранилища — полоса прежняя, без почты
func TestRenderSingleAccountLabel(t *testing.T) {
	isolateRender(t)
	now := time.Now()
	if line := renderInput(t, "s", 10, now.Add(3*24*time.Hour).Truncate(time.Hour), 1, now.Add(time.Hour)); !strings.Contains(line, "нед ▓") {
		t.Errorf("полоса без хранилища: %q", line)
	}
	path, err := LimitsFile()
	if err != nil {
		t.Fatal(err)
	}
	if snap := readSnapshot(t, path); len(snap.FiveHours) != 0 || snap.Weeks[0].Account != "" {
		t.Errorf("без хранилища учётка не известна: %+v", snap)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "accounts.json")); !os.IsNotExist(err) {
		t.Error("строка статуса создала хранилище")
	}
}

// Замер API пишется точно своей учётке, даже при общем сбросе с другой
func TestSnapshotLimitsRecordExact(t *testing.T) {
	isolateRender(t)
	writeStore(t, true)
	now := time.Now()
	resets := now.Add(3 * 24 * time.Hour).Truncate(time.Hour)
	renderInput(t, "s", 50, resets, 5, now.Add(time.Hour))
	if err := (SnapshotLimits{}).Record("personal@example.com", accounts.Window{Used: 7, ResetsAt: resets}, accounts.Window{}, now); err != nil {
		t.Fatal(err)
	}
	usage, err := (SnapshotLimits{}).Usage()
	if err != nil {
		t.Fatal(err)
	}
	if usage["work@example.com"].Week.Used != 50 || usage["personal@example.com"].Week.Used != 7 {
		t.Errorf("расход учёток: %+v", usage)
	}
}

// 22.10 11:28: после входа в другую учётку и возврата на рабочую сессия
// нарисовала долю из ответа, полученного токеном другой учётки, — новая
// неделя ушла рабочей, а её настоящая неделя стала «неопознанной». Неделя
// с новым сбросом не может быть рабочей, пока у той живая неделя: она
// неопознана, пока её не заберёт замер API; полоса — неделя рабочей
func TestRenderForeignWeekAfterSwitch(t *testing.T) {
	isolateRender(t)
	writeStore(t, false)
	now := time.Now()
	workNext := now.Add(4 * 24 * time.Hour).Truncate(time.Hour)
	personalNext := now.Add(6 * 24 * time.Hour).Truncate(time.Hour)
	five := now.Add(2 * time.Hour).Truncate(time.Minute)
	renderInput(t, "lead", 96, workNext, 3, five)

	line := renderInput(t, "other-project", 0, personalNext, 0, five.Add(time.Hour))
	if !strings.Contains(line, "work@example.com") || !strings.Contains(line, "96%") {
		t.Errorf("полоса — неделя рабочей, что у Claude Code: %q", line)
	}
	path, err := LimitsFile()
	if err != nil {
		t.Fatal(err)
	}
	snap := readSnapshot(t, path)
	for _, rec := range snap.Weeks {
		if rec.ResetsAt.Equal(personalNext) && rec.Account != "" {
			t.Errorf("чужая неделя приписана %q", rec.Account)
		}
	}
	if !snap.ResetsAt.Equal(workNext) || snap.SevenDay != 96 {
		t.Errorf("верхние поля — неделя рабочей: %v %v", snap.SevenDay, snap.ResetsAt)
	}
	for _, f := range snap.FiveHours {
		if f.Account == "work@example.com" && f.Used != 3 {
			t.Errorf("пять часов рабочей затёрты чужими: %+v", f)
		}
	}

	// Замер API токеном личной забирает её неделю; верхние поля не меняет
	if err := (SnapshotLimits{}).Record("personal@example.com", accounts.Window{Used: 0, ResetsAt: personalNext}, accounts.Window{}, now); err != nil {
		t.Fatal(err)
	}
	snap = readSnapshot(t, path)
	if len(snap.Weeks) != 2 || !snap.ResetsAt.Equal(workNext) {
		t.Fatalf("недели после замера API: %+v", snap)
	}
	var out strings.Builder
	if err := WriteLimits(&out, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"work@example.com  ← сейчас у Claude Code\n  неделя 96%", "personal@example.com\n  неделя 0%"} {
		if !strings.Contains(text, want) {
			t.Errorf("в сводке нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "неопознанных") {
		t.Errorf("неопознанных недель быть не должно:\n%s", text)
	}
}

// Снимок, испорченный до починки: рабочей приписаны две живые недели.
// Замер API личной возвращает ей её неделю, рабочая остаётся со своей
func TestRecordExactRepairsMisattributedWeek(t *testing.T) {
	isolateRender(t)
	writeStore(t, true)
	now := time.Now().Truncate(time.Second)
	workNext := now.Add(4 * 24 * time.Hour).Truncate(time.Hour)
	personalNext := now.Add(6 * 24 * time.Hour).Truncate(time.Hour)
	path, err := LimitsFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := &limitsSnapshot{Weeks: []weekRecord{
		{ResetsAt: workNext, SevenDay: 96, At: now, SeenAt: now, Account: "work@example.com"},
		{ResetsAt: personalNext, SevenDay: 0, At: now, SeenAt: now, Account: "work@example.com"},
	}}
	if err := saveLimits(path, broken); err != nil {
		t.Fatal(err)
	}
	if err := (SnapshotLimits{}).Record("personal@example.com", accounts.Window{Used: 0, ResetsAt: personalNext}, accounts.Window{}, now); err != nil {
		t.Fatal(err)
	}
	usage, err := (SnapshotLimits{}).Usage()
	if err != nil {
		t.Fatal(err)
	}
	if w := usage["work@example.com"].Week; w.Used != 96 || !w.ResetsAt.Equal(workNext) {
		t.Errorf("неделя рабочей: %+v", w)
	}
	if w := usage["personal@example.com"].Week; !w.ResetsAt.Equal(personalNext) {
		t.Errorf("неделя личной: %+v", w)
	}
	if n := len(readSnapshot(t, path).Weeks); n != 2 {
		t.Errorf("недель %d, ожидались 2", n)
	}

	// Замер API рабочей с другим сбросом: прежняя неделя рабочей ошибочна
	// и становится неопознанной
	other := workNext.Add(time.Hour)
	if err := (SnapshotLimits{}).Record("work@example.com", accounts.Window{Used: 10, ResetsAt: other}, accounts.Window{}, now); err != nil {
		t.Fatal(err)
	}
	for _, rec := range readSnapshot(t, path).Weeks {
		if rec.ResetsAt.Equal(workNext) && rec.Account != "" {
			t.Errorf("ошибочная неделя рабочей осталась за %q", rec.Account)
		}
	}
}

// Сессия без доли во входе (после /login до первого запроса) рисует неделю
// учётки, что сейчас у Claude Code, — полоса не пропадает
func TestRenderWithoutRateLimitsShowsActiveWeek(t *testing.T) {
	isolateRender(t)
	writeStore(t, true)
	now := time.Now()
	renderInput(t, "s", 96, now.Add(4*24*time.Hour).Truncate(time.Hour), 3, now.Add(time.Hour))
	input := fmt.Sprintf(`{"session_id":"fresh","cwd":%q}`, t.TempDir())
	line, err := Render(strings.NewReader(input), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := plain(line); !strings.Contains(got, "work@example.com") || !strings.Contains(got, "96%") {
		t.Errorf("полоса без доли во входе: %q", got)
	}
}

// Цвет полосы — прогноз пула: доля активной мала, но темп по журналу
// опустошает обе учётки до сброса — полоса красная, без значка; сводка
// называет момент
func TestRenderColorsByPool(t *testing.T) {
	isolateRender(t)
	writeStore(t, true)
	now := time.Now()
	workNext := now.Add(5 * 24 * time.Hour).Truncate(time.Hour)
	personalNext := now.Add(6 * 24 * time.Hour).Truncate(time.Hour)
	path, err := LimitsFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// 40% за 10 ч: 4% в час, двух учёток хватит на сутки с небольшим
	var history strings.Builder
	for i, used := range []int{10, 30, 50} {
		history.WriteString(fmt.Sprintf(`{"at":%q,"sevenDayResetsAt":%q,"sevenDayUsedPercentage":%d,"account":"work@example.com"}`+"\n",
			now.Add(time.Duration(i*5-10)*time.Hour).Format(time.RFC3339), workNext.Format(time.RFC3339), used))
	}
	if err := os.WriteFile(limitsHistory(path), []byte(history.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (SnapshotLimits{}).Record("personal@example.com", accounts.Window{Used: 50, ResetsAt: personalNext}, accounts.Window{}, now); err != nil {
		t.Fatal(err)
	}
	// Сама неделя активной зелёная бы: 20% за двое суток — 70% к сбросу
	raw := renderRaw(t, "s", 20, workNext)
	if !strings.Contains(raw, red+"▓") || strings.Contains(raw, "▲") {
		t.Errorf("полоса должна быть красной без значка: %q", plain(raw))
	}
	var out strings.Builder
	if err := WriteLimits(&out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Пул учёток: темп 96% недели учётки в сутки") || !strings.Contains(out.String(), "пул кончится") {
		t.Errorf("сводка без прогноза пула:\n%s", out.String())
	}
}

// renderRaw — строка статуса с цветами для замера недели
func renderRaw(t *testing.T, session string, week int, resets time.Time) string {
	t.Helper()
	input := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"rate_limits":{"seven_day":{"used_percentage":%d,"resets_at":%d}}}`,
		session, t.TempDir(), week, resets.Unix())
	line, err := Render(strings.NewReader(input), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	return line
}
