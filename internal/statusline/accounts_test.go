package statusline

import (
	"context"
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
	line, err := Render(context.Background(), strings.NewReader(input), testLogger(t))
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

	if line := renderInput(t, "s1", 40, workNext, 12, fiveNext); !strings.Contains(line, "нед·work") || !strings.Contains(line, "40%") {
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
	if line := renderInput(t, "s2", 40, workNext, 12, fiveNext); !strings.Contains(line, "нед·work") {
		t.Errorf("старая сессия: %q", line)
	}
	personalNext := now.Add(6 * 24 * time.Hour).Truncate(time.Hour)
	if line := renderInput(t, "s3", 1, personalNext, 1, fiveNext.Add(time.Hour)); !strings.Contains(line, "нед·personal") {
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
