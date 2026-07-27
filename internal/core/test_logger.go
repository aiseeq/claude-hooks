package core

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
)

// TestLogger логгер для тестов, накапливающий вывод в буфер
type TestLogger struct {
	mu     *sync.Mutex
	buffer *bytes.Buffer
	logger *slog.Logger
}

// NewTestLogger создает логгер, захватывающий вывод в память
func NewTestLogger() *TestLogger {
	buffer := &bytes.Buffer{}
	handler := slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})

	return &TestLogger{
		mu:     &sync.Mutex{},
		buffer: buffer,
		logger: slog.New(handler),
	}
}

// Debug логирует сообщение уровня debug
func (t *TestLogger) Debug(msg string, args ...any) {
	t.log(slog.LevelDebug, msg, args...)
}

// Info логирует сообщение уровня info
func (t *TestLogger) Info(msg string, args ...any) {
	t.log(slog.LevelInfo, msg, args...)
}

// Warn логирует сообщение уровня warning
func (t *TestLogger) Warn(msg string, args ...any) {
	t.log(slog.LevelWarn, msg, args...)
}

// Error логирует сообщение уровня error
func (t *TestLogger) Error(msg string, args ...any) {
	t.log(slog.LevelError, msg, args...)
}

// With создает новый logger с дополнительными атрибутами, разделяющий буфер
func (t *TestLogger) With(args ...any) Logger {
	return &TestLogger{
		mu:     t.mu,
		buffer: t.buffer,
		logger: t.logger.With(args...),
	}
}

// Output возвращает накопленный вывод
func (t *TestLogger) Output() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buffer.String()
}

func (t *TestLogger) log(level slog.Level, msg string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logger.Log(context.Background(), level, msg, args...)
}
