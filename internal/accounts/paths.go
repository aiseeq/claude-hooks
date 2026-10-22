// Package accounts хранит токены нескольких подписок Claude и переключает
// Claude Code между ними без нового входа.
//
// Claude Code на Linux держит OAuth в ~/.claude/.credentials.json, а учётку,
// которой принадлежит вход, — в поле oauthAccount файла ~/.claude.json.
// Запущенная сессия перед каждым запросом к API сверяет время изменения файла
// токенов и при изменении перечитывает его (проверено на 2.1.305: сессия
// claude -p после подмены файла посреди работы пошла с новым токеном).
// Поэтому переключение — подмена claudeAiOauth в файле токенов под теми же
// блокировками, под которыми Claude Code обновляет токен, и подмена
// oauthAccount под блокировкой конфигурации.
//
// Токены — секрет: пакет нигде их не печатает и не пишет в лог, хранилище
// лежит вне git с правами 600
package accounts

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths — файлы Claude Code и хранилища. Тесты подставляют свои каталоги
type Paths struct {
	// ClaudeDir — каталог конфигурации Claude Code (~/.claude или CLAUDE_CONFIG_DIR)
	ClaudeDir string
	// GlobalConfig — ~/.claude.json, в нём oauthAccount
	GlobalConfig string
	// StateDir — каталог состояния claude-hooks: хранилище и журнал переключений
	StateDir string
}

// DefaultPaths — пути, которые читает Claude Code: каталог из
// CLAUDE_CONFIG_DIR или ~/.claude, конфигурация рядом с каталогом из
// CLAUDE_CONFIG_DIR или в домашнем каталоге
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("no home directory: %w", err)
	}
	paths := Paths{
		ClaudeDir:    filepath.Join(home, ".claude"),
		GlobalConfig: filepath.Join(home, ".claude.json"),
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		paths.ClaudeDir = dir
		paths.GlobalConfig = filepath.Join(dir, ".claude.json")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	paths.StateDir = filepath.Join(state, "claude-hooks")
	return paths, nil
}

// Credentials — файл токенов Claude Code
func (p Paths) Credentials() string { return filepath.Join(p.ClaudeDir, ".credentials.json") }

// Store — хранилище учёток: токены всех подписок, права 600
func (p Paths) Store() string { return filepath.Join(p.StateDir, "accounts.json") }

// storeLock — flock хранилища: строка статуса и фоновая ротация разных
// сессий работают с хранилищем одновременно
func (p Paths) storeLock() string { return filepath.Join(p.StateDir, ".accounts.lock") }

// Journal — журнал переключений, строка на переключение, без токенов
func (p Paths) Journal() string { return filepath.Join(p.StateDir, "account-switches.jsonl") }

// spawnMark — отметка последнего запуска фоновой ротации: строка статуса
// рисуется часто, а ротация запускается не чаще раза в spawnInterval
func (p Paths) spawnMark() string { return filepath.Join(p.StateDir, ".rotate-spawn") }

// refreshLock — блокировка, под которой Claude Code обновляет токен
// (proper-lockfile, каталог .oauth_refresh.lock в каталоге конфигурации)
func (p Paths) refreshLock() string { return filepath.Join(p.ClaudeDir, ".oauth_refresh.lock") }

// secondRefreshLock — вторая блокировка обновления токена: Claude Code берёт
// её вслед за первой, путь — настоящий путь каталога конфигурации и .lock
func (p Paths) secondRefreshLock() string {
	dir := p.ClaudeDir
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir + ".lock"
}

// storageLock — блокировка записи файла токенов (secureStorage, .storage-write)
func (p Paths) storageLock() string { return filepath.Join(p.ClaudeDir, ".storage-write.lock") }

// configLock — блокировка записи ~/.claude.json
func (p Paths) configLock() string { return p.GlobalConfig + ".lock" }
