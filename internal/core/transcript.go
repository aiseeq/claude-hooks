package core

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// Маркеры фоновых задач в транскрипте сессии Claude Code: результат запуска
// субагента, результат фоновой bash-команды и уведомление о завершении задачи
var (
	agentLaunched = regexp.MustCompile(`(?s)Async agent launched successfully.*?agentId: ([A-Za-z0-9_-]+)`)
	bashLaunched  = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9_-]+)`)
	taskNotified  = regexp.MustCompile(`<task-id>([A-Za-z0-9_-]+)</task-id>`)
)

// transcriptRecord — строка транскрипта: тип записи и содержимое сообщения.
// Содержимое бывает строкой или списком блоков, поэтому разбирается отложенно
type transcriptRecord struct {
	Type    string `json:"type"`
	Message struct {
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
	pending := map[string]bool{}

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
						pending[m[1]] = true
					}
				}
			}
		}
	}

	return len(pending)
}

// completeNotifiedTasks вычёркивает задачи, упомянутые в уведомлении о завершении
func completeNotifiedTasks(pending map[string]bool, text string) {
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

// stringInput извлекает строковое поле из параметров вызова инструмента
func stringInput(raw json.RawMessage, key string) string {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	value, _ := input[key].(string)
	return value
}
