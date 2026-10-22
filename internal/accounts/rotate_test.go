package accounts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const day = 24 * time.Hour

// Новый /login опознаётся по API и сохраняется вместе с oauthAccount
func TestSaveNewLogin(t *testing.T) {
	f := newFixture(t)
	f.save("work")
	s := f.store()
	if len(s.Accounts) != 1 || s.ActiveEmail() != work.email {
		t.Fatalf("хранилище: %d учёток, активная %q", len(s.Accounts), s.ActiveEmail())
	}
	a := s.Accounts[0]
	if a.UUID != work.uuid || a.OrgUUID != "org-work" || !strings.Contains(string(a.OAuthAccount), `"displayName": "work"`) {
		t.Errorf("учётка сохранена не полностью: %s %s %s", a.UUID, a.OrgUUID, a.OAuthAccount)
	}
	info, err := os.Stat(f.p.Store())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права хранилища %o, ожидалось 600", mode)
	}
	if f.profiles != 1 {
		t.Errorf("запросов профиля %d, ожидался 1", f.profiles)
	}
}

// Claude Code обновил токен активной учётки: новый токен уходит в
// хранилище без сети — вход узнаётся по сроку цепочки
func TestSyncFollowsClaudeRefresh(t *testing.T) {
	f := newFixture(t)
	f.save("work")
	f.writeCreds(f.login("work", 2, 8*time.Hour))
	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if got := f.storedRefresh(work.email); got != refreshPrefix+"work-2" {
		t.Errorf("в хранилище не обновлённый токен (поколение %s)", strings.TrimPrefix(got, refreshPrefix+"work-"))
	}
	if f.profiles != 1 {
		t.Errorf("обновление токена спрашивало профиль: %d запросов", f.profiles)
	}
}

// Файл, изменённый только что, сверяется после settle: /login пишет
// токены и учётку не одним движением
func TestSyncWaitsForSettle(t *testing.T) {
	f := newFixture(t)
	f.save("work")
	f.writeCreds(f.login("work", 2, 8*time.Hour))
	f.age(f.p.Credentials(), time.Second)
	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if got := f.storedRefresh(work.email); got != refreshPrefix+"work-1" {
		t.Error("файл сверен до settle")
	}
}

// Claude Code стёр токены (обновление отклонено): учётка мёртвая
func TestSyncMarksClearedLoginDead(t *testing.T) {
	f := newFixture(t)
	f.save("work")
	f.writeCreds(json.RawMessage(`{"accessToken":"","refreshToken":"","expiresAt":null}`))
	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if a := f.store().Accounts[0]; a.Dead == "" {
		t.Error("стёртый вход не помечен мёртвым")
	}
}

// twoAccounts — личная сохранена, затем рабочая: активна рабочая
func twoAccounts(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.save("personal")
	f.save("work")
	if got := f.store().ActiveEmail(); got != work.email {
		t.Fatalf("активна %q", got)
	}
	return f
}

// Правило пользователя: активная пуста — на непустую с ближайшим сбросом.
// Неделя неактивной неизвестна — она спрашивается у API
func TestRotateSwitchesFromEmptyAccount(t *testing.T) {
	f := twoAccounts(t)
	// Сохранение второй учётки уже спрашивало расход личной: следующий запрос — после usageStale
	f.now = f.now.Add(usageStale)
	f.limits.set(work.email, 100, f.now.Add(4*day))
	resets := f.now.Add(2 * day).Truncate(time.Minute)
	f.usage["personal"] = fmt.Sprintf(`{"five_hour":{"utilization":3,"resets_at":%q},"seven_day":{"utilization":10,"resets_at":%q}}`,
		f.now.Add(time.Hour).Format(time.RFC3339), resets.Add(-50*time.Millisecond).Format(time.RFC3339Nano))

	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if u := f.limits.usage[personal.email]; u.Week.Used != 10 || !u.Week.ResetsAt.Equal(resets) {
		t.Errorf("расход личной не записан или сброс не округлён: %+v", u.Week)
	}
	o, fields := f.creds()
	if o.RefreshToken != refreshPrefix+"personal-1" {
		t.Fatal("в файле Claude Code не вход личной")
	}
	if string(fields["mcpOAuth"]) != `{"srv":{"accessToken":"mcp-value"}}` {
		t.Errorf("чужое поле файла токенов изменилось: %s", fields["mcpOAuth"])
	}
	info, err := os.Stat(f.p.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права файла токенов %o", mode)
	}

	config, err := os.ReadFile(f.p.GlobalConfig)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"numStartups\": 5,\n  \"oauthAccount\": {\n    \"accountUuid\": \"u-personal\",\n    \"emailAddress\": \"personal@example.com\",\n    \"displayName\": \"personal\"\n  },\n  \"zz\": true\n}"
	if string(config) != want {
		t.Errorf("~/.claude.json:\n%s\nожидалось:\n%s", config, want)
	}
	if info, err := os.Stat(f.p.GlobalConfig); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("права ~/.claude.json изменились: %v %v", info.Mode(), err)
	}

	s := f.store()
	if s.ActiveEmail() != personal.email || !s.LastSwitch.Equal(f.now) {
		t.Errorf("хранилище: активна %q, переключение %v", s.ActiveEmail(), s.LastSwitch)
	}
	journal := readJournal(t, f)
	if len(journal) != 1 || journal[0].From != work.email || journal[0].To != personal.email || !strings.Contains(journal[0].Reason, "пуста") {
		t.Errorf("журнал переключений: %+v", journal)
	}

	// Сразу после переключения ротация выжидает: сессии ещё не принесли долю
	f.limits.set(personal.email, 99, resets)
	f.limits.set(work.email, 0, time.Time{})
	f.now = f.now.Add(time.Minute)
	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if f.store().ActiveEmail() != personal.email {
		t.Error("переключение в паузе после прошлого")
	}
}

