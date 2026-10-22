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

// watched — скилл, вызванный в сессии, и что о нём уже сказано
type watched struct {
	Name     string    `json:"name"`
	LoadedAt time.Time `json:"loaded_at"`
	// Baseline — хеш SKILL.md, текст которого у агента в контексте. Пустой —
	// файл отличался от снимка уже тогда, когда хук увидел вызов
	Baseline string `json:"baseline"`
	// Notified — хеш файла, о котором агенту уже напомнили: одна правка —
	// одно напоминание
	Notified string `json:"notified,omitempty"`
}

// sessionState — что хук знает о сессии (или субагенте): докуда прочитан
// транскрипт и какие скиллы в нём вызваны, по каталогу скилла
type sessionState struct {
	Transcript string              `json:"transcript"`
	Offset     int64               `json:"offset"`
	Skills     map[string]*watched `json:"skills"`
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
		locked.State.Skills = map[string]*watched{}
		return locked, nil
	}
	if err := json.Unmarshal(data, &locked.State); err != nil {
		// Явный failover: сессия читается заново с начала транскрипта, как
		// встреченная впервые; сбой уходит в лог через Recovered
		locked.Fresh = true
		locked.State = sessionState{Skills: map[string]*watched{}}
		locked.Recovered = fmt.Errorf("state %s unreadable, rescanning transcript: %w", path, err)
		return locked, nil
	}
	if locked.State.Skills == nil {
		locked.State.Skills = map[string]*watched{}
	}
	return locked, nil
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
	return state, nil
}

// cleanupStale удаляет записи сессий, не видевших событий дольше stateTTL
func cleanupStale(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("cannot list state dir: %w", err)
	}
	var errs []error
	for _, entry := range entries {
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
