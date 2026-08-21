package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Маркеры фоновых задач в транскрипте сессии Claude Code: результат запуска
// субагента, результат фоновой bash-команды и уведомление о завершении задачи
var (
	agentLaunched   = regexp.MustCompile(`(?s)Async agent launched successfully.*?agentId: ([A-Za-z0-9_-]+)`)
	bashLaunched    = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9_-]+)`)
	monitorLaunched = regexp.MustCompile(`Monitor started \(task ([A-Za-z0-9_-]+)`)
	taskNotified    = regexp.MustCompile(`<task-id>([A-Za-z0-9_-]+)</task-id>`)
	// Уведомление монитора о конце: явный статус либо истёкший таймаут. Все
	// остальные его уведомления — промежуточные события, монитор после них жив
	taskFinished = regexp.MustCompile(`<status>|\[Monitor timed out`)
)

// Задача без отчёта дольше этого срока считается брошенной и уведомления
// больше не глушит: вечный фоновый процесс иначе заглушил бы их до конца
// сессии. Просроченная живая задача даёт лишний звонок — меньшее зло
const pendingTaskTTL = 2 * time.Hour

// Будильник /loop живёт не дольше своей задержки плюс этот запас на рабочий
// ход после пробуждения: живой цикл к концу хода взводит будильник заново
// либо выключает его, а забытый не должен глушить уведомления до конца сессии
const wakeupGrace = 30 * time.Minute

// Записи транскрипта с вложениями бывают на мегабайты
const (
	transcriptLineInitial = 1024 * 1024
	transcriptLineMax     = 64 * 1024 * 1024
)

// transcriptRecord — строка транскрипта: тип записи и содержимое сообщения.
// Уведомление о задаче доезжает до транскрипта тремя видами записей, смотря
// когда оно пришло: между ходами — user-сообщением, посреди хода — вложением
// queued_command, и в обоих случаях — записями очереди queue-operation
// (enqueue при завершении задачи, remove при доставке модели)
type transcriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content messageContent `json:"content"`
	} `json:"message"`
	// Текст записи очереди queue-operation
	Operation string `json:"operation"`
	Content   string `json:"content"`
	// Вложение: уведомление, доставленное посреди хода. Запрос человека с
	// картинкой приходит тем же вложением, но списком блоков
	Attachment struct {
		Type        string         `json:"type"`
		CommandMode string         `json:"commandMode"`
		Prompt      messageContent `json:"prompt"`
	} `json:"attachment"`
}

// contentBlock — блок содержимого сообщения: вызов инструмента, его результат
// или текст
type contentBlock struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	Text      string         `json:"text"`
	Content   messageContent `json:"content"`
}

// messageContent — содержимое сообщения или результата инструмента: строка
// либо список блоков. Разбирается на месте, поэтому битое содержимое — ошибка
// записи, а не молча пустой список
type messageContent struct {
	Text   string
	Blocks []contentBlock
}

// UnmarshalJSON принимает строку, список блоков и null
func (c *messageContent) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case bytes.Equal(trimmed, []byte("null")):
		return nil
	case len(trimmed) > 0 && trimmed[0] == '"':
		return json.Unmarshal(trimmed, &c.Text)
	default:
		return json.Unmarshal(trimmed, &c.Blocks)
	}
}

// flatten собирает текст содержимого: строку либо тексты блоков
func (c messageContent) flatten() string {
	if c.Text != "" {
		return c.Text
	}
	var parts []string
	for _, block := range c.Blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// scanTranscript вызывает visit для каждой разобранной записи транскрипта.
// Нечитаемые строки не прерывают обход — остальная история остаётся полезной, —
// но считаются и возвращаются одной ошибкой после него
func scanTranscript(transcriptPath string, visit func(record transcriptRecord)) error {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return fmt.Errorf("cannot open transcript: %w", err)
	}
	// Файл открыт только на чтение — ошибка закрытия ни на что не влияет
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, transcriptLineInitial), transcriptLineMax)

	var errs []error
	line := 0
	for scanner.Scan() {
		line++
		var record transcriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", line, err))
			continue
		}
		visit(record)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cannot read transcript: %w", err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("transcript has %d unreadable records, first: %w", len(errs), errs[0])
	}
	return nil
}

// taskTracker сопоставляет запуски фоновых задач с уведомлениями об их
// завершении по ходу транскрипта
type taskTracker struct {
	// Извлечение идентификатора задачи из результата запустившего её вызова
	launchers map[string]*regexp.Regexp
	pending   map[string]time.Time
	// Мониторы шлют уведомление на каждое событие, а не только по концу —
	// вычёркивать их можно лишь по уведомлению с признаком завершения
	monitors map[string]bool
	// Записи, чью отметку времени прочитать не удалось: задача считается
	// без срока, а ошибка уходит вызывающему
	timestampErrs []error
}

func newTaskTracker() *taskTracker {
	return &taskTracker{
		launchers: map[string]*regexp.Regexp{},
		pending:   map[string]time.Time{},
		monitors:  map[string]bool{},
	}
}

// visit разбирает одну запись транскрипта
func (t *taskTracker) visit(record transcriptRecord) {
	switch record.Type {
	case "assistant":
		for _, block := range record.Message.Content.Blocks {
			t.visitToolUse(block)
		}
	case "user":
		// Уведомление между ходами приходит текстом сообщения
		if text := record.Message.Content.Text; text != "" {
			t.complete(text)
			return
		}
		for _, block := range record.Message.Content.Blocks {
			t.visitUserBlock(record.Timestamp, block)
		}
	case "attachment":
		// Уведомление посреди хода: вложение с запросом из очереди
		t.complete(record.Attachment.Prompt.flatten())
	case "queue-operation":
		// Запись очереди появляется при завершении задачи и при доставке;
		// задача завершена уже по первой из них, а в интерактивной сессии
		// очередь разбирается сразу, так что ход без неё не заканчивается
		t.complete(record.Content)
	}
}

// visitToolUse запоминает вызовы, запускающие фоновые задачи, и снимает
// задачи, остановленные явно
func (t *taskTracker) visitToolUse(block contentBlock) {
	if block.Type != "tool_use" {
		return
	}
	switch block.Name {
	case "Agent":
		t.launchers[block.ID] = agentLaunched
	case "Bash":
		if boolInput(block.Input, "run_in_background") {
			t.launchers[block.ID] = bashLaunched
		}
	case "Monitor":
		t.launchers[block.ID] = monitorLaunched
	case "TaskStop":
		// Остановленная задача уведомления уже не пришлёт
		delete(t.pending, stringInput(block.Input, "task_id"))
		delete(t.pending, stringInput(block.Input, "shell_id"))
	}
}

// visitUserBlock разбирает блок пользовательской записи: текст с уведомлением
// либо результат запускающего вызова с идентификатором задачи
func (t *taskTracker) visitUserBlock(timestamp string, block contentBlock) {
	switch block.Type {
	case "text":
		t.complete(block.Text)
	case "tool_result":
		extract, ok := t.launchers[block.ToolUseID]
		if !ok {
			return
		}
		m := extract.FindStringSubmatch(block.Content.flatten())
		if m == nil {
			return
		}
		t.pending[m[1]] = t.launchedAt(timestamp)
		if extract == monitorLaunched {
			t.monitors[m[1]] = true
		}
	}
}

// launchedAt разбирает отметку времени записи; нечитаемая отметка даёт
// нулевое время и запоминается как ошибка
func (t *taskTracker) launchedAt(timestamp string) time.Time {
	launched, err := recordTime(timestamp)
	if err != nil {
		t.timestampErrs = append(t.timestampErrs, err)
	}
	return launched
}

// recordTime разбирает отметку времени записи транскрипта. Запись без отметки
// даёт нулевое время без ошибки: срок у такой задачи не считается
func recordTime(timestamp string) (time.Time, error) {
	if timestamp == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, timestamp)
}

// complete вычёркивает задачи, упомянутые в уведомлении о завершении.
// Уведомление монитора считается завершением только с признаком конца:
// событие монитора приходит тем же уведомлением, но монитор после него жив
func (t *taskTracker) complete(text string) {
	if !strings.Contains(text, "<task-notification>") {
		return
	}
	finished := taskFinished.MatchString(text)
	for _, m := range taskNotified.FindAllStringSubmatch(text, -1) {
		if t.monitors[m[1]] && !finished {
			continue
		}
		delete(t.pending, m[1])
	}
}

// alive считает задачи, ещё ожидающие отчёта
func (t *taskTracker) alive() int {
	alive := 0
	for _, launched := range t.pending {
		// Нулевое время (нет отметки в записи) не повод считать задачу брошенной
		if launched.IsZero() || time.Since(launched) < pendingTaskTTL {
			alive++
		}
	}
	return alive
}

// PendingBackgroundTasks считает фоновые задачи сессии, о завершении которых
// уведомления ещё не приходило. Запуски и завершения ищутся не по всему тексту,
// а в записях транскрипта строго определённого вида: содержимое прочитанных
// файлов тоже попадает в транскрипт и подстрочным поиском давало бы ложные
// срабатывания. Завершение распознаётся в любой из записей, которыми
// уведомление попадает в транскрипт (user, attachment, queue-operation): иначе
// задача, отчитавшаяся посреди хода, считалась бы живой до истечения
// pendingTaskTTL и глушила бы уведомления всё это время. Пустой путь —
// событие без транскрипта, задач нет.
//
// Ошибка сопровождает счёт, а не заменяет его: нечитаемые записи пропущены,
// и по остальным счёт всё равно посчитан.
//
// Известное ограничение: задача, погибшая без уведомления (убитый процесс,
// закрытие сессии), остаётся «живой» до конца транскрипта
func PendingBackgroundTasks(transcriptPath string) (int, error) {
	if transcriptPath == "" {
		return 0, nil
	}
	tracker := newTaskTracker()
	err := scanTranscript(transcriptPath, tracker.visit)
	if err == nil && len(tracker.timestampErrs) > 0 {
		err = fmt.Errorf("%d launch records without readable timestamp, first: %w",
			len(tracker.timestampErrs), tracker.timestampErrs[0])
	}
	return tracker.alive(), err
}

// AwaitingScheduledWakeup сообщает, взведён ли в сессии будильник ScheduleWakeup
// (динамический /loop): остановка хода с взведённым будильником — не завершение
// работы, Claude вернётся сам. Взведённым считается последний по транскрипту
// вызов ScheduleWakeup без stop:true, пока не истекла его задержка с запасом
// wakeupGrace. Ищется только tool_use в ассистентских записях: цитаты в
// прочитанных файлах и результатах команд вызовами не являются.
// Ошибка сопровождает ответ, а не заменяет его: он посчитан по читаемым записям
func AwaitingScheduledWakeup(transcriptPath string) (bool, error) {
	if transcriptPath == "" {
		return false, nil
	}

	var armed bool
	var deadline time.Time
	var timestampErrs []error

	err := scanTranscript(transcriptPath, func(record transcriptRecord) {
		if record.Type != "assistant" {
			return
		}
		for _, block := range record.Message.Content.Blocks {
			if block.Type != "tool_use" || block.Name != "ScheduleWakeup" {
				continue
			}
			if boolInput(block.Input, "stop") {
				armed = false
				continue
			}
			armed = true
			deadline = time.Time{}
			at, err := recordTime(record.Timestamp)
			if err != nil {
				timestampErrs = append(timestampErrs, err)
			}
			if !at.IsZero() {
				delay := time.Duration(numberInput(block.Input, "delaySeconds") * float64(time.Second))
				deadline = at.Add(delay + wakeupGrace)
			}
		}
	})
	if err == nil && len(timestampErrs) > 0 {
		err = fmt.Errorf("%d ScheduleWakeup records without readable timestamp, first: %w",
			len(timestampErrs), timestampErrs[0])
	}

	// Нулевой дедлайн (запись без отметки времени) не повод считать будильник забытым
	return armed && (deadline.IsZero() || time.Now().Before(deadline)), err
}

// boolInput извлекает булево поле из параметров вызова инструмента
func boolInput(input map[string]any, key string) bool {
	value, _ := input[key].(bool)
	return value
}

// numberInput извлекает числовое поле из параметров вызова инструмента
func numberInput(input map[string]any, key string) float64 {
	value, _ := input[key].(float64)
	return value
}

// stringInput извлекает строковое поле из параметров вызова инструмента
func stringInput(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}
