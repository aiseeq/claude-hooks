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
// с текстом запуска», завершение — task-notification в user-записи (между
// ходами), во вложении queued_command (посреди хода) или в записи очереди

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

func taskNotificationAttachment(taskID string) string {
	return `{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>\n<task-id>` + taskID + `</task-id>\n<status>completed</status>\n</task-notification>"}}`
}

func taskNotificationQueued(op, taskID string) string {
	return `{"type":"queue-operation","operation":"` + op + `","content":"<task-notification>\n<task-id>` + taskID + `</task-id>\n<status>completed</status>\n</task-notification>"}`
}

// pendingTasks считает задачи по транскрипту и проваливает тест, если он нечитаем
func pendingTasks(t *testing.T, path string) int {
	t.Helper()
	got, err := PendingBackgroundTasks(path)
	if err != nil {
		t.Fatalf("PendingBackgroundTasks(%s): %v", path, err)
	}
	return got
}

// wakeupArmed проверяет будильник по транскрипту и проваливает тест, если он нечитаем
func wakeupArmed(t *testing.T, path string) bool {
	t.Helper()
	got, err := AwaitingScheduledWakeup(path)
	if err != nil {
		t.Fatalf("AwaitingScheduledWakeup(%s): %v", path, err)
	}
	return got
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

	if got := pendingTasks(t, writeTranscript(t, lines)); got != 3 {
		t.Errorf("запущены три задачи, насчитано %d", got)
	}

	lines = append(lines, taskNotification("a7ceadcaf07124ca8"))
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 2 {
		t.Errorf("одна из трёх завершена, насчитано %d", got)
	}

	lines = append(lines,
		taskNotification("af00991553a36e585"),
		taskNotification("b1a2c3"),
	)
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 0 {
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

	if got := pendingTasks(t, writeTranscript(t, lines)); got != 0 {
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

	if got := pendingTasks(t, writeTranscript(t, lines)); got != 0 {
		t.Errorf("форграундный агент не фоновая задача, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_TaskStopRemoves(t *testing.T) {
	lines := agentLaunch("toolu_30", "a30a30a30")
	lines = append(lines,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_31","name":"TaskStop","input":{"task_id":"a30a30a30"}}]}}`,
	)

	if got := pendingTasks(t, writeTranscript(t, lines)); got != 0 {
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

	if got := pendingTasks(t, writeTranscript(t, lines)); got != 1 {
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

	if !wakeupArmed(t, writeTranscript(t, lines)) {
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

	if wakeupArmed(t, writeTranscript(t, lines)) {
		t.Error("цикл остановлен stop:true, а ожидание всё ещё распознаётся")
	}
}

// Будильник, чей срок с запасом давно вышел, уже не сработает — глушить
// уведомления до конца сессии он не должен
func TestAwaitingScheduledWakeup_StaleExpires(t *testing.T) {
	stale := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	lines := []string{scheduleWakeup(stale, "")}

	if wakeupArmed(t, writeTranscript(t, lines)) {
		t.Error("будильник просрочен, а ожидание всё ещё распознаётся")
	}
}

// Цитата вызова в результате инструмента (чтение транскрипта, git diff) —
// не вызов: учитываются только tool_use в ассистентских записях
func TestAwaitingScheduledWakeup_QuotedIgnored(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_50","content":"\"type\":\"tool_use\",\"name\":\"ScheduleWakeup\",\"input\":{\"delaySeconds\":600}"}]}}`,
	}

	if wakeupArmed(t, writeTranscript(t, lines)) {
		t.Error("в транскрипте только цитата, а ожидание распознано")
	}
}

func TestAwaitingScheduledWakeup_MissingTranscript(t *testing.T) {
	if wakeupArmed(t, "") {
		t.Error("без транскрипта ожидания быть не может")
	}
	armed, err := AwaitingScheduledWakeup(filepath.Join(t.TempDir(), "нет-такого"))
	if err == nil {
		t.Error("несуществующий транскрипт должен давать ошибку, а не молчаливый ответ")
	}
	if armed {
		t.Error("несуществующий транскрипт не должен давать ожидание")
	}
}

func TestPendingBackgroundTasks_MissingTranscript(t *testing.T) {
	if got := pendingTasks(t, ""); got != 0 {
		t.Errorf("без транскрипта должно быть 0, насчитано %d", got)
	}
	got, err := PendingBackgroundTasks(filepath.Join(t.TempDir(), "нет-такого"))
	if err == nil {
		t.Error("несуществующий транскрипт должен давать ошибку, а не молчаливый ответ")
	}
	if got != 0 {
		t.Errorf("несуществующий транскрипт должен давать 0, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_BrokenRecordsReported(t *testing.T) {
	lines := append(agentLaunch("toolu_60", "agent60"), `{"type":"user","message":{"content":{"not":"content"}}}`)
	got, err := PendingBackgroundTasks(writeTranscript(t, lines))
	if err == nil {
		t.Error("нечитаемая запись должна попасть в ошибку")
	}
	if got != 1 {
		t.Errorf("остальные записи должны считаться несмотря на битую: ожидалась 1 задача, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_MonitorAliveThroughEvents(t *testing.T) {
	var lines []string
	lines = append(lines, monitorLaunch("toolu_01", "bm0n1t0r")...)
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 1 {
		t.Fatalf("монитор запущен, насчитано %d", got)
	}

	// Событие монитора — не завершение: он продолжает слушать
	lines = append(lines, monitorEvent("bm0n1t0r", "=== session one"),
		monitorEvent("bm0n1t0r", "=== session two"))
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 1 {
		t.Errorf("после двух событий монитор должен быть жив, насчитано %d", got)
	}

	// Истёкший таймаут — завершение
	lines = append(lines, monitorEvent("bm0n1t0r", "[Monitor timed out — re-arm if needed.]"))
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 0 {
		t.Errorf("монитор истёк, насчитано %d", got)
	}
}

func TestPendingBackgroundTasks_MonitorEndsByStatus(t *testing.T) {
	var lines []string
	lines = append(lines, monitorLaunch("toolu_01", "bm0n1t0r")...)
	lines = append(lines, monitorEvent("bm0n1t0r", "line"))
	lines = append(lines, taskNotification("bm0n1t0r"))
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 0 {
		t.Errorf("монитор завершён со статусом, насчитано %d", got)
	}
}

// Уведомление, пришедшее посреди хода, ложится в транскрипт не user-записью,
// а вложением и записями очереди — задача завершена по любой из них
func TestPendingBackgroundTasks_MidTurnNotificationCompletes(t *testing.T) {
	cases := map[string]string{
		"attachment":    taskNotificationAttachment("bm1dturn"),
		"queue enqueue": taskNotificationQueued("enqueue", "bm1dturn"),
		"queue remove":  taskNotificationQueued("remove", "bm1dturn"),
	}
	for name, record := range cases {
		t.Run(name, func(t *testing.T) {
			var lines []string
			lines = append(lines, bashLaunch("toolu_01", "bm1dturn")...)
			lines = append(lines, bashLaunch("toolu_02", "b0ther")...)
			lines = append(lines, record)
			if got := pendingTasks(t, writeTranscript(t, lines)); got != 1 {
				t.Errorf("после %s должна остаться одна задача, насчитано %d", name, got)
			}
		})
	}
}

// Событие монитора во вложении — тоже не завершение
func TestPendingBackgroundTasks_MonitorEventAttachmentKeepsAlive(t *testing.T) {
	var lines []string
	lines = append(lines, monitorLaunch("toolu_01", "bm0n1t0r")...)
	lines = append(lines, `{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>\n<task-id>bm0n1t0r</task-id>\n<summary>Monitor event</summary>\n<event>line</event>\n</task-notification>"}}`)
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 1 {
		t.Errorf("монитор после события во вложении должен быть жив, насчитано %d", got)
	}
}

// Запрос человека с картинкой — вложение того же вида, но со списком блоков;
// такая запись читаема и задач не трогает
func TestPendingBackgroundTasks_ImagePromptAttachmentReadable(t *testing.T) {
	var lines []string
	lines = append(lines, bashLaunch("toolu_01", "b1mage")...)
	lines = append(lines, `{"type":"attachment","attachment":{"type":"queued_command","commandMode":"prompt","prompt":[{"type":"text","text":"[Image #1] смотри"},{"type":"image","source":{}}]}}`)
	if got := pendingTasks(t, writeTranscript(t, lines)); got != 1 {
		t.Errorf("вложение с картинкой не завершение, насчитано %d", got)
	}
}
