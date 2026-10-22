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
	Notices     map[string][]Notice // по транскрипту
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
	result := &PreviewResult{Since: since, Notices: map[string][]Notice{}}
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
		if len(check.Notices) > 0 {
			result.Notices[path] = check.Notices
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
	for _, notices := range r.Notices {
		total += len(notices)
	}
	fmt.Fprintf(w, "При установке (транскрипты с %s: %d): выдач %d в %d транскриптах\n",
		r.Since.Local().Format("02.01 15:04 MST"), r.Transcripts, total, len(r.Notices))
	for _, path := range sortedKeys(r.Notices) {
		for i := range r.Notices[path] {
			n := &r.Notices[path][i]
			form := fmt.Sprintf("diff %d Б", n.DiffBytes)
			switch {
			case n.Overflow:
				form = fmt.Sprintf("не поместилась, перечнем, diff %d Б", n.DiffBytes)
			case n.Lost:
				form = "текст потерян"
			case n.Diff == "" && len(n.Full)+len(n.Dropped) > 0:
				form = fmt.Sprintf("разделы целиком: %d, удалено %d, diff %d Б", len(n.Full), len(n.Dropped), n.DiffBytes)
			case n.Diff == "":
				form = fmt.Sprintf("перечитать, diff %d Б", n.DiffBytes)
			}
			fmt.Fprintf(w, "  %s  %s (%s, изменён %s)\n", path, n.label(), form, localTime(n.ChangedAt))
		}
	}
	fmt.Fprintf(w, "Предупреждений проверки: %d\n", len(r.Warnings))
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "  %v\n", warning)
	}
}
