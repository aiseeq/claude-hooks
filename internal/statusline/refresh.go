package statusline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/desktop"
)

// RefreshCommand — скрытая подкоманда, которая обновляет индекс в фоне
const RefreshCommand = "refresh-git-index"

// refreshTimeout ограничивает фоновое обновление: большой репозиторий после
// checkout перечитывается за секунды, дольше — что-то не так
const refreshTimeout = 2 * time.Minute

// refreshCooldown — после обновления индекса новое не запускается столько
// времени. Если статус медленный и на свежем индексе (огромное дерево),
// обновление не поможет, и гонять его на каждую отрисовку незачем
const refreshCooldown = time.Minute

// IndexRefresher запускает фоновое обновление индекса репозитория в dir
type IndexRefresher func(dir string) error

// StartIndexRefresh запускает обновление индекса отдельным процессом и сразу
// возвращает управление. command — собственный бинарь с глобальными флагами
// (--config), к нему добавляется подкоманда RefreshCommand.
//
// Зачем: строка статуса индекс не пишет, а обрываемый по таймауту git status
// не успевает записать его и сам. После checkout или сборки, переписавшей
// тысячи файлов, каждый вызов заново перечитывает их и снова не укладывается
// в таймаут — счётчик изменений пропадает, пока человек сам не запустит git.
// Фоновый git status без таймаута запишет обновлённый индекс один раз, и
// следующие отрисовки снова уложатся в десятки миллисекунд
func StartIndexRefresh(command []string, dir string) error {
	if len(command) == 0 {
		return fmt.Errorf("refresh command is empty")
	}
	lockPath, err := refreshLockPath(dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) < refreshCooldown {
		return nil
	}
	args := append(append([]string{}, command[1:]...), RefreshCommand, "--dir", dir)
	return desktop.StartDetached(exec.Command(command[0], args...))
}

// RefreshIndex обновляет индекс репозитория: git status с необязательной
// блокировкой перечитывает файлы с разошедшимися отметками времени и
// записывает индекс. Пока одно обновление идёт, остальные для того же
// репозитория сразу выходят с refreshed=false
func RefreshIndex(ctx context.Context, dir string) (refreshed bool, err error) {
	lockPath, err := refreshLockPath(dir)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return false, fmt.Errorf("cannot create refresh lock dir: %w", err)
	}

	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, fmt.Errorf("cannot open refresh lock: %w", err)
	}
	defer lock.Close()

	// Блокировка снимается ядром при выходе процесса, даже аварийном
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, fmt.Errorf("cannot lock %s: %w", lockPath, err)
	}

	refreshCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	_, statusErr := gitOutput(refreshCtx, dir, "status", "--porcelain", "--untracked-files=no")

	// Время последнего обновления — отметка для refreshCooldown, даже если
	// git упал: повторять его на каждую отрисовку незачем
	now := time.Now()
	return true, errors.Join(statusErr, os.Chtimes(lockPath, now, now))
}

// refreshLockPath возвращает файл блокировки обновления для репозитория
func refreshLockPath(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", dir, err)
	}
	sum := sha256.Sum256([]byte(absolute))

	return filepath.Join(core.RuntimeDir(), "git-refresh", hex.EncodeToString(sum[:8])+".lock"), nil
}
