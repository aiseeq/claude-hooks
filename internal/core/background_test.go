package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActiveBackgroundTasks_FreshTasksAreActive(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	now := time.Now()

	tasks := []BackgroundTask{{ID: "a", Type: "shell"}, {ID: "b", Type: "subagent"}}
	active, err := ActiveBackgroundTasks("s1", tasks, now)
	if err != nil {
		t.Fatalf("учёт задач: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("свежие задачи должны считаться живыми: %+v", active)
	}
}

// Dev-сервер в фоне не отчитывается никогда: иначе он глушил бы уведомления
// до конца сессии
func TestActiveBackgroundTasks_AbandonedTaskExpires(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	start := time.Now()

	server := BackgroundTask{ID: "server", Type: "shell"}
	if _, err := ActiveBackgroundTasks("s1", []BackgroundTask{server}, start); err != nil {
		t.Fatalf("учёт задач: %v", err)
	}

	later := start.Add(abandonedTaskAge + time.Minute)
	agent := BackgroundTask{ID: "agent", Type: "subagent"}
	active, err := ActiveBackgroundTasks("s1", []BackgroundTask{server, agent}, later)
	if err != nil {
		t.Fatalf("учёт задач: %v", err)
	}
	if len(active) != 1 || active[0].ID != "agent" {
		t.Errorf("живой должна остаться только новая задача: %+v", active)
	}
}

// Задача, пропавшая из списка, забывается: тот же идентификатор позже —
// уже новая задача
func TestActiveBackgroundTasks_ForgetsFinishedTasks(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	start := time.Now()

	task := BackgroundTask{ID: "a"}
	if _, err := ActiveBackgroundTasks("s1", []BackgroundTask{task}, start); err != nil {
		t.Fatalf("учёт задач: %v", err)
	}
	if _, err := ActiveBackgroundTasks("s1", nil, start.Add(time.Minute)); err != nil {
		t.Fatalf("учёт задач: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "claude-hooks", "sessions", "s1"+tasksFileSuffix)); !os.IsNotExist(err) {
		t.Error("без задач файл учёта должен удаляться")
	}

	active, err := ActiveBackgroundTasks("s1", []BackgroundTask{task}, start.Add(abandonedTaskAge+time.Hour))
	if err != nil {
		t.Fatalf("учёт задач: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("задача, увиденная заново, должна считаться свежей: %+v", active)
	}
}

func TestActiveBackgroundTasks_CorruptFileIsAnError(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	dir := filepath.Join(runtimeDir, "claude-hooks", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1"+tasksFileSuffix), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	tasks := []BackgroundTask{{ID: "a"}}
	active, err := ActiveBackgroundTasks("s1", tasks, time.Now())
	if err == nil {
		t.Error("испорченный файл учёта должен давать ошибку")
	}
	if active != nil {
		t.Errorf("при сбое учёта список не выдумывается: %+v", active)
	}
}
