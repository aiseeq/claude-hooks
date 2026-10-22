package skillreload

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// Event — событие хука, после которого проверяются файлы сессии
type Event struct {
	SessionID      string
	AgentID        string // пусто — основной разговор
	AgentType      string // "fork" — субагент с копией контекста родителя
	TranscriptPath string // транскрипт основного разговора
}

// Result — итог проверки
type Result struct {
	Notices  []Notice
	NewLoads int  // загрузок скиллов и инструкций в новой части транскрипта
	Pending  bool // транскрипт дочитан не до конца, выдача отложена
	// Warnings — сбои, после которых проверка всё же состоялась: испорченная
	// запись прочитана заново, нечитаемая строка транскрипта пропущена,
	// давние записи не убраны
	Warnings []error
}

// Checker проверяет файлы сессии на событии хука
type Checker struct {
	StateDir string
	// ScanBudget — сколько времени дать чтению транскрипта за одно событие.
	// Первое чтение сессии в сотни МБ растягивается на несколько событий.
	// Ноль — без ограничения
	ScanBudget time.Duration
	Now        func() time.Time
}

// forkAgentType — тип субагента, который наследует контекст родителя вместе
// со снимками скиллов: в его транскрипте этих вызовов нет
const forkAgentType = "fork"

// Check дочитывает транскрипт, запоминает, какие тексты у агента в
// контексте, и возвращает правки, которых агент ещё не видел. Событие без
// транскрипта проверять не по чему — пустой итог
func (c Checker) Check(event Event) (Result, error) {
	transcript := transcriptFor(event)
	if transcript == "" {
		return Result{}, nil
	}
	name, err := stateFileName(event.SessionID, event.AgentID)
	if err != nil {
		return Result{}, err
	}
	locked, err := openState(c.StateDir, name)
	if err != nil {
		return Result{}, err
	}
	defer locked.close()

	var result Result
	if locked.Recovered != nil {
		result.Warnings = append(result.Warnings, locked.Recovered)
	}
	state := &locked.State
	if locked.Fresh {
		result.Warnings = append(result.Warnings, c.prepareFresh(event, state)...)
	}

	scan, err := c.update(state, transcript)
	if err != nil {
		// Прочитанное до сбоя сохраняется: следующее событие продолжит с него
		return Result{}, errors.Join(err, locked.save())
	}
	result.Pending = !scan.Complete
	result.Warnings = append(result.Warnings, scan.Broken...)

	t := tracker{state: state, store: newDirStore(c.StateDir), view: diskView}
	for _, ev := range scan.Events {
		if ev.Kind == evSkillLoad || ev.Kind == evMemoryLoad {
			result.NewLoads++
		}
		if err := t.apply(ev); err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("%s: %w", ev.Path, err))
		}
	}

	// Недочитанный транскрипт может ниже держать новый вызов того же скилла
	// или заметку Claude Code: выдача по старому месту была бы ложной
	if !result.Pending {
		notices, warnings := t.notices(c.now())
		result.Notices = notices
		result.Warnings = append(result.Warnings, warnings...)
	}
	if err := locked.save(); err != nil {
		// Без записи та же правка придёт ещё раз — лишнее лучше
		// пропущенного, поэтому итог отдаётся
		result.Warnings = append(result.Warnings, err)
	}
	return result, nil
}

// prepareFresh готовит запись впервые встреченной сессии: убирает давние
// записи и тексты и засевает форк текстами родителя
func (c Checker) prepareFresh(event Event, state *sessionState) []error {
	var errs []error
	if err := cleanupStale(c.StateDir, c.now()); err != nil {
		errs = append(errs, err)
	}
	if err := cleanupStale(newDirStore(c.StateDir).dir, c.now()); err != nil {
		errs = append(errs, err)
	}
	if event.AgentID == "" || event.AgentType != forkAgentType {
		return errs
	}
	parentName, err := stateFileName(event.SessionID, "")
	if err != nil {
		return append(errs, err)
	}
	parent, err := readStateUnlocked(c.StateDir, parentName)
	if err != nil {
		return append(errs, fmt.Errorf("fork %s inherits no files: %w", event.AgentID, err))
	}
	for path, file := range parent.Files {
		copied := *file
		// Отложенная правка родителя — про его ход, не про форк
		copied.Deferred = ""
		copied.DeferredAt = 0
		state.Files[path] = &copied
	}
	return errs
}

