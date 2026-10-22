package questioncheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Окно чтения хвоста транскрипта: транскрипт сессии бывает в сотни МБ и
// целиком в таймаут хука не укладывается, а нужное — последние записи. Окно
// растёт вдвое, пока не найдена прошлая реплика человека или не исчерпан
// предел: у work-admin между репликами человека бывают сотни сообщений сессий
const (
	tailWindowStart = 1 << 20
	tailWindowMax   = 32 << 20
)

// Tail — то, что проверке нужно из транскрипта
type Tail struct {
	// AssistantUUID — uuid последней записи ассистента с текстом: сообщение,
	// на котором сработал хук. По нему итог недели находит, что было дальше
	AssistantUUID string
	// HumanReply — последняя реплика человека (не сообщение другой сессии)
	HumanReply string
	// HumanFound — реплика найдена в пределах окна
	HumanFound bool
}

type transcriptRecord struct {
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	Origin *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	IsSidechain bool `json:"isSidechain"`
	Message     *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment *struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
		Origin *struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	} `json:"attachment"`
}

// ReadTail читает хвост транскрипта: uuid последнего текста ассистента и
// последнюю реплику человека — набранную в приглашении (type user,
// origin human) или поставленную в очередь посреди хода (queued_command)
func ReadTail(path string) (Tail, error) {
	file, err := os.Open(path)
	if err != nil {
		return Tail{}, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Tail{}, fmt.Errorf("stat transcript: %w", err)
	}
	size := info.Size()

	var tail Tail
	for window := int64(tailWindowStart); ; window *= 2 {
		if window > size {
			window = size
		}
		buf := make([]byte, window)
		if _, err := file.ReadAt(buf, size-window); err != nil && err != io.EOF {
			return Tail{}, fmt.Errorf("read transcript tail: %w", err)
		}
		// Первая строка окна обрезана, если окно не с начала файла
		if window < size {
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				buf = buf[i+1:]
			}
		}
		tail = scanTail(buf)
		if tail.HumanFound || window >= size || window >= tailWindowMax {
			return tail, nil
		}
	}
}

// scanTail идёт по строкам с конца
func scanTail(buf []byte) Tail {
	var tail Tail
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		// Разбор JSON только у строк-кандидатов: в хвосте мегабайты служебных
		// записей (api-request-blob), их разбор съел бы бюджет хука
		wantAssistant := tail.AssistantUUID == "" && bytes.Contains(line, []byte(`"type":"assistant"`))
		if !wantAssistant && !bytes.Contains(line, []byte(`"kind":"human"`)) {
			continue
		}
		var record transcriptRecord
		if json.Unmarshal(line, &record) != nil || record.IsSidechain {
			continue
		}
		if tail.AssistantUUID == "" && record.Type == "assistant" && record.Message != nil && contentText(record.Message.Content) != "" {
			tail.AssistantUUID = record.UUID
		}
		if text, ok := humanText(record); ok {
			tail.HumanReply = text
			tail.HumanFound = true
			return tail
		}
	}
	return tail
}

func humanText(record transcriptRecord) (string, bool) {
	switch {
	case record.Type == "user" && record.Origin != nil && record.Origin.Kind == "human" && record.Message != nil:
		return contentText(record.Message.Content), true
	case record.Type == "attachment" && record.Attachment != nil && record.Attachment.Type == "queued_command" &&
		record.Attachment.Origin != nil && record.Attachment.Origin.Kind == "human":
		return record.Attachment.Prompt, true
	}
	return "", false
}

// contentText — текст содержимого сообщения: строка или текстовые части
func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}
