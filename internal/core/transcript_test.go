package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Записи транскрипта в тестах повторяют структуру реальных: запуск задачи —
// это пара «tool_use инструмента Agent или фонового Bash» и «tool_result
// с текстом запуска», завершение — user-запись с task-notification

func agentLaunch(toolUseID, agentID string) []string {
	return []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + toolUseID + `","name":"Agent","input":{"prompt":"do work","run_in_background":true}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + toolUseID + `","content":"Async agent launched successfully. (This tool result is internal metadata)\nagentId: ` + agentID + ` (internal ID - do not mention to user.)"}]}}`,
	}
}

func bashLaunch(toolUseID, taskID string) []string {
	return []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + toolUseID + `","name":"Bash","input":{"command":"sleep 600","run_in_background":true}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + toolUseID + `","content":[{"type":"text","text":"Command running in background with ID: ` + taskID + `"}]}]}}`,
	}
}

func monitorLaunch(toolUseID, taskID string) []string {
	return []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + toolUseID + `","name":"Monitor","input":{"command":"tail -f log","description":"log","timeout_ms":60000,"persistent":false}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + toolUseID + `","content":"Monitor started (task ` + taskID + `, timeout 60000ms). You will be notified on each event."}]}}`,
	}
}

func monitorEvent(taskID, event string) string {
	return `{"type":"user","message":{"content":"<task-notification>\n<task-id>` + taskID + `</task-id>\n<summary>Monitor event: \"log\"</summary>\n<event>` + event + `</event>\n</task-notification>"}}`
}

func taskNotification(taskID string) string {
	return `{"type":"user","message":{"content":"<task-notification>\n<task-id>` + taskID + `</task-id>\n<status>completed</status>\n</task-notification>"}}`
}

func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("не удалось записать транскрипт: %v", err)
	}
	return path
}

func TestPendingBackgroundTasks_LaunchAndComplete(t *testing.T) {
	var lines []string
	lines = append(lines, agentLaunch("toolu_01", "a7ceadcaf07124ca8")...)
	lines = append(lines, agentLaunch("toolu_02", "af00991553a36e585")...)
	lines = append(lines, bashLaunch("toolu_03", "b1a2c3")...)

	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 3 {
		t.Errorf("запущены три задачи, насчитано %d", got)
	}

	lines = append(lines, taskNotification("a7ceadcaf07124ca8"))
	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 2 {
		t.Errorf("одна из трёх завершена, насчитано %d", got)
	}

	lines = append(lines,
		taskNotification("af00991553a36e585"),
		taskNotification("b1a2c3"),
	)
	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 0 {
		t.Errorf("все задачи завершены, насчитано %d", got)
	}
}

// Содержимое прочитанных файлов и вывод обычных команд попадают в транскрипт
// и могут дословно цитировать маркеры запуска — считаться запуском они не должны
func TestPendingBackgroundTasks_QuotedMarkersIgnored(t *testing.T) {
	lines := []string{
		// git diff файла с тестовыми фикстурами — Bash без run_in_background
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_10","name":"Bash","input":{"command":"git diff"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_10","content":"+Async agent launched successfully\n+agentId: fake111\n+Command running in background with ID: fake222"}]}}`,
		// чтение файла, цитирующего те же маркеры
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_11","name":"Read","input":{"file_path":"/x/notifier_test.go"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_11","content":"Async agent launched successfully agentId: fake333"}]}}`,
		// служебная запись очереди уведомлений — не user-сообщение
		`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>fake444</task-id>"}`,
	}

	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 0 {
		t.Errorf("в транскрипте только цитаты, насчитано %d", got)
	}
}

// Ответ форграундного субагента — обычный tool_result без текста запуска:
// такой вызов завершается внутри хода и фоновой задачей не является
func TestPendingBackgroundTasks_ForegroundAgentIgnored(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_20","name":"Agent","input":{"prompt":"quick check","run_in_background":false}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_20","content":"Проверка выполнена, ошибок нет."}]}}`,
	}

	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 0 {
		t.Errorf("форграундный агент не фоновая задача, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_TaskStopRemoves(t *testing.T) {
	lines := agentLaunch("toolu_30", "a30a30a30")
	lines = append(lines,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_31","name":"TaskStop","input":{"task_id":"a30a30a30"}}]}}`,
	)

	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 0 {
		t.Errorf("остановленная задача уведомления не пришлёт, насчитано %d", got)
	}
}

