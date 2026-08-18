package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if got := loadState(t, "session-1"); got != StateWorking {
		t.Errorf("неизвестная сессия должна считаться работающей, получено %q", got)
	}

	for _, state := range []SessionState{StateWaiting, StateDone, StateWorking} {
		saveState(t, "session-1", state)
		if got := loadState(t, "session-1"); got != state {
			t.Errorf("ожидалось %q, получено %q", state, got)
		}
	}
}

func TestSessionStateIsolatedBySession(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	saveState(t, "first", StateWaiting)
	saveState(t, "second", StateDone)

	if got := loadState(t, "first"); got != StateWaiting {
		t.Errorf("первая сессия: ожидалось %q, получено %q", StateWaiting, got)
	}
	if got := loadState(t, "second"); got != StateDone {
		t.Errorf("вторая сессия: ожидалось %q, получено %q", StateDone, got)
	}
}

func TestSessionStateRejectsPathTraversal(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// Идентификатор приходит извне и в путь попадать не должен
	saveState(t, "../../escaped", StateDone)

	if _, err := os.Stat(filepath.Join(runtimeDir, "..", "..", "escaped")); err == nil {
		t.Error("файл состояния оказался за пределами каталога сессий")
	}
	if got := loadState(t, "escaped"); got != StateDone {
		t.Errorf("имя должно усекаться до последнего сегмента, получено %q", got)
	}
}

func TestSessionStateIgnoresEmptyID(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	saveState(t, "", StateDone)

	if entries, err := os.ReadDir(filepath.Join(runtimeDir, "claude-hooks", "sessions")); err == nil && len(entries) > 0 {
		t.Error("сессия без идентификатора не должна создавать файлов")
	}
	if got := loadState(t, ""); got != StateWorking {
		t.Errorf("ожидалось %q, получено %q", StateWorking, got)
	}
}

func TestSessionStateCleansUpStaleFiles(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	saveState(t, "old", StateDone)
	stalePath, err := sessionStatePath("old")
	if err != nil {
		t.Fatalf("не удалось получить путь состояния: %v", err)
	}

	stale := time.Now().Add(-stateTTL - time.Hour)
	if err := os.Chtimes(stalePath, stale, stale); err != nil {
		t.Fatalf("не удалось состарить файл: %v", err)
	}

	// Уборка выполняется попутно при следующей записи
	saveState(t, "fresh", StateWorking)

	if _, err := os.Stat(stalePath); err == nil {
		t.Error("устаревшее состояние должно удаляться")
	}
	if got := loadState(t, "fresh"); got != StateWorking {
		t.Errorf("свежее состояние потеряно: %q", got)
	}
}

// saveState сохраняет состояние и проваливает тест на ошибке записи
func saveState(t *testing.T, sessionID string, state SessionState) {
	t.Helper()
	if err := SaveSessionState(sessionID, state); err != nil {
		t.Fatalf("SaveSessionState(%q): %v", sessionID, err)
	}
}

// loadState читает состояние и проваливает тест на ошибке чтения
func loadState(t *testing.T, sessionID string) SessionState {
	t.Helper()
	got, err := LoadSessionState(sessionID)
	if err != nil {
		t.Fatalf("LoadSessionState(%q): %v", sessionID, err)
	}
	return got
}

func TestLoadSessionStateFallsBackOnGarbage(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	path, err := sessionStatePath("broken")
	if err != nil {
		t.Fatalf("не удалось получить путь состояния: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("не удалось создать каталог: %v", err)
	}
	if err := os.WriteFile(path, []byte("что-то не то"), 0o644); err != nil {
		t.Fatalf("не удалось записать файл: %v", err)
	}

	if got := loadState(t, "broken"); got != StateWorking {
		t.Errorf("ожидалось %q, получено %q", StateWorking, got)
	}
}
