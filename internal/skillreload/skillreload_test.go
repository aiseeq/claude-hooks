package skillreload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture — скилл и транскрипт сессии во временном каталоге
type fixture struct {
	t          *testing.T
	root       string
	skillDir   string
	transcript string
	checker    Checker
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:          t,
		root:       root,
		skillDir:   filepath.Join(root, "skills", "demo"),
		transcript: filepath.Join(root, "projects", "p", "sess-1.jsonl"),
		checker:    Checker{StateDir: filepath.Join(root, "state")},
	}
	if err := os.MkdirAll(f.skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

const demoFrontmatter = "---\nname: demo\ndescription: тестовый скилл\n---\n\n"

// writeSkill пишет SKILL.md с телом body и временем изменения at
func (f *fixture) writeSkill(body string, at time.Time) {
	f.t.Helper()
	path := filepath.Join(f.skillDir, skillFileName)
	if err := os.WriteFile(path, []byte(demoFrontmatter+body+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
}

// snapshotLine — запись транскрипта, которой Claude Code кладёт скилл в контекст
func snapshotLine(t *testing.T, dir, body string, at time.Time) string {
	t.Helper()
	record := map[string]any{
		"type":            "user",
		"isMeta":          true,
		"timestamp":       at.UTC().Format(time.RFC3339Nano),
		"sourceToolUseID": "toolu_1",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]string{
				{"type": "text", "text": loadHeader + dir + "\n\n" + body},
			},
		},
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

// appendTranscript дописывает строки в транскрипт
func (f *fixture) appendTranscript(lines ...string) {
	f.t.Helper()
	file, err := os.OpenFile(f.transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	for _, line := range lines {
		if _, err := file.WriteString(line); err != nil {
			f.t.Fatal(err)
		}
	}
}

// load дописывает вызов скилла с текущим телом файла
func (f *fixture) load(body string, at time.Time) {
	f.t.Helper()
	f.appendTranscript(snapshotLine(f.t, f.skillDir, body, at))
}

func (f *fixture) check(event Event) []Reminder {
	f.t.Helper()
	if event.SessionID == "" {
		event.SessionID = "sess-1"
	}
	if event.TranscriptPath == "" {
		event.TranscriptPath = f.transcript
	}
	result, err := f.checker.Check(event)
	if err != nil {
		f.t.Fatalf("Check: %v", err)
	}
	return result.Reminders
}

func TestRemindsOncePerEdit(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Версия 1.", base)
	f.load("Версия 1.", base.Add(time.Minute))

	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("скилл не менялся, а напоминаний %d", len(got))
	}

	f.writeSkill("Версия 2.", base.Add(10*time.Minute))
	got := f.check(Event{})
	if len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("после правки ждали одно напоминание о demo, получили %+v", got)
	}
	if !got[0].LoadedAt.Equal(base.Add(time.Minute)) {
		t.Errorf("время вызова %v, ждали %v", got[0].LoadedAt, base.Add(time.Minute))
	}

	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("о той же правке напомнили повторно: %+v", got)
	}

	f.writeSkill("Версия 3.", base.Add(20*time.Minute))
	if got := f.check(Event{}); len(got) != 1 {
		t.Fatalf("о новой правке ждали напоминание, получили %d", len(got))
	}
}

func TestTouchWithoutEditIsSilent(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Текст.", base)
	f.load("Текст.", base.Add(time.Minute))
	f.check(Event{})

	// Сон и выкладка конфигурации меняют время файла, не трогая текст
	f.writeSkill("Текст.", base.Add(30*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("время изменилось без правки текста, а напоминаний %d", len(got))
	}
}

func TestRevertToSnapshotIsSilent(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Исходный.", base)
	f.load("Исходный.", base.Add(time.Minute))
	f.check(Event{})

	f.writeSkill("Правка.", base.Add(5*time.Minute))
	if got := f.check(Event{}); len(got) != 1 {
		t.Fatalf("ждали напоминание о правке, получили %d", len(got))
	}
	f.writeSkill("Исходный.", base.Add(6*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("файл вернулся к тексту в контексте, а напоминаний %d", len(got))
	}
}

func TestReloadResetsBaseline(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Старый.", base)
	f.load("Старый.", base.Add(time.Minute))
	f.check(Event{})

	f.writeSkill("Новый.", base.Add(5*time.Minute))
	f.check(Event{})
	f.load("Новый.", base.Add(6*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("скилл вызван заново, а напоминаний %d", len(got))
	}
	f.writeSkill("Новейший.", base.Add(7*time.Minute))
	if got := f.check(Event{}); len(got) != 1 {
		t.Fatalf("правка после нового вызова должна напомнить, получили %d", len(got))
	}
}

func TestFirstSightComparesSnapshot(t *testing.T) {
	// Сессия жила до установки хука: вызов давний, файл с тех пор правили
	f := newFixture(t)
	base := time.Now().Add(-48 * time.Hour)
	f.load("Как было.", base)
	f.writeSkill("Как стало.", base.Add(time.Hour))
	if got := f.check(Event{}); len(got) != 1 {
		t.Fatalf("файл изменён до первого чтения, ждали напоминание, получили %d", len(got))
	}

	g := newFixture(t)
	g.load("Тот же.", base)
	g.writeSkill("Тот же.", base.Add(time.Hour))
	if got := g.check(Event{}); len(got) != 0 {
		t.Fatalf("текст совпал со снимком, а напоминаний %d", len(got))
	}
}

func TestSlashArgumentsAndSkillDirAreRendered(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-48 * time.Hour)
	// Снимок с подставленным каталогом и хвостом аргументов /имя
	snapshot := "Папка " + f.skillDir + ".\n\n\nARGUMENTS: что-то"
	f.load(snapshot, base)
	f.writeSkill("Папка ${CLAUDE_SKILL_DIR}.", base.Add(time.Hour))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("тот же текст после подстановок, а напоминаний %d", len(got))
	}
}

func TestPartialLineWaitsForCompletion(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Один.", base)
	line := snapshotLine(t, f.skillDir, "Один.", base.Add(time.Minute))
	f.appendTranscript(line[:len(line)/2])

	result, err := f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if result.NewLoads != 0 {
		t.Fatalf("недописанная строка засчитана как вызов")
	}

	f.appendTranscript(line[len(line)/2:])
	result, err = f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if result.NewLoads != 1 {
		t.Fatalf("дописанная строка не прочитана: вызовов %d", result.NewLoads)
	}
}

func TestIgnoresCopiesOfSnapshotText(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Тело.", base)
	text := loadHeader + f.skillDir + "\n\nТело."
	blob, _ := json.Marshal(map[string]any{"type": "api-request-blob", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	result, _ := json.Marshal(map[string]any{"type": "user", "timestamp": base.Format(time.RFC3339Nano), "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "content": text}}}})
	f.appendTranscript(string(blob)+"\n", string(result)+"\n")

	got, err := f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if got.NewLoads != 0 {
		t.Fatalf("копии текста скилла засчитаны как вызов: %d", got.NewLoads)
	}
}

func TestSubagentHasOwnTranscriptAndForkInherits(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Родитель.", base)
	f.load("Родитель.", base.Add(time.Minute))
	f.check(Event{})

	agentTranscript := strings.TrimSuffix(f.transcript, ".jsonl") + "/subagents/agent-a1.jsonl"
	if err := os.MkdirAll(filepath.Dir(agentTranscript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentTranscript, []byte(`{"type":"user","message":{"content":"задание"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Обычный субагент скилл не вызывал: родительский вызов не его
	f.check(Event{AgentID: "a1", AgentType: "general-purpose"})
	// Форк унаследовал снимок родителя
	f.check(Event{AgentID: "a2", AgentType: "fork"})

	f.writeSkill("Правка.", base.Add(10*time.Minute))
	if got := f.check(Event{AgentID: "a1", AgentType: "general-purpose"}); len(got) != 0 {
		t.Fatalf("субагент без вызова получил напоминание: %+v", got)
	}
	if got := f.check(Event{AgentID: "a2", AgentType: "fork"}); len(got) != 1 {
		t.Fatalf("форк с унаследованным снимком не получил напоминание: %d", len(got))
	}
	if got := f.check(Event{}); len(got) != 1 {
		t.Fatalf("основной разговор не получил напоминание: %d", len(got))
	}
}

func TestConcurrentEventsRemindOnce(t *testing.T) {
	// UserPromptSubmit и PostToolBatch приходят почти одновременно
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("А.", base)
	f.load("А.", base.Add(time.Minute))
	f.check(Event{})
	f.writeSkill("Б.", base.Add(5*time.Minute))

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			total += len(result.Reminders)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("одна правка — одно напоминание, а пришло %d", total)
	}
}

func TestScanBudgetDefersReminders(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.load("Давний.", base)
	f.writeSkill("Изменённый.", base.Add(time.Minute))
	filler := `{"type":"assistant","message":{"content":"x"}}` + "\n"
	f.appendTranscript(strings.Repeat(filler, 5000))

	f.checker.ScanBudget = time.Nanosecond
	result, err := f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || len(result.Reminders) != 0 {
		t.Fatalf("недочитанный транскрипт: ждали отложенные напоминания, получили pending=%v, %d", result.Pending, len(result.Reminders))
	}

	f.checker.ScanBudget = 0
	result, err = f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending || len(result.Reminders) != 1 {
		t.Fatalf("дочитанный транскрипт: ждали одно напоминание, получили pending=%v, %d", result.Pending, len(result.Reminders))
	}
}

func TestSkillName(t *testing.T) {
	cases := []struct {
		dir, raw, want string
	}{
		{"/h/.claude/skills/web-page", "---\nname: web-page\n---\n", "web-page"},
		{"/h/.claude/skills/dir-name", "без шапки", "dir-name"},
		{"/h/.claude/plugins/cache/official/frontend-design/ea10/skills/frontend-design", "---\nname: frontend-design\n---\n", "frontend-design:frontend-design"},
	}
	for _, c := range cases {
		if got := skillName(c.dir, []byte(c.raw)); got != c.want {
			t.Errorf("skillName(%s) = %q, ждали %q", c.dir, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 37, 0, 0, time.UTC)
	text := Format([]Reminder{{Name: "chat", LoadedAt: at, ChangedAt: at.Add(2 * time.Hour)}})
	for _, want := range []string{"Скилл chat изменён после твоего вызова", "Вызови Skill chat заново"} {
		if !strings.Contains(text, want) {
			t.Errorf("в напоминании нет %q: %s", want, text)
		}
	}
}

func TestSimulateCountsEachEditOnce(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	dir := "/s/demo"
	scan := transcriptScan{
		Path:  "t.jsonl",
		Loads: []Load{{Dir: dir, At: base, Snapshot: "v1"}},
		Events: []time.Time{
			base.Add(time.Minute), base.Add(2 * time.Hour), base.Add(3 * time.Hour), base.Add(5 * time.Hour),
		},
	}
	timelines := map[string][]version{dir: {
		{At: base.Add(-time.Hour), Hash: bodyHash("v1")},
		{At: base.Add(90 * time.Minute), Hash: bodyHash("v2")},
		{At: base.Add(4 * time.Hour), Hash: bodyHash("v3")},
	}}
	got := simulate(scan, timelines, base.Add(-24*time.Hour))
	if len(got) != 2 || !got[0].First || got[1].First {
		t.Fatalf("две правки — два напоминания, первое помечено первым: %+v", got)
	}
}
