package skillreload

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture — скилл, файл инструкций и транскрипт сессии во временном каталоге
type fixture struct {
	t          *testing.T
	root       string
	skillDir   string
	memory     string
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
		memory:     filepath.Join(root, "proj", "CLAUDE.md"),
		transcript: filepath.Join(root, "projects", "p", "sess-1.jsonl"),
		checker:    Checker{StateDir: filepath.Join(root, "state")},
	}
	for _, dir := range []string{f.skillDir, filepath.Dir(f.memory), filepath.Dir(f.transcript)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

const demoFrontmatter = "---\nname: demo\ndescription: тестовый скилл\n---\n\n"

// writeFile пишет файл с временем изменения at
func (f *fixture) writeFile(path, text string, at time.Time) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
}

// writeSkill пишет SKILL.md с телом body и временем изменения at
func (f *fixture) writeSkill(body string, at time.Time) {
	f.t.Helper()
	f.writeFile(filepath.Join(f.skillDir, skillFileName), demoFrontmatter+body+"\n", at)
}

func jsonLine(t *testing.T, record map[string]any) string {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func stamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}

// snapshotLine — запись транскрипта, которой Claude Code кладёт скилл в контекст
func snapshotLine(t *testing.T, dir, body string, at time.Time) string {
	t.Helper()
	return jsonLine(t, map[string]any{
		"type":            "user",
		"isMeta":          true,
		"timestamp":       stamp(at),
		"sourceToolUseID": "toolu_1",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": loadHeader + dir + "\n\n" + body}},
		},
	})
}

// attachmentLine — вложение Claude Code
func attachmentLine(t *testing.T, at time.Time, attachment map[string]any) string {
	t.Helper()
	return jsonLine(t, map[string]any{"type": "attachment", "timestamp": stamp(at), "attachment": attachment})
}

// instructionsLine — список инструкций при старте или после сжатия
func (f *fixture) instructionsLine(content string, at time.Time) string {
	return attachmentLine(f.t, at, map[string]any{
		"type":  "instructions",
		"files": []map[string]string{{"path": f.memory, "type": "Project", "content": content}},
	})
}

// noteLine — заметка Claude Code «файл изменён на диске»
func (f *fixture) noteLine(path, snippet string, at time.Time) string {
	return attachmentLine(f.t, at, map[string]any{"type": "edited_text_file", "filename": path, "snippet": snippet})
}

// compactLines — сжатие: граница и переложенный список инструкций
func (f *fixture) compactLines(memoryContent string, at time.Time) []string {
	lines := []string{jsonLine(f.t, map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": stamp(at)})}
	if memoryContent != "" {
		lines = append(lines, f.instructionsLine(memoryContent, at))
	}
	return lines
}

// assistantLine — ответ модели, возможно с вызовом инструмента по файлу
func assistantLine(t *testing.T, at time.Time, tool, path string) string {
	t.Helper()
	content := []map[string]any{{"type": "text", "text": "ок"}}
	if tool != "" {
		content = append(content, map[string]any{"type": "tool_use", "id": "toolu_2", "name": tool, "input": map[string]any{"file_path": path}})
	}
	return jsonLine(t, map[string]any{"type": "assistant", "timestamp": stamp(at), "message": map[string]any{"role": "assistant", "content": content}})
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

// load дописывает вызов скилла
func (f *fixture) load(body string, at time.Time) {
	f.t.Helper()
	f.appendTranscript(snapshotLine(f.t, f.skillDir, body, at))
}

func (f *fixture) check(event Event) []Notice {
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
	for _, w := range result.Warnings {
		f.t.Logf("warning: %v", w)
	}
	return result.Notices
}

// wantDiff проверяет одну выдачу с diff, где есть и нет нужные строки
func wantDiff(t *testing.T, got []Notice, has, hasNot []string) Notice {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("ждали одну выдачу, получили %d: %+v", len(got), got)
	}
	n := got[0]
	if n.Diff == "" {
		t.Fatalf("ждали diff, получили выдачу без него: %+v", n)
	}
	for _, s := range has {
		if !strings.Contains(n.Diff, s) {
			t.Errorf("в diff нет %q:\n%s", s, n.Diff)
		}
	}
	for _, s := range hasNot {
		if strings.Contains(n.Diff, s) {
			t.Errorf("в diff лишнее %q:\n%s", s, n.Diff)
		}
	}
	return n
}

func TestSkillDiffOncePerEditFromDeliveredText(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("# Правила\nВерсия 1.", base)
	f.load("# Правила\nВерсия 1.", base.Add(time.Minute))

	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("скилл не менялся, а выдач %d", len(got))
	}

	f.writeSkill("# Правила\nВерсия 2.", base.Add(10*time.Minute))
	n := wantDiff(t, f.check(Event{}), []string{"-Версия 1.", "+Версия 2.", "@@ -2 +2 @@ Правила"}, nil)
	if n.Name != "demo" || n.Kind != kindSkill {
		t.Errorf("выдача не о скилле demo: %+v", n)
	}
	if !strings.Contains(Format([]Notice{n}), "перечитывать не нужно") {
		t.Errorf("текст выдачи с diff просит перечитать: %s", Format([]Notice{n}))
	}

	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("о той же правке сказали повторно: %+v", got)
	}

	// Следующий diff — от показанного текста, а не от снимка вызова
	f.writeSkill("# Правила\nВерсия 3.", base.Add(20*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"-Версия 2.", "+Версия 3."}, []string{"Версия 1."})
}

