package accounts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// Сроки блокировок Claude Code (proper-lockfile): блокировка — каталог, а
// владелец раз в update обновляет его время. Каталог старше stale брошен:
// Claude Code сам снимает такой и берёт заново, так же поступает и ротация
const (
	refreshLockStale = 60 * time.Second
	storageLockStale = 15 * time.Second
	configLockStale  = 10 * time.Second
)

// lockWait — сколько ждать чужую живую блокировку. Claude Code держит их
// доли секунды (обновление токена — до 30 секунд на сетевой запрос).
// Переменная: тест занятой блокировки не ждёт 40 секунд
var lockWait = 40 * time.Second

// lockPoll — шаг ожидания блокировки
const lockPoll = 200 * time.Millisecond

// errLockBusy — блокировку держит живой владелец дольше lockWait
var errLockBusy = errors.New("lock is held by another process")

// dirLock берёт блокировку Claude Code: каталог создаётся атомарно, занятый
// ждётся до lockWait, брошенный (старше stale) снимается. Возвращает снятие.
// Время — настенное: возраст блокировки — время изменения настоящего каталога
func dirLock(path string, stale time.Duration) (func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() {
				// Каталог пуст: proper-lockfile ничего в него не кладёт
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintf(os.Stderr, "claude-hooks: lock %s not released: %v\n", path, err)
				}
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("cannot take lock %s: %w", path, err)
		}
		info, statErr := os.Stat(path)
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			// Владелец снял блокировку между mkdir и stat
			continue
		case statErr != nil:
			return nil, fmt.Errorf("cannot inspect lock %s: %w", path, statErr)
		case time.Since(info.ModTime()) > stale:
			// Брошенная блокировка: владелец не обновлял её дольше срока
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("cannot remove stale lock %s: %w", path, err)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: %w", path, errLockBusy)
		}
		time.Sleep(lockPoll)
	}
}

// claudeLocks берёт по порядку блокировки Claude Code и снимает в обратном.
// Порядок тот же, что у Claude Code при обновлении токена: обновление, вторая
// блокировка обновления, запись файла токенов — так взаимной блокировки нет
func claudeLocks(p Paths) (func(), error) {
	locks := []struct {
		path  string
		stale time.Duration
	}{
		{p.refreshLock(), refreshLockStale},
		{p.secondRefreshLock(), refreshLockStale},
		{p.storageLock(), storageLockStale},
	}
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, lock := range locks {
		release, err := dirLock(lock.path, lock.stale)
		if err != nil {
			releaseAll()
			return nil, err
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
}

// flock берёт исключительный flock файла. wait=false — не ждать: занято,
// значит ротацию уже ведёт другой процесс
func flock(path string, wait bool) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("cannot open lock %s: %w", path, err)
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return func() { f.Close() }, true, nil
}
