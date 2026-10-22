package questioncheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Вердикты в журнале
const (
	VerdictPass       = "pass"
	VerdictBlock      = "block"
	VerdictError      = "error"
	VerdictTimeout    = "timeout"
	VerdictAfterBlock = "after_block"
)

// Event — вход Stop, нужный проверке
type Event struct {
	SessionID      string
	TranscriptPath string
	AgentID        string
	StopHookActive bool
	LastMessage    string
	// ClaudePID — CLAUDE_PID из окружения хука: по нему читается имя сессии
	ClaudePID string
}

// Checker — проверка вопросов в конце хода
type Checker struct {
	Scope       Scope
	Journals    Journals
	Judge       Judge
	JournalPath string
	Now         func() time.Time
	// Budget — сколько ждать судью; таймаут хука в settings.json должен быть
	// больше на чтение журналов и запуск
	Budget time.Duration
	// Warn — запись в лог хука
	Warn func(msg string, args ...any)
}

// Result — что хук отдаёт Claude Code
type Result struct {
	// Block — остановить конец хода, Reason — что агент прочтёт
	Block  bool
	Reason string
	// Skip — почему проверка не делалась (для лога хука)
	Skip string
	// Verdict — вердикт, если судью звали
	Verdict string
}

// Entry — строка журнала срабатываний: по нему Сон считает итог недели
type Entry struct {
	Time       string    `json:"time"`
	Session    string    `json:"session"`
	SessionID  string    `json:"session_id"`
	Transcript string    `json:"transcript"`
	MessageID  string    `json:"message_uuid,omitempty"`
	Questions  []string  `json:"questions"`
	Verdict    string    `json:"verdict"`
	Reason     string    `json:"reason,omitempty"`
	Lishnie    []Finding `json:"lishnie,omitempty"`
	Model      string    `json:"model,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	Usage      *Usage    `json:"usage,omitempty"`
	HumanFound bool      `json:"human_reply_found"`
	Excerpts   int       `json:"excerpts"`
	// Message — сообщение после остановки (after_block): что агент сделал
	// дальше, без чтения транскрипта
	Message string `json:"message,omitempty"`
}

// afterBlockRunes — сколько текста хода после остановки сохраняет журнал
const afterBlockRunes = 4000

// DefaultJournalPath — $XDG_STATE_HOME/claude-hooks/question-check.jsonl,
// рядом с limits-history.jsonl и skill-reload
func DefaultJournalPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-hooks", "question-check.jsonl"), nil
}

// Run проверяет конец хода. Всё, кроме вердикта «лишний», пропускает агента:
// сбой судьи не повод держать человека без ответа
func (c Checker) Run(ctx context.Context, event Event) Result {
	now := c.Now()
	gate := c.Scope.Gate(now, event)
	if gate.Status == GateSkip {
		return Result{Skip: gate.Skip}
	}

	questions := gate.Questions
	entry := Entry{
		Time:       now.Format(time.RFC3339),
		Session:    gate.Target.Name,
		SessionID:  event.SessionID,
		Transcript: event.TranscriptPath,
		Questions:  Texts(questions),
	}

	if gate.Status == GateAfterBlock {
		// Ход после остановки: повторно не блокировать. В журнал — что агент
		// дописал, по этому итог недели видит, нашёл ли он ответ сам
		entry.Verdict = VerdictAfterBlock
		entry.Message = truncateRunes(event.LastMessage, afterBlockRunes)
		c.fillTail(&entry, event.TranscriptPath)
		c.write(entry)
		return Result{Skip: gate.Skip}
	}

	tail := c.fillTail(&entry, event.TranscriptPath)
	excerpts, err := c.Journals.Find(questions, time.Time{})
	if err != nil {
		// Без выдержек судья работает как в сухом прогоне: по тексту и реплике
		c.Warn("question check journals unavailable", "error", err)
	}
	entry.Excerpts = len(excerpts.Replies) + len(excerpts.Decisions)

	input := Case{HumanReply: tail.HumanReply, Answer: event.LastMessage, Excerpts: excerpts}.Input()
	judgeCtx, cancel := context.WithTimeout(ctx, c.Budget)
	defer cancel()
	started := time.Now()
	verdict, usage, err := c.Judge.Judge(judgeCtx, input)
	entry.LatencyMS = time.Since(started).Milliseconds()
	entry.Model = c.Judge.Model()
	if usage != (Usage{}) {
		entry.Usage = &usage
	}

	switch {
	case err != nil && errors.Is(judgeCtx.Err(), context.DeadlineExceeded):
		entry.Verdict = VerdictTimeout
		entry.Reason = err.Error()
	case err != nil:
		entry.Verdict = VerdictError
		entry.Reason = err.Error()
	case verdict.Block():
		entry.Verdict = VerdictBlock
		entry.Lishnie = verdict.Lishnie
		entry.Reason = BlockReason(verdict)
	default:
		entry.Verdict = VerdictPass
		entry.Lishnie = verdict.Lishnie
	}
	c.write(entry)

	if entry.Verdict == VerdictBlock {
		return Result{Block: true, Reason: entry.Reason, Verdict: entry.Verdict}
	}
	if entry.Verdict != VerdictPass {
		c.Warn("question check judge failed, answer passed", "verdict", entry.Verdict, "error", entry.Reason)
	}
	return Result{Verdict: entry.Verdict}
}

// fillTail дописывает в запись uuid сообщения и признак найденной реплики
func (c Checker) fillTail(entry *Entry, transcriptPath string) Tail {
	tail, err := ReadTail(transcriptPath)
	if err != nil {
		c.Warn("question check transcript tail unreadable", "error", err)
		return Tail{}
	}
	entry.MessageID = tail.AssistantUUID
	entry.HumanFound = tail.HumanFound
	return tail
}

// write дописывает строку журнала. Сбой записи — в лог хука: вердикт важнее
func (c Checker) write(entry Entry) {
	if err := appendJSONL(c.JournalPath, entry); err != nil {
		c.Warn("question check journal not written", "path", c.JournalPath, "error", err)
	}
}

func appendJSONL(path string, entry Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create journal dir: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode journal entry: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	// Одна запись write в O_APPEND: строки параллельных сессий не перемешиваются
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("append journal: %w", err)
	}
	return file.Close()
}

// BlockReason — текст, который агент получит вместо конца хода
func BlockReason(verdict Verdict) string {
	var b strings.Builder
	b.WriteString("Проверка вопросов (эксперимент до 30.10): судья счёл вопрос пользователю лишним.\n")
	for _, finding := range verdict.Lishnie {
		fmt.Fprintf(&b, "- «%s»: %s", finding.Quote, finding.Why)
		if finding.Where != "" {
			fmt.Fprintf(&b, " Где искать: %s.", strings.TrimSuffix(finding.Where, "."))
		}
		b.WriteString("\n")
	}
	b.WriteString("Найди ответ сам (журналы, код, тикеты, переписка) или реши в своей зоне и допиши итог. " +
		"Если после поиска вопрос всё же нужен, задай его снова со строкой «Искал: …».")
	return b.String()
}