// update дочитывает транскрипт с прошлого места. Транскрипта субагента ещё
// может не быть — тогда читать нечего
func (c Checker) update(state *sessionState, transcript string) (scanResult, error) {
	info, err := os.Stat(transcript)
	if errors.Is(err, fs.ErrNotExist) {
		return scanResult{Offset: state.Offset, Complete: true}, nil
	}
	if err != nil {
		return scanResult{}, fmt.Errorf("cannot stat transcript: %w", err)
	}
	if state.Transcript != transcript || info.Size() < state.Offset {
		// Транскрипт другой или переписан: всё, что о нём знали, недействительно.
		// У новой записи транскрипта ещё нет, а тексты форка взяты у родителя
		if state.Transcript != "" {
			state.Files = map[string]*watched{}
			state.LastAssistant = 0
			state.CompactAt = time.Time{}
		}
		state.Transcript = transcript
		state.Offset = 0
	}

	var deadline time.Time
	if c.ScanBudget > 0 {
		deadline = c.now().Add(c.ScanBudget)
	}
	scan, err := scanTranscript(transcript, state.Offset, deadline, false)
	state.Offset = scan.Offset
	return scan, err
}

// skillNameFromDir — имя скилла без файла: по каталогу
func skillNameFromDir(dir string) string {
	return skillName(dir, nil)
}

// transcriptFor — транскрипт агента события: у субагента свой файл рядом
// с транскриптом основного разговора
func transcriptFor(event Event) string {
	if event.TranscriptPath == "" {
		return ""
	}
	if event.AgentID == "" {
		return event.TranscriptPath
	}
	agent := safeName(event.AgentID)
	if agent == "" {
		return ""
	}
	return strings.TrimSuffix(event.TranscriptPath, ".jsonl") + "/subagents/agent-" + agent + ".jsonl"
}

// Format — текст для контекста агента, по абзацу на файл
func Format(notices []Notice) string {
	parts := make([]string, 0, len(notices))
	for i := range notices {
		parts = append(parts, notices[i].format())
	}
	return strings.Join(parts, "\n\n")
}

func (n *Notice) format() string {
	var subject, reload string
	if n.Kind == kindSkill {
		subject = fmt.Sprintf("Скилл %s изменён после того, как попал тебе в контекст (текст в контексте от %s, файл изменён %s)",
			n.Name, localTime(n.LoadedAt), localTime(n.ChangedAt))
		reload = fmt.Sprintf("Вызови Skill %s заново, прежде чем работать по нему.", n.Name)
	} else {
		subject = fmt.Sprintf("Файл инструкций %s изменён на диске после того, как попал тебе в контекст (текст в контексте от %s, файл изменён %s)",
			n.Path, localTime(n.LoadedAt), localTime(n.ChangedAt))
		reload = fmt.Sprintf("Перечитай его (Read %s), прежде чем работать дальше.", n.Path)
	}
	switch {
	case n.Lost:
		return subject + ": текст, бывший у тебя в контексте, не сохранился, правку не посчитать. " + reload
	case n.Diff == "" && len(n.Full)+len(n.Dropped) > 0:
		return n.formatSections(subject)
	case n.Diff == "":
		sections := "разделы не определены"
		if len(n.Sections) > 0 {
			sections = "изменены разделы: «" + strings.Join(n.Sections, "», «") + "»"
		}
		return fmt.Sprintf("%s: правка большая (diff %.1f КБ), %s. %s", subject, float64(n.DiffBytes)/1024, sections, reload)
	}
	return subject + ". Ниже правка от текста у тебя в контексте; действует сразу, перечитывать не нужно.\n```diff\n" + n.Diff + "```"
}

// formatSections — большая правка разделами целиком: каждый изменённый
// раздел в том виде, в каком он сейчас на диске, удалённый — строкой
func (n *Notice) formatSections(subject string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s. Правка большая (diff %.1f КБ), поэтому ниже изменённые разделы целиком, как они сейчас на диске; они заменяют прежний текст этих разделов у тебя в контексте, остальное не менялось, перечитывать не нужно.",
		subject, float64(n.DiffBytes)/1024)
	for _, s := range n.Full {
		f := fence(s.Text)
		fmt.Fprintf(&out, "\n\nРаздел «%s»:\n%smarkdown\n%s\n%s", s.Title, f, s.Text, f)
	}
	for _, title := range n.Dropped {
		fmt.Fprintf(&out, "\n\nРаздел «%s» удалён.", title)
	}
	return out.String()
}

// localTime — время в поясе машины с его именем
func localTime(t time.Time) string {
	return t.Local().Format("02.01 15:04 MST")
}

func (c Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
