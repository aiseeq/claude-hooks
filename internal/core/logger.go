package core

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Logger интерфейс для структурированного логирования
// CLAUDE.MD NOTE: Supervisor tools используют изолированную slog архитектуру,
// отдельную от основного проекта для независимости инструментов
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
}

// slogLogger обертка вокруг slog.Logger
type slogLogger struct {
	logger *slog.Logger
}

// NewLogger создает новый logger с настройками
func NewLogger(config *LoggerConfig) (Logger, error) {
	if config == nil {
		config = &LoggerConfig{
			Level:   "info",
			Format:  "text",
			Output:  "stderr",
			LogFile: "",
		}
	}

	// Определяем уровень логирования
	var level slog.Level
	switch config.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Определяем выходной поток
	var writer io.Writer
	switch config.Output {
	case "stdout":
		writer = os.Stdout
	case "stderr":
		writer = os.Stderr
	case "file":
		if config.LogFile == "" {
			return nil, fmt.Errorf("log file path is required when output is 'file'")
		}

		// Создаем директорию для лог файла если не существует
		if err := os.MkdirAll(filepath.Dir(config.LogFile), 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}

		file, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		writer = file
	default:
		writer = os.Stderr
	}

	// Создаем handler в зависимости от формата
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Форматируием время в читаемый вид
			if a.Key == slog.TimeKey {
				return slog.Attr{
					Key:   a.Key,
					Value: slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05")),
				}
			}
			return a
		},
	}

	switch config.Format {
	case "json":
		handler = slog.NewJSONHandler(writer, opts)
	case "text":
		handler = slog.NewTextHandler(writer, opts)
	default:
		handler = slog.NewTextHandler(writer, opts)
	}

	logger := slog.New(handler)

	return &slogLogger{logger: logger}, nil
}

// Debug логирует сообщение уровня debug
func (l *slogLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

// Info логирует сообщение уровня info
func (l *slogLogger) Info(msg string, args ...any) {
	l.logger.Info(msg, args...)
}

// Warn логирует сообщение уровня warning
func (l *slogLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}

// Error логирует сообщение уровня error
func (l *slogLogger) Error(msg string, args ...any) {
	l.logger.Error(msg, args...)
}

// With создает новый logger с дополнительными атрибутами
func (l *slogLogger) With(args ...any) Logger {
	return &slogLogger{logger: l.logger.With(args...)}
}

// LoggerConfig конфигурация для логгера
type LoggerConfig struct {
	Level   string `yaml:"level"`  // debug, info, warn, error
	Format  string `yaml:"format"` // text, json
	Output  string `yaml:"output"` // stdout, stderr, file
	LogFile string `yaml:"file"`   // путь к файлу лога (если output = file)
}

// DefaultLoggerConfig возвращает конфигурацию по умолчанию
func DefaultLoggerConfig() *LoggerConfig {
	return &LoggerConfig{
		Level:   "info",
		Format:  "text",
		Output:  "stderr",
		LogFile: "",
	}
}

// ClaudeHooksLoggerConfig возвращает конфигурацию для Claude Hooks
func ClaudeHooksLoggerConfig(logDir string) *LoggerConfig {
	return &LoggerConfig{
		Level:   "info",
		Format:  "text",
		Output:  "file",
		LogFile: filepath.Join(logDir, "claude-hooks.log"),
	}
}

// LogTiming логирует время выполнения операции
func LogTiming(logger Logger, operation string, start time.Time) {
	duration := time.Since(start)
	logger.Debug("operation completed",
		"operation", operation,
		"duration_ms", duration.Milliseconds(),
	)
}

// LogError логирует ошибку с контекстом
func LogError(logger Logger, err error, operation string, context ...any) {
	args := []any{"error", err, "operation", operation}
	args = append(args, context...)
	logger.Error("operation failed", args...)
}
