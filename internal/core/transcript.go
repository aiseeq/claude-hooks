package core

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"
)

// Маркеры фоновых задач в транскрипте сессии Claude Code: результат запуска
// субагента, результат фоновой bash-команды и уведомление о завершении задачи
var (
	agentLaunched = regexp.MustCompile(`(?s)Async agent launched successfully.*?agentId: ([A-Za-z0-9_-]+)`)
	bashLaunched  = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9_-]+)`)
	taskNotified  = regexp.MustCompile(`<task-id>([A-Za-z0-9_-]+)</task-id>`)
)

// Задача без отчёта дольше этого срока считается брошенной и уведомления
// больше не глушит: вечный фоновый процесс иначе заглушил бы их до конца
// сессии. Просроченная живая задача даёт лишний звонок — меньшее зло
const pendingTaskTTL = 2 * time.Hour

// Будильник /loop живёт не дольше своей задержки плюс этот запас на рабочий
// ход после пробуждения: живой цикл к концу хода взводит будильник заново
// либо выключает его, а забытый не должен глушить уведомления до конца сессии
const wakeupGrace = 30 * time.Minute

// transcriptRecord — строка транскрипта: тип записи и содержимое сообщения.
// Содержимое бывает строкой или списком блоков, поэтому разбирается отложенно
type transcriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentBlock — блок содержимого сообщения: вызов инструмента, его результат
// или текст
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
}

// PendingBackgroundTasks считает фоновые задачи сессии, о завершении которых
// уведомления ещё не приходило. Запуски и завершения ищутся не по всему тексту,
// а в записях транскрипта строго определённого вида: содержимое прочитанных
// файлов тоже попадает в транскрипт и подстрочным поиском давало бы ложные
// срабатывания.
//
// Известное ограничение: задача, погибшая без уведомления (убитый процесс,
// закрытие сессии), остаётся «живой» до конца транскрипта
func PendingBackgroundTasks(transcriptPath string) int {
	if transcriptPath == "" {
		return 0
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return 0
	}
	// Файл открыт только на чтение — ошибка закрытия ни на что не влияет
	defer func() { _ = f.Close() }()

	// Извлечение идентификатора задачи из результата запустившего её вызова
	launchers := map[string]*regexp.Regexp{}
	pending := map[string]time.Time{}

	scanner := bufio.NewScanner(f)
	// Записи транскрипта с вложениями бывают на мегабайты
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		var record transcriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}

		switch record.Type {
		case "assistant":
			for _, block := range contentBlocks(record.Message.Content) {
				if block.Type != "tool_use" {
					continue
				}
				switch block.Name {
				case "Agent":
					launchers[block.ID] = agentLaunched
				case "Bash":
					if boolInput(block.Input, "run_in_background") {
						launchers[block.ID] = bashLaunched
					}
				case "TaskStop":
					// Остановленная задача уведомления уже не пришлёт
					delete(pending, stringInput(block.Input, "task_id"))
					delete(pending, stringInput(block.Input, "shell_id"))
				}
			}

		case "user":
			// Уведомление о завершении приходит текстом сообщения
			var text string
			if err := json.Unmarshal(record.Message.Content, &text); err == nil {
				completeNotifiedTasks(pending, text)
				continue
			}

			for _, block := range contentBlocks(record.Message.Content) {
				switch block.Type {
				case "text":
					completeNotifiedTasks(pending, block.Text)
				case "tool_result":
					extract, ok := launchers[block.ToolUseID]
					if !ok {
						continue
					}
					if m := extract.FindStringSubmatch(flattenContent(block.Content)); m != nil {
						launched, _ := time.Parse(time.RFC3339, record.Timestamp)
						pending[m[1]] = launched
					}
				}
			}
		}
	}

	alive := 0
	for _, launched := range pending {
		// Нулевое время (нет отметки в записи) не повод считать задачу брошенной
		if launched.IsZero() || time.Since(launched) < pendingTaskTTL {
			alive++
		}
	}
	return alive
}

// AwaitingScheduledWakeup сообщает, взведён ли в сессии будильник ScheduleWakeup
// (динамический /loop): остановка хода с взведённым будильником — не завершение
// работы, Claude вернётся сам. Взведённым считается последний по транскрипту
// вызов ScheduleWakeup без stop:true, пока не истекла его задержка с запасом
// wakeupGrace. Ищется только tool_use в ассистентских записях: цитаты в
// прочитанных файлах и результатах команд вызовами не являются
func AwaitingScheduledWakeup(transcriptPath string) bool {
	if transcriptPath == "" {
		return false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return false
	}
	// Файл открыт только на чтение — ошибка закрытия ни на что не влияет
	defer func() { _ = f.Close() }()

	var armed bool
	var deadline time.Time

	scanner := bufio.NewScanner(f)
	// Записи транскрипта с вложениями бывают на мегабайты
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		var record transcriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record.Type != "assistant" {
			continue
		}
		for _, block := range contentBlocks(record.Message.Content) {
			if block.Type != "tool_use" || block.Name != "ScheduleWakeup" {
				continue
			}
			if boolInput(block.Input, "stop") {
				armed = false
				continue
			}
			armed = true
			deadline = time.Time{}
			if at, err := time.Parse(time.RFC3339, record.Timestamp); err == nil {
				delay := time.Duration(numberInput(block.Input, "delaySeconds") * float64(time.Second))
				deadline = at.Add(delay + wakeupGrace)
			}
		}
	}

	// Нулевой дедлайн (запись без отметки времени) не повод считать будильник забытым
	return armed && (deadline.IsZero() || time.Now().Before(deadline))
}

// completeNotifiedTasks вычёркивает задачи, упомянутые в уведомлении о завершении
func completeNotifiedTasks(pending map[string]time.Time, text string) {
	if !strings.Contains(text, "<task-notification>") {
		return
	}
	for _, m := range taskNotified.FindAllStringSubmatch(text, -1) {
		delete(pending, m[1])
	}
}

// contentBlocks разбирает содержимое сообщения как список блоков
func contentBlocks(raw json.RawMessage) []contentBlock {
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// flattenContent собирает текст из содержимого результата инструмента:
// строки либо списка текстовых блоков
func flattenContent(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var parts []string
	for _, block := range contentBlocks(raw) {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// boolInput извлекает булево поле из параметров вызова инструмента
func boolInput(raw json.RawMessage, key string) bool {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return false
	}
	value, _ := input[key].(bool)
	return value
}

// numberInput извлекает числовое поле из параметров вызова инструмента
func numberInput(raw json.RawMessage, key string) float64 {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return 0
	}
	value, _ := input[key].(float64)
	return value
}

// stringInput извлекает строковое поле из параметров вызова инструмента
func stringInput(raw json.RawMessage, key string) string {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	value, _ := input[key].(string)
	return value
}
