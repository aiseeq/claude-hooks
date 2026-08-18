package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLogger_WritesToFile(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "logs", "hooks.log")

	logger, err := NewLogger(LoggerConfig{Level: "info", Format: "text", Output: "file", LogFile: logFile})
	if err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
	logger.Info("test message", "key", "value")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("лог-файл не создан: %v", err)
	}
	if !strings.Contains(string(content), "test message") {
		t.Errorf("сообщение не записано: %s", content)
	}
}

// Без ротации лог-файл растет неограниченно: хук вызывается на каждую операцию
func TestNewLogger_RotatesOversizedFile(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "hooks.log")
	oversized := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(logFile, []byte(oversized), 0o644); err != nil {
		t.Fatalf("не удалось создать лог-файл: %v", err)
	}

	logger, err := NewLogger(LoggerConfig{Level: "info", Format: "text", Output: "file", LogFile: logFile, MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
	logger.Info("after rotation")

	if _, err := os.Stat(logFile + ".1"); err != nil {
		t.Errorf("предыдущий лог должен быть сохранен как .1: %v", err)
	}

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("новый лог не создан: %v", err)
	}
	if info.Size() >= int64(len(oversized)) {
		t.Error("после ротации лог должен начинаться заново")
	}
}

func TestNewLogger_KeepsFileWithinLimit(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "hooks.log")
	if err := os.WriteFile(logFile, []byte("existing entry\n"), 0o644); err != nil {
		t.Fatalf("не удалось создать лог-файл: %v", err)
	}

	logger, err := NewLogger(LoggerConfig{Level: "info", Format: "text", Output: "file", LogFile: logFile, MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
	logger.Info("appended entry")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("не удалось прочитать лог: %v", err)
	}
	if !strings.Contains(string(content), "existing entry") {
		t.Error("небольшой лог не должен ротироваться")
	}
}

// stdout занят протоколом обмена с Claude Code
func TestNewLogger_StdoutFallsBackToStderr(t *testing.T) {
	if _, err := NewLogger(LoggerConfig{Level: "info", Format: "text", Output: "stdout"}); err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
}

func TestNewLogger_FileOutputRequiresPath(t *testing.T) {
	if _, err := NewLogger(LoggerConfig{Level: "info", Format: "text", Output: "file"}); err == nil {
		t.Error("output: file без пути должен приводить к ошибке")
	}
}

// testLogger — логгер для тестов пакета: пишет в stderr, который go test
// показывает только при провале
func testLogger(t *testing.T) Logger {
	t.Helper()
	logger, err := NewLogger(LoggerConfig{Level: "debug"})
	if err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
	return logger
}
