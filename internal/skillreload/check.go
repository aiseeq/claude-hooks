package skillreload

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

// Event — событие хука, после которого проверяются скиллы сессии
type Event struct {
	SessionID      string
	AgentID        string // пусто — основной разговор
	AgentType      string // "fork" — субагент с копией контекста родителя
	TranscriptPath string // транскрипт основного разговора
}

// Reminder — скилл, текст которого в контексте устарел
type Reminder struct {
	Name      string
	Path      string
	LoadedAt  time.Time
	ChangedAt time.Time
}

// Result — итог проверки
type Result struct {
	Reminders []Reminder
	NewLoads  int  // вызовов скиллов найдено в новой части транскрипта
	Pending   bool // транскрипт дочитан не до конца, напоминания отложены
	// Warnings — сбои, после которых проверка всё же состоялась: испорченная
	// запись прочитана заново, нечитаемая строка транскрипта пропущена,
	// давние записи не убраны
	Warnings []error
}

// Checker проверяет скиллы сессии на событии хука
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

// Check дочитывает транскрипт, запоминает новые вызовы скиллов и возвращает
// скиллы, изменённые после вызова, о которых агенту ещё не говорили.
// Событие без транскрипта проверять не по чему — пустой итог
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
	result.NewLoads = len(scan.Loads)
	result.Pending = !scan.Complete
	result.Warnings = append(result.Warnings, scan.Broken...)
	for _, load := range scan.Loads {
		skill, err := observe(load)
		if err != nil {
			result.Warnings = append(result.Warnings, err)
		}
		state.Skills[load.Dir] = skill
	}

	// Недочитанный транскрипт может ниже держать новый вызов того же скилла:
	// напоминание по старому вызову было бы ложным
	if !result.Pending {
		reminders, warnings := remind(state)
		result.Reminders = reminders
		result.Warnings = append(result.Warnings, warnings...)
	}
	if err := locked.save(); err != nil {
		// Без записи то же напоминание придёт ещё раз — лишнее лучше
		// пропущенного, поэтому итог отдаётся
		result.Warnings = append(result.Warnings, err)
	}
	return result, nil
}

// prepareFresh готовит запись впервые встреченной сессии: убирает давние
// записи и засевает форк снимками родителя
func (c Checker) prepareFresh(event Event, state *sessionState) []error {
	var errs []error
	if err := cleanupStale(c.StateDir, c.now()); err != nil {
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
		return append(errs, fmt.Errorf("fork %s inherits no skills: %w", event.AgentID, err))
	}
	for dir, skill := range parent.Skills {
		copied := *skill
		// О старом тексте форку ещё не говорили, даже если родителю сказали
		copied.Notified = ""
		state.Skills[dir] = &copied
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
		// У новой записи транскрипта ещё нет, а снимки форка взяты у родителя
		if state.Transcript != "" {
			state.Skills = map[string]*watched{}
		}
		state.Transcript = transcript
		state.Offset = 0
	}

	var deadline time.Time
	if c.ScanBudget > 0 {
		deadline = c.now().Add(c.ScanBudget)
	}
	scan, err := scanTranscript(transcript, state.Offset, deadline)
	state.Offset = scan.Offset
	return scan, err
}

// observe запоминает вызов скилла. Файл на диске соответствует снимку, если
// тело совпало или файл не трогали после вызова; иначе файл изменили между
// вызовом и тем, как хук его увидел (первое чтение старой сессии) — тогда
// Baseline пуст и напоминание придёт сразу
func observe(load Load) (*watched, error) {
	skill := &watched{Name: skillNameFromDir(load.Dir), LoadedAt: load.At}
	file, err := readSkillFile(load.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		// Скилла больше нет (временный каталог, удалённый скилл): remind
		// пропускает отсутствующий файл, вызывать заново нечего
		return skill, nil
	}
	if err != nil {
		return skill, fmt.Errorf("skill %s not compared with its snapshot: %w", skill.Name, err)
	}
	skill.Name = skillName(load.Dir, file.Raw)
	if renderBody(file.Raw, load.Dir) == load.Snapshot || !file.ModTime.After(load.At) {
		skill.Baseline = contentHash(file.Raw)
	}
	return skill, nil
}

// skillNameFromDir — имя скилла без файла: по каталогу
func skillNameFromDir(dir string) string {
	return skillName(dir, nil)
}

// remind собирает скиллы, файл которых отличается от текста в контексте и о
// которых ещё не напоминали, и отмечает их. Отсутствующий файл пропускается:
// вызвать заново нечего
func remind(state *sessionState) ([]Reminder, []error) {
	var reminders []Reminder
	var errs []error
	for _, dir := range sortedKeys(state.Skills) {
		skill := state.Skills[dir]
		file, err := readSkillFile(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("skill %s not checked: %w", skill.Name, err))
			continue
		}
		hash := contentHash(file.Raw)
		if hash == skill.Baseline || hash == skill.Notified {
			continue
		}
		skill.Notified = hash
		reminders = append(reminders, Reminder{
			Name:      skill.Name,
			Path:      file.Path,
			LoadedAt:  skill.LoadedAt,
			ChangedAt: file.ModTime,
		})
	}
	sort.Slice(reminders, func(i, j int) bool { return reminders[i].Name < reminders[j].Name })
	return reminders, errs
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

// Format — текст напоминания агенту, по строке на скилл
func Format(reminders []Reminder) string {
	lines := make([]string, 0, len(reminders))
	for _, r := range reminders {
		lines = append(lines, fmt.Sprintf(
			"Скилл %s изменён после твоего вызова (вызван %s, изменён %s): в контексте старый текст. Вызови Skill %s заново, прежде чем работать по нему.",
			r.Name, localTime(r.LoadedAt), localTime(r.ChangedAt), r.Name))
	}
	return strings.Join(lines, "\n")
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