// Обе непусты: активна та, чья неделя сбросится раньше
func TestRotatePrefersNearestReset(t *testing.T) {
	f := twoAccounts(t)
	f.limits.set(work.email, 40, f.now.Add(5*day))
	f.limits.set(personal.email, 70, f.now.Add(1*day))
	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if f.store().ActiveEmail() != personal.email {
		t.Error("не переключилась на учётку с ближайшим сбросом")
	}
	if f.refreshes != 0 {
		t.Errorf("живой токен обновлялся: %d", f.refreshes)
	}
}

// Перед подменой токен активной учётки из файла уходит в хранилище: Claude
// Code мог обновить его после последней сверки
func TestSwitchSavesActiveTokenFirst(t *testing.T) {
	f := twoAccounts(t)
	f.writeCreds(f.login("work", 2, 8*time.Hour))
	s := f.store()
	target, err := s.find(personal.email)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.switchTo(context.Background(), s, target, "тест"); err != nil {
		t.Fatal(err)
	}
	if err := saveStore(f.p, s); err != nil {
		t.Fatal(err)
	}
	if got := f.storedRefresh(work.email); got != refreshPrefix+"work-2" {
		t.Error("обновлённый токен активной учётки потерян при переключении")
	}
}

// Токен доступа цели истекает: он обновляется до подмены, ответ сразу в хранилище
func TestSwitchRefreshesExpiringTarget(t *testing.T) {
	f := newFixture(t)
	f.writeConfig(personal)
	f.writeCreds(f.login("personal", 1, 3*time.Minute))
	if _, err := f.r.Rotate(context.Background(), Options{Force: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	f.save("work")
	email, err := f.r.Switch(context.Background(), "PERSONAL@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if email != personal.email || f.refreshes != 1 {
		t.Fatalf("переключено на %q, обновлений %d", email, f.refreshes)
	}
	o, _ := f.creds()
	if o.RefreshToken != refreshPrefix+"personal-2" || !o.expires().After(f.now.Add(7*time.Hour)) {
		t.Error("в файле не обновлённый вход цели")
	}
	s := f.store()
	if !s.AutoOff {
		t.Error("ручное переключение не выключило автоматическое")
	}
	if got := f.storedRefresh(personal.email); got != refreshPrefix+"personal-2" {
		t.Error("обновлённый токен цели не в хранилище")
	}
}

// Сервер отклонил обновление цели: цель мёртвая, Claude Code остаётся на
// прежней учётке, ответ сервера с эхом токена не доходит до ошибки
func TestSwitchToDeadLogin(t *testing.T) {
	f := newFixture(t)
	f.writeConfig(personal)
	f.writeCreds(f.login("personal", 1, time.Minute))
	if _, err := f.r.Rotate(context.Background(), Options{Force: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	f.dead["personal"] = true
	f.save("work")
	f.limits.set(work.email, 99, f.now.Add(3*day))
	f.limits.set(personal.email, 5, f.now.Add(day))

	if _, err := f.r.Rotate(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	s := f.store()
	if s.ActiveEmail() != work.email {
		t.Error("переключилась на мёртвый вход")
	}
	if a, _ := s.find(personal.email); a.Dead == "" {
		t.Error("отклонённый вход не помечен мёртвым")
	}
	if o, _ := f.creds(); o.RefreshToken != refreshPrefix+"work-1" {
		t.Error("файл Claude Code изменился")
	}

	_, err := f.r.Switch(context.Background(), personal.email)
	if err == nil {
		t.Fatal("ручное переключение на мёртвый вход прошло")
	}
	assertNoSecrets(t, "ошибка", err.Error())
}

// В файле вход, которого хранилище не знает: подмена отложена, вход не потерян
func TestSwitchPostponedForUnknownLogin(t *testing.T) {
	f := twoAccounts(t)
	f.people["third"] = person{name: "third", uuid: "u-third", email: "third@example.com", family: f.now.Add(28 * day)}
	f.writeCreds(f.login("third", 1, 8*time.Hour))
	s := f.store()
	target, _ := s.find(personal.email)
	if err := f.r.switchTo(context.Background(), s, target, "тест"); err == nil {
		t.Fatal("подмена затёрла несохранённый вход")
	}
	if o, _ := f.creds(); o.RefreshToken != refreshPrefix+"third-1" {
		t.Error("файл Claude Code изменился")
	}
	if !s.SyncedMtime.IsZero() {
		t.Error("сверка не назначена заново")
	}
}

// Живая блокировка Claude Code ждётся, брошенная — снимается
func TestSwitchHonoursClaudeLocks(t *testing.T) {
	f := twoAccounts(t)
	saved := lockWait
	lockWait = 300 * time.Millisecond
	t.Cleanup(func() { lockWait = saved })
	if err := os.Mkdir(f.p.refreshLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	s := f.store()
	target, _ := s.find(personal.email)
	if err := f.r.switchTo(context.Background(), s, target, "тест"); !errors.Is(err, errLockBusy) {
		t.Fatalf("занятая блокировка: %v", err)
	}
	if o, _ := f.creds(); o.RefreshToken != refreshPrefix+"work-1" {
		t.Fatal("файл подменён под чужой блокировкой")
	}
	f.age(f.p.refreshLock(), 2*refreshLockStale)
	if err := f.r.switchTo(context.Background(), s, target, "тест"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.p.refreshLock()); !os.IsNotExist(err) {
		t.Error("блокировка не снята после переключения")
	}
	if o, _ := f.creds(); o.RefreshToken != refreshPrefix+"personal-1" {
		t.Error("брошенная блокировка помешала переключению")
	}
}

// Строка статуса запускает ротацию только при деле для неё
func TestPending(t *testing.T) {
	f := twoAccounts(t)
	s := f.store()
	f.limits.set(work.email, 50, f.now.Add(3*day))
	f.limits.set(personal.email, 50, f.now.Add(4*day))
	usage, _ := f.limits.Usage()
	check := func(name, want string) {
		t.Helper()
		got, err := pending(f.p, s, usage, f.now)
		if err != nil {
			t.Fatal(err)
		}
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: причина %q, ожидалось %q", name, got, want)
		}
	}
	check("активная с ближайшим сбросом", "")

	usage[work.email] = Usage{Week: Window{Used: 100, ResetsAt: f.now.Add(3 * day)}}
	check("активная пуста", "пуста")
	s.AutoOff = true
	check("автопереключение выключено", "")
	s.AutoOff = false
	s.LastSwitch = f.now.Add(-time.Minute)
	check("пауза после переключения", "")
	s.LastSwitch = time.Time{}

	delete(usage, personal.email)
	a, _ := s.find(personal.email)
	a.UsageCheckedAt = time.Time{}
	check("неделя неактивной неизвестна", "неизвестна")
	a.UsageCheckedAt = f.now.Add(-time.Minute)
	check("расход спрошен недавно", "пуста")

	f.writeCreds(f.login("work", 2, 8*time.Hour))
	check("файл токенов изменился", "файл токенов")
	f.age(f.p.Credentials(), time.Second)
	s.AutoOff = true
	check("файл меняется прямо сейчас", "")

	s.RetryAt = f.now.Add(time.Minute)
	f.age(f.p.Credentials(), time.Minute)
	check("пауза после сетевой ошибки", "")
}

// Auto и forget правят хранилище под его блокировкой
func TestAutoAndForget(t *testing.T) {
	f := twoAccounts(t)
	if err := SetAuto(f.p, false); err != nil {
		t.Fatal(err)
	}
	if !f.store().AutoOff {
		t.Error("auto off не записан")
	}
	email, err := Forget(f.p, work.email)
	if err != nil || email != work.email {
		t.Fatalf("forget: %q %v", email, err)
	}
	s := f.store()
	if len(s.Accounts) != 1 || s.Active != "" {
		t.Errorf("после forget: %d учёток, активна %q", len(s.Accounts), s.Active)
	}
	if _, err := Forget(f.p, "nobody@example.com"); err == nil {
		t.Error("forget неизвестной прошёл")
	}
}

// Сводка учёток без токенов
func TestInfosHaveNoSecrets(t *testing.T) {
	f := twoAccounts(t)
	infos := f.store().Infos()
	if len(infos) != 2 || !infos[1].Active || !infos[0].LoginExpires.Equal(time.UnixMilli(f.person("personal").family.UnixMilli())) {
		t.Errorf("сводка: %+v", infos)
	}
	assertNoSecrets(t, "сводка", fmt.Sprintf("%+v", infos))
}

func readJournal(t *testing.T, f *fixture) []switchRecord {
	t.Helper()
	file, err := os.Open(f.p.Journal())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []switchRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		assertNoSecrets(t, "журнал переключений", scanner.Text())
		var rec switchRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}