func TestBaseSurvivesHookRestart(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("А.", base)
	f.load("А.", base.Add(time.Minute))
	f.check(Event{})
	f.writeSkill("Б.", base.Add(2*time.Minute))
	f.check(Event{})

	// Новый процесс хука: только каталог записей
	f.checker = Checker{StateDir: f.checker.StateDir}
	f.writeSkill("В.", base.Add(3*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"-Б.", "+В."}, []string{"А."})
}

func TestLargeEditFallsBackToReload(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("# Шаги\nкоротко.\n# Итог\nготово.", base)
	f.load("# Шаги\nкоротко.\n# Итог\nготово.", base.Add(time.Minute))
	f.check(Event{})

	long := strings.Repeat("длинное правило ", 200)
	f.writeSkill("# Шаги\n"+long+"\n# Итог\nготово.", base.Add(5*time.Minute))
	got := f.check(Event{})
	if len(got) != 1 || got[0].Diff != "" || got[0].DiffBytes <= diffLimit {
		t.Fatalf("правка больше %d Б: ждали выдачу без diff, получили %+v", diffLimit, got)
	}
	if strings.Join(got[0].Sections, ",") != "Шаги" {
		t.Errorf("изменённые разделы %v, ждали [Шаги]", got[0].Sections)
	}
	text := Format(got)
	for _, want := range []string{"правка большая", "«Шаги»", "Вызови Skill demo заново"} {
		if !strings.Contains(text, want) {
			t.Errorf("в выдаче нет %q: %s", want, text)
		}
	}
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("о большой правке сказали повторно: %d", len(got))
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
		t.Fatalf("время изменилось без правки текста, а выдач %d", len(got))
	}
}

func TestRevertToSnapshotAfterNoticeIsShown(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Исходный.", base)
	f.load("Исходный.", base.Add(time.Minute))
	f.check(Event{})

	f.writeSkill("Правка.", base.Add(5*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"+Правка."}, nil)
	// Агент уже работает по правке: откат — тоже правка
	f.writeSkill("Исходный.", base.Add(6*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"-Правка.", "+Исходный."}, nil)
}

func TestReloadResetsBase(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Старый.", base)
	f.load("Старый.", base.Add(time.Minute))
	f.check(Event{})

	f.writeSkill("Новый.", base.Add(5*time.Minute))
	f.load("Новый.", base.Add(6*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("скилл вызван заново, а выдач %d", len(got))
	}
	f.writeSkill("Новейший.", base.Add(7*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"-Новый.", "+Новейший."}, nil)
}

func TestCompactionRestoresOldSnapshot(t *testing.T) {
	// Сжатие подкладывает снимок вызова, а не текст с показанной правкой
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Снимок.", base)
	f.load("Снимок.", base.Add(time.Minute))
	f.check(Event{})
	f.writeSkill("Правка.", base.Add(5*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"+Правка."}, nil)

	f.appendTranscript(f.compactLines("", base.Add(10*time.Minute))...)
	f.appendTranscript(attachmentLine(t, base.Add(10*time.Minute), map[string]any{
		"type":   "invoked_skills",
		"skills": []map[string]string{{"name": "demo", "path": "userSettings:demo", "content": loadHeader + f.skillDir + "\n\nСнимок."}},
	}))
	wantDiff(t, f.check(Event{}), []string{"-Снимок.", "+Правка."}, nil)
}

func TestOwnEditIsNotShown(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("До.", base)
	f.load("До.", base.Add(time.Minute))
	f.check(Event{})

	f.writeSkill("После.", base.Add(2*time.Minute))
	f.appendTranscript(assistantLine(t, base.Add(2*time.Minute), "Edit", filepath.Join(f.skillDir, skillFileName)))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("агент сам изменил файл, а ему показали правку: %+v", got)
	}
	f.appendTranscript(assistantLine(t, base.Add(3*time.Minute), "", ""))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("после ответа модели своя правка всплыла как чужая: %+v", got)
	}
}

