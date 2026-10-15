package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// stopInput собирает вход Stop по схеме Claude Code 2.1.284
func stopInput(t *testing.T, sessionID, transcript string, tasks []map[string]any, crons []map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"session_id":             sessionID,
		"transcript_path":        transcript,
		"cwd":                    "/tmp/project",
		"hook_event_name":        "Stop",
		"stop_hook_active":       false,
		"last_assistant_message": "готово",
		"background_tasks":       tasks,
		"session_crons":          crons,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func silentConfig(t *testing.T) (*core.Config, core.Logger) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_PID", "")

	config := &core.Config{Tools: map[string]core.ToolConfig{"notifier": {Enabled: false}}}
	logger, err := core.NewLogger(core.LoggerConfig{Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	return config, logger
}

// Stop не открывает транскрипт: путь указывает на FIFO, и любая попытка его
// прочитать повесила бы хук до таймаута теста
func TestRunSessionEvent_StopDoesNotReadTranscript(t *testing.T) {
	config, logger := silentConfig(t)

	fifo := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan struct{})
	go func() {
		runSessionEvent(core.EventStop, stopInput(t, "s1", fifo, []map[string]any{}, []map[string]any{}), config, logger)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop-хук ждёт транскрипт")
	}

	state, err := core.LoadSessionState("s1")
	if err != nil || state != core.StateDone {
		t.Errorf("состояние после остановки: %q, %v", state, err)
	}
}

// Остановка при живой фоновой задаче — пауза; следующая остановка без задач —
// конец работы
func TestRunSessionEvent_PauseThenDone(t *testing.T) {
	config, logger := silentConfig(t)
	shell := []map[string]any{{"id": "b1", "type": "shell", "status": "running", "description": "sleep 40", "command": "sleep 40"}}

	runSessionEvent(core.EventStop, stopInput(t, "s2", "/nonexistent.jsonl", shell, []map[string]any{}), config, logger)
	if state, err := core.LoadSessionState("s2"); err != nil || state != core.StatePaused {
		t.Fatalf("при живой задаче ожидалась пауза: %q, %v", state, err)
	}

	idle, err := json.Marshal(map[string]any{"session_id": "s2", "notification_type": "idle_prompt", "message": "Claude is waiting for your input"})
	if err != nil {
		t.Fatal(err)
	}
	runSessionEvent(core.EventNotification, idle, config, logger)
	if state, err := core.LoadSessionState("s2"); err != nil || state != core.StatePaused {
		t.Fatalf("напоминание при паузе не меняет состояние: %q, %v", state, err)
	}

	runSessionEvent(core.EventStop, stopInput(t, "s2", "/nonexistent.jsonl", []map[string]any{}, []map[string]any{}), config, logger)
	if state, err := core.LoadSessionState("s2"); err != nil || state != core.StateDone {
		t.Errorf("после последней задачи ожидалось «готово»: %q, %v", state, err)
	}
}

// Сбой учёта фоновых задач не превращает остановку в «готово»: Claude Code
// сам сказал, что задача жива, и сессия остаётся на паузе
func TestRunSessionEvent_BrokenTaskBookkeepingKeepsPause(t *testing.T) {
	config, logger := silentConfig(t)

	dir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "claude-hooks", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s3.tasks"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	shell := []map[string]any{{"id": "b1", "type": "shell", "status": "running"}}
	runSessionEvent(core.EventStop, stopInput(t, "s3", "/nonexistent.jsonl", shell, []map[string]any{}), config, logger)
	if state, err := core.LoadSessionState("s3"); err != nil || state != core.StatePaused {
		t.Errorf("при сбое учёта ожидалась пауза: %q, %v", state, err)
	}
}

// draftStopInput вход Stop с ответом модели и признаком продолжения хода
func draftStopInput(t *testing.T, message string, active bool) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"session_id":             "draft-session",
		"hook_event_name":        "Stop",
		"stop_hook_active":       active,
		"last_assistant_message": message,
		"background_tasks":       []map[string]any{},
		"session_crons":          []map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// captureStdout перехватывает то, что хук печатает Claude Code
func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	run()
	os.Stdout = saved
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestReviewDrafts_FeedbackContinuesTurn(t *testing.T) {
	config, logger := silentConfig(t)
	config.Tools["draft_style"] = core.ToolConfig{Enabled: true}

	var issued bool
	out := captureStdout(t, func() {
		issued = reviewDrafts(draftStopInput(t, "Текст:\n\n```\nФикс в тесте — проверяй. Получилось?\n```\n", false), config, logger)
	})
	if !issued {
		t.Fatal("черновик с тире должен продолжить ход")
	}

	var feedback stopFeedback
	if err := json.Unmarshal([]byte(out), &feedback); err != nil {
		t.Fatalf("ответ хука не JSON: %v\n%s", err, out)
	}
	if feedback.HookSpecificOutput.HookEventName != core.EventStop || feedback.HookSpecificOutput.AdditionalContext == "" {
		t.Errorf("ответ хука без additionalContext для Stop: %s", out)
	}
}

// Ход, уже продолженный stop-хуком, второй раз не задерживается
func TestReviewDrafts_SkipsContinuedTurn(t *testing.T) {
	config, logger := silentConfig(t)
	config.Tools["draft_style"] = core.ToolConfig{Enabled: true}

	out := captureStdout(t, func() {
		if reviewDrafts(draftStopInput(t, "```\nФикс в тесте — проверяй\n```", true), config, logger) {
			t.Error("продолженный ход проверяться не должен")
		}
	})
	if out != "" {
		t.Errorf("хук напечатал ответ на продолженном ходе: %s", out)
	}
}

func TestReviewDrafts_DisabledByConfig(t *testing.T) {
	config, logger := silentConfig(t)
	if reviewDrafts(draftStopInput(t, "```\nФикс в тесте — проверяй\n```", false), config, logger) {
		t.Error("без draft_style в конфигурации черновики не проверяются")
	}
}
