package skillreload

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PreviewResult — что сказал бы установленный хук на ближайшем событии
// каждой недавней сессии: настоящая проверка Checker на копии записей
type PreviewResult struct {
	Since       time.Time
	Transcripts int
	Reminders   map[string][]Reminder // по транскрипту
	Warnings    []error
}

// PreviewInstall прогоняет Checker по транскриптам, изменённым после since,
// с записями во временном каталоге: записи хука не трогаются
func PreviewInstall(root string, since time.Time) (*PreviewResult, error) {
	paths, err := recentTranscripts(root, since)
	if err != nil {
		return nil, err
	}
	stateDir, err := os.MkdirTemp("", "skill-reload-preview-")
	if err != nil {
		return nil, fmt.Errorf("cannot create preview state dir: %w", err)
	}
	defer os.RemoveAll(stateDir)

	checker := Checker{StateDir: stateDir}
	result := &PreviewResult{Since: since, Reminders: map[string][]Reminder{}}
	for _, path := range paths {
		event, ok := eventForTranscript(path)
		if !ok {
			continue
		}
		result.Transcripts++
		check, err := checker.Check(event)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		result.Warnings = append(result.Warnings, check.Warnings...)
		if len(check.Reminders) > 0 {
			result.Reminders[path] = check.Reminders
		}
	}
	return result, nil
}

// eventForTranscript восстанавливает событие хука по пути транскрипта:
// <проект>/<сессия>.jsonl или <проект>/<сессия>/subagents/agent-<id>.jsonl
func eventForTranscript(path string) (Event, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	parent := filepath.Dir(path)
	if filepath.Base(parent) != "subagents" {
		return Event{SessionID: base, TranscriptPath: path}, true
	}
	agent, ok := strings.CutPrefix(base, "agent-")
	if !ok {
		return Event{}, false
	}
	sessionDir := filepath.Dir(parent)
	return Event{
		SessionID:      filepath.Base(sessionDir),
		AgentID:        agent,
		TranscriptPath: sessionDir + ".jsonl",
	}, true
}

// WritePreview печатает итог проверки при установке
func WritePreview(w io.Writer, r *PreviewResult) {
	total := 0
	for _, reminders := range r.Reminders {
		total += len(reminders)
	}
	fmt.Fprintf(w, "При установке (транскрипты с %s: %d): напоминаний %d в %d транскриптах\n",
		r.Since.Local().Format("02.01 15:04 MST"), r.Transcripts, total, len(r.Reminders))
	for _, path := range sortedKeys(r.Reminders) {
		for _, reminder := range r.Reminders[path] {
			fmt.Fprintf(w, "  %s  %s (вызван %s, изменён %s)\n", path, reminder.Name,
				localTime(reminder.LoadedAt), localTime(reminder.ChangedAt))
		}
	}
	fmt.Fprintf(w, "Предупреждений проверки: %d\n", len(r.Warnings))
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "  %v\n", warning)
	}
}
