package main

import (
	"encoding/json"
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
