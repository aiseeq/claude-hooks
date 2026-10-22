package skillreload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// watched — скилл или файл инструкций в контексте сессии и что о нём уже
// сказано
type watched struct {
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Path     string    `json:"path"`          // файл на диске
	Dir      string    `json:"dir,omitempty"` // каталог скилла
	LoadedAt time.Time `json:"loaded_at"`
	// Base — хеш текста, который сейчас у агента в контексте; сам текст —
	// в хранилище текстов. От него считается следующий diff
	Base string `json:"base"`
	// Snapshot и LoadBase — хеш снимка последнего вызова скилла и база,
	// выбранная при вызове: сжатие подкладывает тот же снимок, и база
	// возвращается к нему
	Snapshot string `json:"snapshot,omitempty"`
	LoadBase string `json:"load_base,omitempty"`
	// Native — Claude Code сам следит за файлом и покажет агенту ближайшую
	// правку заметкой «changed on disk»: хук ждёт её, а не пишет второй раз
	Native bool `json:"native,omitempty"`
	// Deferred — хеш текста, правку к которому хук оставил Claude Code, и
	// место транскрипта в тот момент. Нет заметки после ответа модели —
	// Claude Code правку пропустил, и её доносит хук
	Deferred   string `json:"deferred,omitempty"`
	DeferredAt int64  `json:"deferred_at,omitempty"`
}

// Виды отслеживаемых файлов
const (
	kindSkill  = "skill"
	kindMemory = "memory"
)

// stateSchema — версия записи. Запись прежнего вида (только хеши скиллов)
// перечитывается с начала транскрипта: тексты в контексте берутся из снимков
const stateSchema = 2

// sessionState — что хук знает о сессии (или субагенте): докуда прочитан
// транскрипт и какие файлы у агента в контексте, по пути файла
type sessionState struct {
	Schema     int                 `json:"schema"`
	Transcript string              `json:"transcript"`
	Offset     int64               `json:"offset"`
	Files      map[string]*watched `json:"files"`
	// LastAssistant — место последнего ответа модели в транскрипте: после
	// него Claude Code уже показал бы свою заметку
	LastAssistant int64 `json:"last_assistant,omitempty"`
	// CompactAt — время последнего сжатия: список инструкций сразу после
	// него Claude Code не отслеживает
	CompactAt time.Time `json:"compact_at,omitempty"`
	// Announced — хеши SKILL.md, о которых напомнил хук схемы 1, по
	// каталогу скилла: та правка уже объявлена, второй раз её не показывать
	Announced map[string]string `json:"announced,omitempty"`
	// Skills — записи прежней версии, читаются только при переходе
	Skills map[string]schema1Skill `json:"skills,omitempty"`
}

// schema1Skill — запись скилла в схеме 1
type schema1Skill struct {
	Notified string `json:"notified,omitempty"`
}

// newSessionState — пустая запись
func newSessionState() sessionState {
	return sessionState{Schema: stateSchema, Files: map[string]*watched{}}
}

// stateTTL — сколько хранить запись сессии без событий. Сессии живут днями
// и продолжаются через --resume; запись в несколько килобайт не мешает
const stateTTL = 30 * 24 * time.Hour

// lockWait — сколько ждать блокировку записи: UserPromptSubmit и
// PostToolBatch срабатывают почти одновременно, и без неё напоминание ушло бы
// дважды. Оба держат её миллисекунды
const lockWait = time.Second

// lockPoll — шаг ожидания блокировки
const lockPoll = 10 * time.Millisecond

// DefaultStateDir — каталог записей: $XDG_STATE_HOME/claude-hooks/skill-reload.
// Не каталог времени выполнения: сессию продолжают после перезагрузки
func DefaultStateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-hooks", "skill-reload"), nil
}

// stateFileName — имя записи сессии или её субагента
func stateFileName(sessionID, agentID string) (string, error) {
	session := safeName(sessionID)
	if session == "" {
		return "", fmt.Errorf("invalid session id %q: %w", sessionID, os.ErrInvalid)
	}
	if agentID == "" {
		return session + ".json", nil
	}
	agent := safeName(agentID)
	if agent == "" {
		return "", fmt.Errorf("invalid agent id %q: %w", agentID, os.ErrInvalid)
	}
	return session + "." + agent + ".json", nil
}