// Вечный фоновый процесс не должен глушить уведомления до конца сессии:
// запуск старше TTL без отчёта перестаёт считаться живым
func TestPendingBackgroundTasks_StaleLaunchExpires(t *testing.T) {
	stale := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_40","name":"Bash","input":{"command":"./dev-server","run_in_background":true}}]}}`,
		`{"type":"user","timestamp":"` + stale + `","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_40","content":"Command running in background with ID: eternal1"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_41","name":"Agent","input":{"prompt":"work"}}]}}`,
		`{"type":"user","timestamp":"` + fresh + `","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_41","content":"Async agent launched successfully\nagentId: fresh1 (internal)"}]}}`,
	}

	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 1 {
		t.Errorf("просроченный запуск не должен считаться, ожидалась 1 живая задача, насчитано %d", got)
	}
}

func scheduleWakeup(timestamp, extraInput string) string {
	return `{"type":"assistant","timestamp":"` + timestamp + `","message":{"content":[{"type":"tool_use","id":"toolu_wk","name":"ScheduleWakeup","input":{"delaySeconds":600,"prompt":"продолжай цикл","reason":"жду перегон"` + extraInput + `}}]}}`
}

// Тик динамического /loop завершает ход вызовом ScheduleWakeup — сессия
// вернётся к работе сама, уведомлять человека не о чем
func TestAwaitingScheduledWakeup_Armed(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	lines := []string{scheduleWakeup(now, "")}

	if !AwaitingScheduledWakeup(writeTranscript(t, lines)) {
		t.Error("будильник взведён, а ожидание не распознано")
	}
}

// ScheduleWakeup со stop:true завершает цикл: следующая остановка — настоящая
func TestAwaitingScheduledWakeup_StopDisarms(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	lines := []string{
		scheduleWakeup(now, ""),
		`{"type":"assistant","timestamp":"` + now + `","message":{"content":[{"type":"tool_use","id":"toolu_wk2","name":"ScheduleWakeup","input":{"stop":true}}]}}`,
	}

	if AwaitingScheduledWakeup(writeTranscript(t, lines)) {
		t.Error("цикл остановлен stop:true, а ожидание всё ещё распознаётся")
	}
}

// Будильник, чей срок с запасом давно вышел, уже не сработает — глушить
// уведомления до конца сессии он не должен
func TestAwaitingScheduledWakeup_StaleExpires(t *testing.T) {
	stale := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	lines := []string{scheduleWakeup(stale, "")}

	if AwaitingScheduledWakeup(writeTranscript(t, lines)) {
		t.Error("будильник просрочен, а ожидание всё ещё распознаётся")
	}
}

// Цитата вызова в результате инструмента (чтение транскрипта, git diff) —
// не вызов: учитываются только tool_use в ассистентских записях
func TestAwaitingScheduledWakeup_QuotedIgnored(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_50","content":"\"type\":\"tool_use\",\"name\":\"ScheduleWakeup\",\"input\":{\"delaySeconds\":600}"}]}}`,
	}

	if AwaitingScheduledWakeup(writeTranscript(t, lines)) {
		t.Error("в транскрипте только цитата, а ожидание распознано")
	}
}

func TestAwaitingScheduledWakeup_MissingTranscript(t *testing.T) {
	if AwaitingScheduledWakeup("") {
		t.Error("без транскрипта ожидания быть не может")
	}
	if AwaitingScheduledWakeup(filepath.Join(t.TempDir(), "нет-такого")) {
		t.Error("несуществующий транскрипт не должен давать ожидание")
	}
}

func TestPendingBackgroundTasks_MissingTranscript(t *testing.T) {
	if got := PendingBackgroundTasks(""); got != 0 {
		t.Errorf("без транскрипта должно быть 0, насчитано %d", got)
	}
	if got := PendingBackgroundTasks(filepath.Join(t.TempDir(), "нет-такого")); got != 0 {
		t.Errorf("несуществующий транскрипт должен давать 0, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_MonitorAliveThroughEvents(t *testing.T) {
	var lines []string
	lines = append(lines, monitorLaunch("toolu_01", "bm0n1t0r")...)
	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 1 {
		t.Fatalf("монитор запущен, насчитано %d", got)
	}

	// Событие монитора — не завершение: он продолжает слушать
	lines = append(lines, monitorEvent("bm0n1t0r", "=== session one"),
		monitorEvent("bm0n1t0r", "=== session two"))
	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 1 {
		t.Errorf("после двух событий монитор должен быть жив, насчитано %d", got)
	}

	// Истёкший таймаут — завершение
	lines = append(lines, monitorEvent("bm0n1t0r", "[Monitor timed out — re-arm if needed.]"))
	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 0 {
		t.Errorf("монитор истёк, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_MonitorEndsByStatus(t *testing.T) {
	var lines []string
	lines = append(lines, monitorLaunch("toolu_01", "bm0n1t0r")...)
	lines = append(lines, monitorEvent("bm0n1t0r", "line"))
	lines = append(lines, taskNotification("bm0n1t0r"))
	if got := PendingBackgroundTasks(writeTranscript(t, lines)); got != 0 {
		t.Errorf("монитор завершён со статусом, насчитано %d", got)
	}
}
