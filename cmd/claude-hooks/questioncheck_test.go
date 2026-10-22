package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/questioncheck"
)

// questionDay — день внутри эксперимента: решения в тестах от часов не зависят
var questionDay = time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)

const questionText = "Посчитал. Поменять лимит на 200?"

// stubJudge — судья без живой модели: вердикт, ошибка или ожидание таймаута
type stubJudge struct {
	verdict questioncheck.Verdict
	err     error
	hang    bool
	calls   int
}

func (s *stubJudge) Model() string { return "stub" }

func (s *stubJudge) Judge(ctx context.Context, _ string) (questioncheck.Verdict, questioncheck.Usage, error) {
	s.calls++
	if s.hang {
		<-ctx.Done()
		return questioncheck.Verdict{}, questioncheck.Usage{}, ctx.Err()
	}
	return s.verdict, questioncheck.Usage{}, s.err
}

// questionEnv — домашний каталог с записью сессии и settings.json, где
// question-check стоит в Stop-хуках
type questionEnv struct {
	home       string
	transcript string
	config     *core.Config
	logger     core.Logger
}

func newQuestionEnv(t *testing.T, sessionName string, registered bool) *questionEnv {
	t.Helper()
	config, logger := silentConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	pid := strconv.Itoa(os.Getpid())
	t.Setenv("CLAUDE_PID", pid)

	claudeDir := filepath.Join(home, ".claude")
	writeFile(t, filepath.Join(claudeDir, "sessions", pid+".json"), `{"pid":`+pid+`,"name":"`+sessionName+`"}`)
	command := "$HOME/.claude/hooks/claude-hooks stop"
	if registered {
		command = "$HOME/.claude/hooks/claude-hooks question-check"
	}
	writeFile(t, filepath.Join(claudeDir, "settings.json"),
		`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"$HOME/.claude/hooks/claude-hooks stop","timeout":3},{"type":"command","command":"`+command+`","timeout":60}]}]}}`)

	transcript := filepath.Join(claudeDir, "projects", questioncheck.ProjectDirName(home), "s.jsonl")
	writeFile(t, transcript, `{"type":"user","uuid":"u1","origin":{"kind":"human"},"message":{"role":"user","content":"посчитай лимит"}}`+"\n"+
		`{"type":"assistant","uuid":"m1","message":{"role":"assistant","content":[{"type":"text","text":"`+questionText+`"}]}}`+"\n")
	return &questionEnv{home: home, transcript: transcript, config: config, logger: logger}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// input — вход Stop без фоновых задач: конец хода, который зовёт человека
func (e *questionEnv) input(sessionID, message string, stopHookActive bool) *core.ToolInput {
	empty := []core.BackgroundTask{}
	crons := []core.SessionCron{}
	return &core.ToolInput{
		SessionID:            sessionID,
		TranscriptPath:       e.transcript,
		StopHookActive:       stopHookActive,
		LastAssistantMessage: message,
		BackgroundTasks:      &empty,
		SessionCrons:         &crons,
	}
}

func (e *questionEnv) checker(judge questioncheck.Judge) questioncheck.Checker {
	scope := defaultScope(e.home)
	return questioncheck.Checker{
		Scope:       scope,
		Journals:    questioncheck.DefaultJournals(scope.ClaudeDir),
		Judge:       judge,
		JournalPath: filepath.Join(e.home, "state", "question-check.jsonl"),
		Now:         func() time.Time { return questionDay },
		Budget:      50 * time.Millisecond,
		Warn:        func(string, ...any) {},
	}
}

// check прогоняет question-check так, как после отданного звонка
func (e *questionEnv) check(t *testing.T, judge questioncheck.Judge, input *core.ToolInput) (questioncheck.Result, string, bool) {
	t.Helper()
	awaited := stopAwaitsQuestionCheck(input, e.config, e.logger, questionDay)
	var out strings.Builder
	result, decision := checkStop(context.Background(), e.checker(judge), stopCheck{
		input: input, config: e.config, logger: e.logger, out: &out, awaited: awaited, started: time.Now(),
	})
	return result, out.String(), decision != nil && decision.Alert
}

func sessionState(t *testing.T, sessionID string) core.SessionState {
	t.Helper()
	state, err := core.LoadSessionState(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// На pass, сбой и таймаут судьи звонит question-check: ход кончился, а Stop-хук
// звонок отдал. На block агент продолжает — звонка нет, состояние не тронуто
func TestQuestionCheckRingsAfterVerdict(t *testing.T) {
	cases := []struct {
		name  string
		judge *stubJudge
		ring  bool
	}{
		{"pass", &stubJudge{verdict: questioncheck.Verdict{Verdict: questioncheck.VerdictPass}}, true},
		{"error", &stubJudge{err: errors.New("claude: exit status 1")}, true},
		{"timeout", &stubJudge{hang: true}, true},
		{"block", &stubJudge{verdict: questioncheck.Verdict{
			Verdict: questioncheck.VerdictBlock,
			Lishnie: []questioncheck.Finding{{Quote: "Поменять лимит на 200?", Why: "решено в журнале"}},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newQuestionEnv(t, "lead", true)
			input := env.input("q-"+tc.name, questionText, false)

			result, out, rang := env.check(t, tc.judge, input)
			if tc.judge.calls != 1 {
				t.Fatalf("судью звали %d раз", tc.judge.calls)
			}
			if rang != tc.ring {
				t.Fatalf("звонок %v, ожидался %v (result %+v)", rang, tc.ring, result)
			}
			if tc.ring {
				if out != "" {
					t.Errorf("конец хода не остановлен, а вывод есть: %s", out)
				}
				if state := sessionState(t, input.SessionID); state != core.StateDone {
					t.Errorf("после звонка состояние %q, ожидалось done", state)
				}
				return
			}
			var block stopBlockOutput
			if err := json.Unmarshal([]byte(out), &block); err != nil || block.Decision != "block" {
				t.Fatalf("ожидалась остановка, вывод %q: %v", out, err)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "claude-hooks", "sessions", input.SessionID)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("на block состояние сессии записано: %v", err)
			}
		})
	}
}

// Stop-хук отдаёт звонок только там, где question-check позовёт судью
func TestStopAwaitsQuestionCheck(t *testing.T) {
	cases := []struct {
		name       string
		session    string
		registered bool
		disabled   bool
		message    string
		afterBlock bool
		subagent   bool
		now        time.Time
		awaits     bool
	}{
		{name: "вопрос в сессии лида", session: "lead", registered: true, message: questionText, now: questionDay, awaits: true},
		{name: "чужая сессия", session: "folio-master", registered: true, message: questionText, now: questionDay},
		{name: "без вопроса", session: "lead", registered: true, message: "Готово, коммит abc123.", now: questionDay},
		{name: "ход после остановки", session: "lead", registered: true, message: questionText, afterBlock: true, now: questionDay},
		{name: "субагент", session: "lead", registered: true, message: questionText, subagent: true, now: questionDay},
		{name: "после срока", session: "lead", registered: true, message: questionText, now: questioncheck.Until},
		{name: "не стоит в settings.json", session: "lead", message: questionText, now: questionDay},
		{name: "выключен в config.yaml", session: "lead", registered: true, disabled: true, message: questionText, now: questionDay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newQuestionEnv(t, tc.session, tc.registered)
			if tc.disabled {
				env.config.Tools[questionCheckTool] = core.ToolConfig{Enabled: false}
			}
			input := env.input("a", tc.message, tc.afterBlock)
			if tc.subagent {
				input.AgentID = "agent-1"
			}
			if got := stopAwaitsQuestionCheck(input, env.config, env.logger, tc.now); got != tc.awaits {
				t.Errorf("stopAwaitsQuestionCheck = %v, ожидалось %v", got, tc.awaits)
			}
		})
	}
}

// Чужая сессия и сообщение без вопроса: Stop-хук звонит как раньше, а
// question-check не звонит второй раз
func TestStopRingsWhereQuestionCheckIsIdle(t *testing.T) {
	cases := []struct{ name, session, message string }{
		{"чужая сессия", "folio-master", questionText},
		{"без вопроса", "lead", "Готово, коммит abc123."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newQuestionEnv(t, tc.session, true)
			input := env.input("idle", tc.message, false)

			decision := stopEvent(t, env, input)
			if !decision {
				t.Fatal("Stop-хук не позвонил")
			}
			judge := &stubJudge{verdict: questioncheck.Verdict{Verdict: questioncheck.VerdictPass}}
			if _, _, rang := env.check(t, judge, input); rang || judge.calls != 0 {
				t.Errorf("question-check: звонок %v, судья %d раз", rang, judge.calls)
			}
		})
	}
}

// stopEvent прогоняет Stop-хук и говорит, позвал ли он человека. Позвал —
// значит, перевёл работающую сессию в done: звонок решает тот же переход
func stopEvent(t *testing.T, env *questionEnv, input *core.ToolInput) bool {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	runSessionEvent(core.EventStop, data, env.config, env.logger)
	return sessionState(t, input.SessionID) == core.StateDone
}

// На проверяемом конце хода Stop-хук молчит и состояние не пишет: его
// запишет question-check после вердикта
func TestStopMutedWhileQuestionCheckJudges(t *testing.T) {
	if !time.Now().Before(questioncheck.Until) {
		t.Skip("эксперимент окончен: Stop-хук больше не отдаёт звонок")
	}
	env := newQuestionEnv(t, "lead", true)
	input := env.input("muted", questionText, false)

	if stopEvent(t, env, input) {
		t.Fatal("Stop-хук позвонил до вердикта")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "claude-hooks", "sessions", "muted")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stop-хук записал состояние до вердикта: %v", err)
	}

	// Ход после остановки — обычный конец хода: звонит Stop-хук
	if !stopEvent(t, env, env.input("muted", "Нашёл в журнале: лимит 200. Поменял.", true)) {
		t.Error("после остановки Stop-хук не позвонил")
	}
}