// safeName не пускает в имя файла разделители пути: идентификатор пришёл извне
func safeName(id string) string {
	name := filepath.Base(strings.TrimSpace(id))
	if name == "." || name == string(filepath.Separator) || strings.ContainsAny(name, "/\\") {
		return ""
	}
	return name
}

// lockedState — открытая под блокировкой запись сессии
type lockedState struct {
	file  *os.File
	State sessionState
	Fresh bool // записи не было: сессия встречена впервые
	// Recovered — запись была испорчена (оборванная запись) и заменена
	// пустой: транскрипт читается заново с начала
	Recovered error
}

// openState открывает запись сессии под исключительной блокировкой
func openState(dir, name string) (*lockedState, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create state dir: %w", err)
	}
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open state %s: %w", path, err)
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, err
	}

	data, err := io.ReadAll(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot read state %s: %w", path, err)
	}
	locked := &lockedState{file: file}
	if len(data) == 0 {
		locked.Fresh = true
		locked.State = newSessionState()
		return locked, nil
	}
	if err := json.Unmarshal(data, &locked.State); err != nil {
		// Явный failover: сессия читается заново с начала транскрипта, как
		// встреченная впервые; сбой уходит в лог через Recovered
		locked.Fresh = true
		locked.State = newSessionState()
		locked.Recovered = fmt.Errorf("state %s unreadable, rescanning transcript: %w", path, err)
		return locked, nil
	}
	if locked.State.Schema != stateSchema {
		locked.State = migrate(locked.State)
	}
	if locked.State.Files == nil {
		locked.State.Files = map[string]*watched{}
	}
	return locked, nil
}

// migrate переводит запись прежней версии: транскрипт читается заново с
// начала, а правки, о которых прежняя версия уже напомнила, запоминаются,
// чтобы не объявить их второй раз
func migrate(old sessionState) sessionState {
	state := newSessionState()
	for dir, skill := range old.Skills {
		if skill.Notified == "" {
			continue
		}
		if state.Announced == nil {
			state.Announced = map[string]string{}
		}
		state.Announced[filepath.Join(dir, skillFileName)] = skill.Notified
	}
	return state
}

// lockFile ждёт блокировку не дольше lockWait
func lockFile(file *os.File) error {
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("cannot lock state %s: %w", file.Name(), err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("state %s is locked by another hook", file.Name())
		}
		time.Sleep(lockPoll)
	}
}

// save записывает запись на место старой, не снимая блокировку
func (l *lockedState) save() error {
	data, err := json.Marshal(l.State)
	if err != nil {
		return fmt.Errorf("cannot encode state: %w", err)
	}
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("cannot truncate state %s: %w", l.file.Name(), err)
	}
	if _, err := l.file.WriteAt(data, 0); err != nil {
		return fmt.Errorf("cannot write state %s: %w", l.file.Name(), err)
	}
	return nil
}

// close снимает блокировку
func (l *lockedState) close() {
	l.file.Close()
}

// readStateUnlocked читает чужую запись без блокировки — для засева форка.
// Нет записи — пустое состояние
func readStateUnlocked(dir, name string) (sessionState, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return sessionState{}, nil
	}
	if err != nil {
		return sessionState{}, fmt.Errorf("cannot read state: %w", err)
	}
	var state sessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return sessionState{}, fmt.Errorf("cannot decode state %s: %w", name, err)
	}
	if state.Schema != stateSchema {
		return sessionState{}, fmt.Errorf("state %s has schema %d, want %d", name, state.Schema, stateSchema)
	}
	return state, nil
}

// cleanupStale удаляет файлы каталога (записи сессий, тексты), не тронутые
// дольше stateTTL
func cleanupStale(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot list state dir: %w", err)
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if now.Sub(info.ModTime()) < stateTTL {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