func TestFirstSightComparesSnapshot(t *testing.T) {
	// Сессия жила до установки хука: вызов давний, файл с тех пор правили
	f := newFixture(t)
	base := time.Now().Add(-48 * time.Hour)
	f.load("Как было.", base)
	f.writeSkill("Как стало.", base.Add(time.Hour))
	wantDiff(t, f.check(Event{}), []string{"-Как было.", "+Как стало."}, nil)

	g := newFixture(t)
	g.load("Тот же.", base)
	g.writeSkill("Тот же.", base.Add(time.Hour))
	if got := g.check(Event{}); len(got) != 0 {
		t.Fatalf("текст совпал со снимком, а выдач %d", len(got))
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
		t.Fatalf("тот же текст после подстановок, а выдач %d", len(got))
	}
}

func TestMemoryFirstEditLeftToClaudeCode(t *testing.T) {
	// При старте Claude Code сам показывает первую правку CLAUDE.md
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "Раздел\n- правило 1\n", base)
	f.appendTranscript(f.instructionsLine("Раздел\n- правило 1", base.Add(time.Minute)))
	f.check(Event{})

	f.writeFile(f.memory, "Раздел\n- правило 2\n", base.Add(5*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("первую правку покажет Claude Code, а хук выдал %+v", got)
	}
	f.appendTranscript(f.noteLine(f.memory, "1\tРаздел\n2\t- правило 2", base.Add(5*time.Minute)), assistantLine(t, base.Add(5*time.Minute), "", ""))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("Claude Code показал правку, а хук повторил её: %+v", got)
	}

	// После своей заметки Claude Code файл не отслеживает: правку несёт хук
	f.writeFile(f.memory, "Раздел\n- правило 3\n", base.Add(10*time.Minute))
	n := wantDiff(t, f.check(Event{}), []string{"-- правило 2", "+- правило 3", "@@ -2 +2 @@ Раздел"}, []string{"правило 1"})
	if n.Kind != kindMemory || !strings.Contains(Format([]Notice{n}), "Файл инструкций "+f.memory) {
		t.Errorf("выдача не о файле инструкций: %s", Format([]Notice{n}))
	}
}

func TestMemoryEditMissedByClaudeCodeIsShown(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "- было\n", base)
	f.appendTranscript(f.instructionsLine("- было", base.Add(time.Minute)))
	f.check(Event{})

	f.writeFile(f.memory, "- стало\n", base.Add(5*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("до ответа модели правку ждут от Claude Code, а хук выдал %+v", got)
	}
	// Второе событие до ответа модели (UserPromptSubmit вслед за PostToolBatch)
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("модель ещё не отвечала, а хук выдал %+v", got)
	}
	f.appendTranscript(assistantLine(t, base.Add(6*time.Minute), "", ""))
	wantDiff(t, f.check(Event{}), []string{"-- было", "+- стало"}, nil)
}

func TestMemoryEmptyNoteIsNotDelivery(t *testing.T) {
	// Заметка без фрагмента («changes are not shown») правку не показала
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "- раз\n", base)
	f.appendTranscript(f.instructionsLine("- раз", base.Add(time.Minute)))
	f.check(Event{})
	f.writeFile(f.memory, "- два\n", base.Add(5*time.Minute))
	f.check(Event{})
	f.appendTranscript(f.noteLine(f.memory, "", base.Add(5*time.Minute)))
	wantDiff(t, f.check(Event{}), []string{"-- раз", "+- два"}, nil)
}

func TestMemoryAfterCompactionShownAtOnce(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "- старт\n", base)
	f.appendTranscript(f.instructionsLine("- старт", base.Add(time.Minute)))
	f.writeFile(f.memory, "- до сжатия\n", base.Add(3*time.Minute))
	// Сжатие переложило текущий текст: правка до сжатия у агента уже есть
	f.appendTranscript(f.compactLines("- до сжатия", base.Add(4*time.Minute))...)
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("сжатие переложило текущий текст, а хук выдал %+v", got)
	}

	f.writeFile(f.memory, "- после сжатия\n", base.Add(10*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"-- до сжатия", "+- после сжатия"}, []string{"старт"})
}

