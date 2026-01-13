package core

import (
	"bytes"
	"log/slog"
	"sync"
)

// TestLogger - test-friendly logger implementation
type TestLogger struct {
	buffer *bytes.Buffer
	logger *slog.Logger
	mu     sync.Mutex
}

// NewTestLogger creates a new test logger that captures log output
func NewTestLogger() Logger {
	buffer := &bytes.Buffer{}

	// Create handler that writes to buffer
	handler := slog.NewTextHandler(buffer, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	logger := slog.New(handler)

	return &TestLogger{
		buffer: buffer,
		logger: logger,
	}
}

// Debug logs debug level message
func (t *TestLogger) Debug(msg string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := convertArgs(args...)
	t.logger.LogAttrs(nil, slog.LevelDebug, msg, attrs...)
}

// Info logs info level message
func (t *TestLogger) Info(msg string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := convertArgs(args...)
	t.logger.LogAttrs(nil, slog.LevelInfo, msg, attrs...)
}

// Warn logs warning level message
func (t *TestLogger) Warn(msg string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := convertArgs(args...)
	t.logger.LogAttrs(nil, slog.LevelWarn, msg, attrs...)
}

// Error logs error level message
func (t *TestLogger) Error(msg string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := convertArgs(args...)
	t.logger.LogAttrs(nil, slog.LevelError, msg, attrs...)
}

// With creates a new logger with additional context
func (t *TestLogger) With(args ...any) Logger {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Convert to slog.Any format for With method
	slogArgs := make([]any, 0, len(args))
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			slogArgs = append(slogArgs, args[i], args[i+1])
		}
	}

	contextLogger := t.logger.With(slogArgs...)

	return &TestLogger{
		buffer: t.buffer,
		logger: contextLogger,
	}
}

// GetOutput returns all logged output as string
func (t *TestLogger) GetOutput() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buffer.String()
}

// Clear clears the log buffer
func (t *TestLogger) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buffer.Reset()
}

// Helper function to convert interface{} args to slog.Attr
func convertArgs(args ...any) []slog.Attr {
	var attrs []slog.Attr

	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			key, ok := args[i].(string)
			if ok {
				attrs = append(attrs, slog.Any(key, args[i+1]))
			}
		}
	}

	return attrs
}
