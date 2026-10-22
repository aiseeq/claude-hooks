package skillreload

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// textStore хранит тексты по хешу: запись сессии держит только хеши, а diff
// считается от текста, который был у агента в контексте
type textStore interface {
	put(text string) (string, error)
	get(hash string) (string, error)
}

// errTextLost — текста под хешем нет: хранилище почищено или запись старше
var errTextLost = errors.New("text not stored")

// dirStore — тексты файлами в каталоге: переживают перезапуск хука и
// сжатие контекста, общие для всех сессий (один SKILL.md — один файл)
type dirStore struct {
	dir string
}

// textsDirName — подкаталог текстов в каталоге записей
const textsDirName = "texts"

func newDirStore(stateDir string) dirStore {
	return dirStore{dir: filepath.Join(stateDir, textsDirName)}
}

// put сохраняет текст, если его ещё нет, и обновляет время файла: давние
// тексты убирает cleanup по времени
func (s dirStore) put(text string) (string, error) {
	hash := textHash(text)
	path := filepath.Join(s.dir, hash)
	now := time.Now()
	err := os.Chtimes(path, now, now)
	if err == nil {
		return hash, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("cannot touch stored text %s: %w", path, err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create texts dir: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, hash+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("cannot store text: %w", err)
	}
	_, writeErr := tmp.WriteString(text)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("cannot store text: %w", err)
	}
	// Переименование атомарно: параллельный хук видит текст целиком или никак
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("cannot store text: %w", err)
	}
	return hash, nil
}

func (s dirStore) get(hash string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, filepath.Base(hash)))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s: %w", hash, errTextLost)
	}
	if err != nil {
		return "", fmt.Errorf("cannot read stored text %s: %w", hash, err)
	}
	return string(data), nil
}

// memStore — тексты в памяти: сухой прогон
type memStore map[string]string

func (s memStore) put(text string) (string, error) {
	hash := textHash(text)
	s[hash] = text
	return hash, nil
}

func (s memStore) get(hash string) (string, error) {
	text, ok := s[hash]
	if !ok {
		return "", fmt.Errorf("%s: %w", hash, errTextLost)
	}
	return text, nil
}

// textHash — хеш текста в виде, в каком его видит агент
func textHash(text string) string {
	return contentHash([]byte(text))
}