func TestMemoryNestedLostOnCompaction(t *testing.T) {
	// Сжатие не перекладывает подгруженный по ходу файл: Claude Code его больше
	// не отслеживает, правку несёт хук сразу
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "- вложенный\n", base)
	f.appendTranscript(attachmentLine(t, base.Add(time.Minute), map[string]any{
		"type": "nested_memory", "path": f.memory, "content": map[string]any{"path": f.memory, "content": "- вложенный"},
	}))
	f.appendTranscript(f.compactLines("", base.Add(4*time.Minute))...)
	f.writeFile(f.memory, "- вложенный, правка\n", base.Add(10*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"+- вложенный, правка"}, nil)
}

func TestStaleLoadShownAtOnce(t *testing.T) {
	// Claude Code положил при старте прежнюю версию из кеша: файл изменён
	// раньше загрузки, заметки о нём не будет
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "- новое\n", base)
	f.appendTranscript(f.instructionsLine("- старое", base.Add(time.Minute)))
	wantDiff(t, f.check(Event{}), []string{"-- старое", "+- новое"}, nil)
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("о той же правке сказали повторно: %+v", got)
	}
}

func TestMemoryCommentsAreNotChanges(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeFile(f.memory, "- правило\n<!--\nзаметка для людей\n-->\n", base)
	f.appendTranscript(f.instructionsLine("- правило", base.Add(time.Minute)))
	f.appendTranscript(f.compactLines("- правило", base.Add(2*time.Minute))...)
	f.writeFile(f.memory, "- правило\n<!--\nдругая заметка\n-->\n", base.Add(5*time.Minute))
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("изменился только HTML-комментарий, которого нет в контексте, а выдач %d", len(got))
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
	// Результат инструмента с копией вложения инструкций (вывод jq по транскрипту)
	copied := `{"type":"attachment","attachment":{"type":"instructions","files":[{"path":"` + f.memory + `","content":"x"}]}}`
	result, _ := json.Marshal(map[string]any{"type": "user", "timestamp": stamp(base), "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "content": text + copied}}}})
	f.appendTranscript(string(blob)+"\n", string(result)+"\n")

	got, err := f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if got.NewLoads != 0 {
		t.Fatalf("копии текста засчитаны как загрузка: %d", got.NewLoads)
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
		t.Fatalf("субагент без вызова получил выдачу: %+v", got)
	}
	if got := f.check(Event{AgentID: "a2", AgentType: "fork"}); len(got) != 1 {
		t.Fatalf("форк с унаследованным снимком не получил выдачу: %d", len(got))
	}
	if got := f.check(Event{}); len(got) != 1 {
		t.Fatalf("основной разговор не получил выдачу: %d", len(got))
	}
}

func TestConcurrentEventsNotifyOnce(t *testing.T) {
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
			total += len(result.Notices)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("одна правка — одна выдача, а пришло %d", total)
	}
}

func TestScanBudgetDefersNotices(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.load("Давний.", base)
	f.writeSkill("Изменённый.", base.Add(time.Minute))
	filler := `{"type":"user","message":{"content":"x"}}` + "\n"
	f.appendTranscript(strings.Repeat(filler, 5000))

	f.checker.ScanBudget = time.Nanosecond
	result, err := f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || len(result.Notices) != 0 {
		t.Fatalf("недочитанный транскрипт: ждали отложенную выдачу, получили pending=%v, %d", result.Pending, len(result.Notices))
	}

	f.checker.ScanBudget = 0
	result, err = f.checker.Check(Event{SessionID: "sess-1", TranscriptPath: f.transcript})
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending || len(result.Notices) != 1 {
		t.Fatalf("дочитанный транскрипт: ждали одну выдачу, получили pending=%v, %d", result.Pending, len(result.Notices))
	}
}

func TestSchema1StateKeepsAnnouncedEdit(t *testing.T) {
	// Прежняя версия хука уже сказала «вызови заново» об этой правке
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.load("Старое.", base)
	f.writeSkill("Новое.", base.Add(time.Minute))
	raw, err := os.ReadFile(filepath.Join(f.skillDir, skillFileName))
	if err != nil {
		t.Fatal(err)
	}
	schema1 := `{"transcript":"` + f.transcript + `","offset":1,"skills":{"` + f.skillDir + `":{"name":"demo","baseline":"x","notified":"` + contentHash(raw) + `"}}}`
	if err := os.MkdirAll(f.checker.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.checker.StateDir, "sess-1.json"), []byte(schema1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.check(Event{}); len(got) != 0 {
		t.Fatalf("правку уже объявила прежняя версия, а выдач %d", len(got))
	}
	f.writeSkill("Новейшее.", base.Add(2*time.Minute))
	wantDiff(t, f.check(Event{}), []string{"-Новое.", "+Новейшее."}, nil)
}

func TestLostBaseAsksToReload(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-time.Hour)
	f.writeSkill("Раз.", base)
	f.load("Раз.", base.Add(time.Minute))
	f.check(Event{})
	if err := os.RemoveAll(newDirStore(f.checker.StateDir).dir); err != nil {
		t.Fatal(err)
	}
	f.writeSkill("Два.", base.Add(2*time.Minute))
	got := f.check(Event{})
	if len(got) != 1 || !got[0].Lost || !strings.Contains(Format(got), "Вызови Skill demo заново") {
		t.Fatalf("текст в контексте потерян: ждали просьбу вызвать заново, получили %+v", got)
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

func TestLineDiffSections(t *testing.T) {
	old := "Вступление\n\nТексты\n- правило А\n- правило Б\n\n## Git\n```bash\n# не заголовок\n```\nстрока"
	updated := "Вступление\n\nТексты\n- правило А\n\n## Git\n```bash\n# не заголовок\n```\nновая строка"
	diff, sections := lineDiff(old, updated, "f")
	if strings.Join(sections, "|") != "Тексты|Git" {
		t.Fatalf("разделы %q, ждали [Тексты Git]:\n%s", sections, diff)
	}
	for _, want := range []string{"@@ -5 +4,0 @@ Тексты\n-- правило Б\n", "@@ -11 +10 @@ Git\n-строка\n+новая строка\n"} {
		if !strings.Contains(diff, want) {
			t.Errorf("в diff нет %q:\n%s", want, diff)
		}
	}
}

func TestDryRunReplaysHook(t *testing.T) {
	f := newFixture(t)
	base := time.Now().Add(-2 * time.Hour)
	f.writeSkill("Тело 1.", base)
	hook := func(at time.Time) string {
		return jsonLine(t, map[string]any{"type": "user", "timestamp": stamp(at), "message": map[string]any{"content": "ход"}})
	}
	// Вызов, затем ход после правки, которую видел другой транскрипт
	f.appendTranscript(snapshotLine(t, f.skillDir, "Тело 1.", base.Add(time.Minute)), hook(base.Add(2*time.Minute)), hook(base.Add(40*time.Minute)), hook(base.Add(50*time.Minute)))
	other := filepath.Join(filepath.Dir(f.transcript), "sess-2.jsonl")
	if err := os.WriteFile(other, []byte(snapshotLine(t, f.skillDir, "Тело 2.", base.Add(30*time.Minute))), 0o600); err != nil {
		t.Fatal(err)
	}
	f.writeSkill("Тело 2.", base.Add(20*time.Minute))
	// Третий транскрипт получил из кеша Claude Code прежний текст: это не
	// откат файла, сессии первого транскрипта правки он не даёт
	stale := filepath.Join(filepath.Dir(f.transcript), "sess-3.jsonl")
	if err := os.WriteFile(stale, []byte(snapshotLine(t, f.skillDir, "Тело 1.", base.Add(45*time.Minute))), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := DryRun(context.Background(), filepath.Dir(filepath.Dir(f.transcript)), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range result.Notices {
		n := &result.Notices[i]
		got = append(got, filepath.Base(n.Transcript)+": "+strings.ReplaceAll(n.Notice.Diff, "\n", " | "))
	}
	// sess-1: правка 1→2 одна; sess-3: в контексте прежний текст, ему diff 1→2
	if len(got) != 2 || !strings.HasPrefix(got[0], "sess-1.jsonl") || !strings.Contains(got[0], "+Тело 2.") ||
		!strings.HasPrefix(got[1], "sess-3.jsonl") || !strings.Contains(got[1], "+Тело 2.") {
		t.Fatalf("ждали по выдаче 1→2 в sess-1 и sess-3, получили %q", got)
	}
	if len(result.RenderMismatch) != 1 || !result.RenderMismatch[0].Stale {
		t.Fatalf("загрузка прежнего текста не опознана: %+v", result.RenderMismatch)
	}
}
