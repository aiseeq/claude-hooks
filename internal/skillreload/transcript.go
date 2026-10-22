// Package skillreload доносит до агента правки скиллов и файлов инструкций
// (CLAUDE.md, AGENTS.md), сделанные после того, как их текст попал в контекст.
//
// Текст скилла попадает в контекст сессии один раз, в момент вызова (Skill
// или /имя), и дальше это снимок: сжатие подкладывает тот же текст, правка
// файла до сессии не доходит. CLAUDE.md Claude Code загружает при старте и
// показывает только первую правку после загрузки; после неё и после сжатия
// правки до сессии не доходят. Сессии живут днями, а Сон правит инструкции
// ночью. Хук читает транскрипт с прошлого места, помнит текст, который видел
// агент, и кладёт в контекст diff от него к файлу на диске.
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

// memoryFileNames — файлы инструкций, правки которых хук доносит до сессии
var memoryFileNames = map[string]bool{"CLAUDE.md": true, "AGENTS.md": true}

// isWatchedName — файл, за которым следит хук: SKILL.md или файл инструкций
func isWatchedName(path string) bool {
	name := filepath.Base(path)
	return name == skillFileName || memoryFileNames[name]
}

// eventKind — что произошло в транскрипте с отслеживаемым файлом
type eventKind int

const (
	// evSkillLoad — вызов скилла: снимок тела SKILL.md в контексте
	evSkillLoad eventKind = iota + 1
	// evSkillReinject — сжатие подложило снимок вызванного скилла заново
	evSkillReinject
	// evMemoryLoad — Claude Code положил в контекст файл инструкций
	evMemoryLoad
	// evCompact — сжатие контекста
	evCompact
	// evNote — заметка Claude Code «файл изменён на диске» (edited_text_file)
	evNote
	// evRead — агент прочёл файл целиком
	evRead
	// evEdit — агент сам изменил файл (Edit, Write, MultiEdit)
	evEdit
	// evAssistant — ответ модели: Claude Code уже собрал вложения хода
	evAssistant
	// evHook — запись type=user: в этот момент срабатывает хук (сухой прогон)
	evHook
)

// transcriptEvent — событие транскрипта
type transcriptEvent struct {
	Kind   eventKind
	At     time.Time
	Offset int64  // смещение строки в транскрипте
	Path   string // файл на диске: SKILL.md или файл инструкций
	Dir    string // каталог скилла
	Text   string // текст в контексте: снимок скилла или файла инструкций
	// Nested — файл инструкций подгружен по ходу работы (nested_memory), а не
	// списком при старте или после сжатия
	Nested bool
	// Delivered — заметка Claude Code показала изменения (непустой фрагмент)
	Delivered bool
}

// readBufferSize — буфер чтения транскрипта. Строки длиннее (результаты
// инструментов в мегабайты) собираются по частям
const readBufferSize = 1 << 20

// scanResult — что дал проход по транскрипту
type scanResult struct {
	Events   []transcriptEvent
	Offset   int64 // место, с которого читать в следующий раз
	Complete bool  // дочитан до конца файла
	// Broken — строки, похожие на нужную запись, но нечитаемые. Чтение не
	// прерывают: остальные записи транскрипта от них не зависят
	Broken []error
}

// lineHandler получает каждую целую строку транскрипта и её смещение
type lineHandler func(line []byte, at int64) error

