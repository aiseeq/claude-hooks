// Package skillreload напоминает агенту вызвать скилл заново, когда SKILL.md
// изменился после вызова.
//
// Текст скилла попадает в контекст сессии один раз, в момент вызова (Skill
// или /имя), и дальше это снимок: сжатие подкладывает тот же текст, правка
// файла до сессии не доходит. Сессии живут днями, а Сон правит скиллы ночью.
// Вызов скилла виден в транскрипте записью с текстом снимка; хук читает
// транскрипт с прошлого места и сравнивает файл с тем, что видел агент.
package skillreload

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// loadHeader — начало текста, которым Claude Code кладёт скилл в контекст:
// за ним каталог скилла, пустая строка и тело SKILL.md без шапки
const loadHeader = "Base directory for this skill: "

// argumentsMarker — хвост, который Claude Code дописывает к снимку, когда
// скилл вызван с аргументами (/имя аргументы)
const argumentsMarker = "\n\nARGUMENTS: "

// Load — вызов скилла, найденный в транскрипте
type Load struct {
	Dir      string
	At       time.Time
	Snapshot string // тело снимка без шапки и аргументов, как его видел агент
}

// readBufferSize — буфер чтения транскрипта. Строки длиннее (результаты
// инструментов в мегабайты) собираются по частям
const readBufferSize = 1 << 20

// scanResult — что дал проход по транскрипту
type scanResult struct {
	Loads    []Load
	Offset   int64 // место, с которого читать в следующий раз
	Complete bool  // дочитан до конца файла
	// Broken — строки, похожие на вызов, но нечитаемые. Чтение не
	// прерывают: остальные вызовы транскрипта от них не зависят
	Broken []error
}

// lineHandler получает каждую целую строку транскрипта и её смещение
type lineHandler func(line []byte, at int64) error

// scanTranscript читает транскрипт с offset до конца или до deadline и
// собирает вызовы скиллов. Незаконченная последняя строка (Claude Code ещё
// пишет её) не засчитывается: следующий проход прочтёт её целиком
func scanTranscript(path string, offset int64, deadline time.Time) (scanResult, error) {
	result := scanResult{Offset: offset}
	complete, next, err := forEachLine(path, offset, deadline, func(line []byte, at int64) error {
		load, ok, err := parseLoad(line)
		switch {
		case err != nil:
			result.Broken = append(result.Broken, fmt.Errorf("%s at byte %d: %w", path, at, err))
		case ok:
			result.Loads = append(result.Loads, load)
		}
		return nil
	})
	result.Offset = next
	result.Complete = complete
	return result, err
}

// forEachLine вызывает handle для каждой целой строки файла начиная с offset.
// Возвращает, дочитан ли файл, и смещение после последней целой строки.
// Нулевой deadline — без ограничения времени
func forEachLine(path string, offset int64, deadline time.Time, handle lineHandler) (bool, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, offset, fmt.Errorf("cannot open transcript: %w", err)
	}
	defer file.Close()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return false, offset, fmt.Errorf("cannot seek transcript %s: %w", path, err)
	}

	reader := bufio.NewReaderSize(file, readBufferSize)
	var long []byte
	lines := 0
	for {
		chunk, err := reader.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			long = append(long, chunk...)
			continue
		case errors.Is(err, io.EOF):
			// Хвост без перевода строки — запись, которую ещё дописывают
			return true, offset, nil
		case err != nil:
			return false, offset, fmt.Errorf("cannot read transcript %s: %w", path, err)
		}

		line := chunk
		if long != nil {
			long = append(long, chunk...)
			line = long
		}
		if err := handle(line, offset); err != nil {
			return false, offset, err
		}
		offset += int64(len(line))
		long = long[:0:0]

		lines++
		if !deadline.IsZero() && lines%256 == 0 && time.Now().After(deadline) {
			return false, offset, nil
		}
	}
}

// loadRecord — поля записи транскрипта, нужные для поиска вызова скилла
type loadRecord struct {
	Type      string `json:"type"`
	IsMeta    bool   `json:"isMeta"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseLoad узнаёт в строке транскрипта вызов скилла: служебное сообщение
// пользователя, текст которого начинается с каталога скилла. Так Claude Code
// пишет и вызов Skill, и /имя; копии текста в результатах инструментов и в
// api-request-blob служебными сообщениями пользователя не бывают. Ошибка —
// строка похожа на вызов, но не читается
func parseLoad(line []byte) (Load, bool, error) {
	if !bytes.Contains(line, []byte(loadHeader)) || !bytes.Contains(line, []byte(`"isMeta":true`)) {
		return Load{}, false, nil
	}

	var record loadRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return Load{}, false, fmt.Errorf("skill load record is not JSON: %w", err)
	}
	if record.Type != "user" || !record.IsMeta {
		return Load{}, false, nil
	}

	text, found, err := loadText(record.Message.Content)
	if err != nil || !found {
		return Load{}, false, err
	}
	at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return Load{}, false, fmt.Errorf("skill load record has no time: %w", err)
	}

	dir, snapshot, err := splitSnapshot(text)
	if err != nil {
		return Load{}, false, err
	}
	return Load{Dir: dir, At: at, Snapshot: snapshot}, true, nil
}

// loadText достаёт текст снимка: content бывает строкой или списком блоков.
// found ложно, если снимка в сообщении нет
func loadText(raw json.RawMessage) (text string, found bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false, fmt.Errorf("skill load record has no content")
	}
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", false, fmt.Errorf("skill load content string is malformed: %w", err)
		}
		return text, strings.HasPrefix(text, loadHeader), nil
	}

	var blocks []contentBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", false, fmt.Errorf("skill load content blocks are malformed: %w", err)
	}
	for _, block := range blocks {
		if block.Type == "text" && strings.HasPrefix(block.Text, loadHeader) {
			return block.Text, true, nil
		}
	}
	return "", false, nil
}

// splitSnapshot делит текст снимка на каталог скилла и тело
func splitSnapshot(text string) (string, string, error) {
	rest := strings.TrimPrefix(text, loadHeader)
	dir, body, found := strings.Cut(rest, "\n")
	dir = strings.TrimSpace(dir)
	if !found || !filepath.IsAbs(dir) {
		return "", "", fmt.Errorf("skill load names no absolute skill dir: %q", dir)
	}
	if i := strings.LastIndex(body, argumentsMarker); i >= 0 {
		body = body[:i]
	}
	return dir, strings.TrimSpace(body), nil
}