// Новое сообщение человека во время проверки: конец прошлого хода уже не
// новость, question-check не звонит и состояние не трогает
func TestQuestionCheckSilentWhenStateChangedDuringCheck(t *testing.T) {
	env := newQuestionEnv(t, "lead", true)
	input := env.input("race", questionText, false)
	started := time.Now()
	time.Sleep(10 * time.Millisecond)
	if err := core.SaveSessionState(input.SessionID, core.StateWorking); err != nil {
		t.Fatal(err)
	}

	if decision := ringAfterQuestionCheck(input, env.config, env.logger, started); decision != nil {
		t.Fatalf("звонок после нового сообщения: %+v", decision)
	}
	if state := sessionState(t, input.SessionID); state != core.StateWorking {
		t.Errorf("состояние %q, ожидалось working", state)
	}
}

// Claude Code запускает Stop-хуки параллельно. Оба процесса решают одной
// функцией: на проверяемом конце хода пишет и звонит только question-check,
// на остальных — только Stop-хук. Звонок ровно один, состояние — done
func TestParallelStopHooksRingOnce(t *testing.T) {
	if !time.Now().Before(questioncheck.Until) {
		t.Skip("эксперимент окончен: Stop-хук больше не отдаёт звонок")
	}
	cases := []struct {
		name, message string
		stopRings     bool
	}{
		{"вопрос", questionText, false},
		{"без вопроса", "Готово, коммит abc123.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newQuestionEnv(t, "lead", true)
			for i := 0; i < 20; i++ {
				input := env.input("par-"+strconv.Itoa(i), tc.message, false)
				data, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				var checkRang bool
				wg.Add(2)
				go func() {
					defer wg.Done()
					runSessionEvent(core.EventStop, data, env.config, env.logger)
				}()
				go func() {
					defer wg.Done()
					judge := &stubJudge{verdict: questioncheck.Verdict{Verdict: questioncheck.VerdictPass}}
					_, _, checkRang = env.check(t, judge, input)
				}()
				wg.Wait()

				if checkRang == tc.stopRings {
					t.Fatalf("прогон %d: question-check звонит %v, Stop-хук должен звонить %v", i, checkRang, tc.stopRings)
				}
				if state := sessionState(t, input.SessionID); state != core.StateDone {
					t.Fatalf("прогон %d: состояние %q, ожидалось done", i, state)
				}
			}
		})
	}
}