// scanTranscript читает транскрипт с offset до конца или до deadline и
// собирает события отслеживаемых файлов. Незаконченная последняя строка
// (Claude Code ещё пишет её) не засчитывается: следующий проход прочтёт её
// целиком. withHooks добавляет события хука (записи type=user) — для сухого
// прогона
func scanTranscript(path string, offset int64, deadline time.Time, withHooks bool) (scanResult, error) {
	result := scanResult{Offset: offset}
	complete, next, err := forEachLine(path, offset, deadline, func(line []byte, at int64) error {
		events, err := parseLine(line, withHooks)
		if err != nil {
			result.Broken = append(result.Broken, fmt.Errorf("%s at byte %d: %w", path, at, err))
		}
		for i := range events {
			events[i].Offset = at
		}
		result.Events = append(result.Events, events...)
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

// record — поля записи транскрипта, нужные хуку
type record struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsMeta    bool   `json:"isMeta"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment *attachment `json:"attachment"`
}

// attachment — вложение Claude Code. Поля разных типов вложений вместе
type attachment struct {
	Type string `json:"type"`
	// edited_text_file
	Filename string `json:"filename"`
	Snippet  string `json:"snippet"`
	// instructions
	Files []memoryContent `json:"files"`
	// nested_memory
	Content *memoryContent `json:"content"`
	// invoked_skills
	Skills []struct {
		Content string `json:"content"`
	} `json:"skills"`
}

// memoryContent — файл инструкций во вложении: путь и текст, как его видит агент
type memoryContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// toolInput — поля вызова Read, Edit, Write, MultiEdit, нужные хуку
type toolInput struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset"`
	Limit    *int   `json:"limit"`
}

// Признаки строк, которые стоит разбирать: остальные строки транскрипта
// (сотни мегабайт) хук пропускает без декодирования JSON
var (
	markerMeta      = []byte(`"isMeta":true`)
	markerHeader    = []byte(loadHeader)
	markerAssistant = []byte(`"type":"assistant"`)
	markerUser      = []byte(`"type":"user"`)
	markerCompact   = []byte(`"compact_boundary"`)
	markerAttach    = [][]byte{
		[]byte(`"type":"instructions"`),
		[]byte(`"type":"nested_memory"`),
		[]byte(`"type":"edited_text_file"`),
		[]byte(`"type":"invoked_skills"`),
	}
)

// parseLine узнаёт в строке транскрипта события отслеживаемых файлов. Ошибка
// — строка похожа на нужную запись, но не читается
func parseLine(line []byte, withHooks bool) ([]transcriptEvent, error) {
	if !worthParsing(line, withHooks) {
		return nil, nil
	}
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, fmt.Errorf("transcript record is not JSON: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		// Записи без времени (сводки, служебные) хуку не нужны, кроме
		// вызова скилла — у него время обязательно
		if rec.Type == "user" && rec.IsMeta && bytes.Contains(line, markerHeader) {
			return nil, fmt.Errorf("skill load record has no time: %w", err)
		}
		return nil, nil
	}

	switch {
	case rec.Type == "user":
		return parseUser(rec, at, withHooks)
	case rec.Type == "assistant":
		return parseAssistant(rec, at)
	case rec.Type == "system" && rec.Subtype == "compact_boundary":
		return []transcriptEvent{{Kind: evCompact, At: at}}, nil
	case rec.Attachment != nil:
		return parseAttachment(rec.Attachment, at)
	}
	return nil, nil
}

// worthParsing — дешёвая проверка по байтам до декодирования строки
func worthParsing(line []byte, withHooks bool) bool {
	if bytes.Contains(line, markerAssistant) || bytes.Contains(line, markerCompact) {
		return true
	}
	if bytes.Contains(line, markerHeader) && bytes.Contains(line, markerMeta) {
		return true
	}
	if withHooks && bytes.Contains(line, markerUser) {
		return true
	}
	for _, marker := range markerAttach {
		if bytes.Contains(line, marker) {
			return true
		}
	}
	return false
}

// parseUser — вызов скилла (служебное сообщение пользователя, текст которого
// начинается с каталога скилла; так Claude Code пишет и вызов Skill, и
// /имя) и событие хука для сухого прогона. Копии текста в результатах
// инструментов и в api-request-blob служебными сообщениями пользователя не
// бывают
func parseUser(rec record, at time.Time, withHooks bool) ([]transcriptEvent, error) {
	var events []transcriptEvent
	if withHooks {
		events = append(events, transcriptEvent{Kind: evHook, At: at})
	}
	if !rec.IsMeta {
		return events, nil
	}
	text, found, err := loadText(rec.Message.Content)
	if err != nil || !found {
		return events, err
	}
	dir, snapshot, err := splitSnapshot(text)
	if err != nil {
		return events, err
	}
	load := transcriptEvent{Kind: evSkillLoad, At: at, Dir: dir, Path: filepath.Join(dir, skillFileName), Text: snapshot}
	// Хук срабатывает до того, как вызов попал в контекст: снимок раньше события
	return append([]transcriptEvent{load}, events...), nil
}

// parseAssistant — ответ модели и вызовы Read, Edit, Write отслеживаемых
// файлов в нём
func parseAssistant(rec record, at time.Time) ([]transcriptEvent, error) {
	events := []transcriptEvent{{Kind: evAssistant, At: at}}
	content := bytes.TrimSpace(rec.Message.Content)
	if len(content) == 0 || content[0] != '[' {
		return events, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return events, fmt.Errorf("assistant content blocks are malformed: %w", err)
	}
	for _, block := range blocks {
		if block.Type != "tool_use" {
			continue
		}
		kind, ok := toolKinds[block.Name]
		if !ok {
			continue
		}
		var input toolInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			return events, fmt.Errorf("%s input is malformed: %w", block.Name, err)
		}
		if !filepath.IsAbs(input.FilePath) || !isWatchedName(input.FilePath) {
			continue
		}
		// Чтение части файла не кладёт в контекст весь текст
		if kind == evRead && (input.Offset != nil || input.Limit != nil) {
			continue
		}
		events = append(events, transcriptEvent{Kind: kind, At: at, Path: filepath.Clean(input.FilePath)})
	}
	return events, nil
}

// toolKinds — инструменты, после которых текст файла у агента известен
var toolKinds = map[string]eventKind{
	"Read":      evRead,
	"Edit":      evEdit,
	"MultiEdit": evEdit,
	"Write":     evEdit,
}

// parseAttachment — файлы инструкций в контексте, заметки Claude Code об
// изменённых файлах и снимки скиллов, подложенные после сжатия
func parseAttachment(a *attachment, at time.Time) ([]transcriptEvent, error) {
	var events []transcriptEvent
	switch a.Type {
	case "instructions":
		for _, file := range a.Files {
			if memoryFileNames[filepath.Base(file.Path)] && filepath.IsAbs(file.Path) {
				events = append(events, transcriptEvent{Kind: evMemoryLoad, At: at, Path: filepath.Clean(file.Path), Text: file.Content})
			}
		}
	case "nested_memory":
		if a.Content != nil && memoryFileNames[filepath.Base(a.Content.Path)] && filepath.IsAbs(a.Content.Path) {
			events = append(events, transcriptEvent{Kind: evMemoryLoad, At: at, Path: filepath.Clean(a.Content.Path), Text: a.Content.Content, Nested: true})
		}
	case "edited_text_file":
		if isWatchedName(a.Filename) && filepath.IsAbs(a.Filename) {
			events = append(events, transcriptEvent{Kind: evNote, At: at, Path: filepath.Clean(a.Filename), Delivered: a.Snippet != ""})
		}
	case "invoked_skills":
		for _, skill := range a.Skills {
			if !strings.HasPrefix(skill.Content, loadHeader) {
				continue
			}
			dir, snapshot, err := splitSnapshot(skill.Content)
			if err != nil {
				return events, err
			}
			events = append(events, transcriptEvent{Kind: evSkillReinject, At: at, Dir: dir, Path: filepath.Join(dir, skillFileName), Text: snapshot})
		}
	}
	return events, nil
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
	return filepath.Clean(dir), strings.TrimSpace(body), nil
}
