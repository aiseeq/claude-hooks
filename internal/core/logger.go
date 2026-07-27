package core

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// defaultMaxLogSizeMB размер лога, после которого выполняется ротация.
// Без него файл растет неограниченно: хук вызывается на каждую операцию Claude Code
const defaultMaxLogSizeMB = 16

// LoggerConfig конфигурация логгера
type LoggerConfig struct {
	Level     string `yaml:"level"`       // debug, info, warn, error
	Format    string `yaml:"format"`      // text, json
	Output    string `yaml:"output"`      // stdout, stderr, file
	LogFile   string `yaml:"file"`        // путь к файлу лога (если output = file)
	MaxSizeMB int    `yaml:"max_size_mb"` // порог ротации, 0 — отключить ротацию
}

// DefaultLoggerConfig возвращает конфигурацию логгера по умолчанию
func DefaultLoggerConfig() LoggerConfig {
	return LoggerConfig{
		Level:     "info",
		Format:    "text",
		Output:    "stderr",
		MaxSizeMB: defaultMaxLogSizeMB,
	}
}

// applyLoggerDefaults подставляет значения по умолчанию для незаполненных полей
func applyLoggerDefaults(config *LoggerConfig) {
	defaults := DefaultLoggerConfig()
	if config.Level == "" {
		config.Level = defaults.Level
	}
	if config.Format == "" {
		config.Format = defaults.Format
	}
	if config.Output == "" {
		config.Output = defaults.Output
	}
	if config.MaxSizeMB == 0 {
		config.MaxSizeMB = defaults.MaxSizeMB
	}
}

// Logger интерфейс для структурированного логирования
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

// NewLogger создает logger по конфигурации
func NewLogger(config LoggerConfig) (Logger, error) {
	applyLoggerDefaults(&config)

	writer, err := openLogWriter(config)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{
		Level: parseLogLevel(config.Level),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05"))
			}
			return a
		},
	}

	var handler slog.Handler
	if config.Format == "json" {
		handler = slog.NewJSONHandler(writer, opts)
	} else {
		handler = slog.NewTextHandler(writer, opts)
	}

	return &slogLogger{logger: slog.New(handler)}, nil
}

// parseLogLevel преобразует текстовый уровень в slog.Level
func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// openLogWriter открывает поток вывода логов
func openLogWriter(config LoggerConfig) (io.Writer, error) {
	switch config.Output {
	case "stdout":
		// stdout зарезервирован под протокол обмена с Claude Code
		return os.Stderr, nil
	case "file":
		if config.LogFile == "" {
			return nil, fmt.Errorf("log file path is required when output is 'file'")
		}
		if err := os.MkdirAll(filepath.Dir(config.LogFile), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}
		rotateLogIfNeeded(config.LogFile, config.MaxSizeMB)

		file, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		return file, nil
	default:
		return os.Stderr, nil
	}
}

// rotateLogIfNeeded переименовывает лог в *.1 при превышении порога размера.
// Ошибки ротации игнорируются намеренно: логирование не должно ломать работу хука
func rotateLogIfNeeded(logFile string, maxSizeMB int) {
	if maxSizeMB <= 0 {
		return
	}

	info, err := os.Stat(logFile)
	if err != nil || info.Size() < int64(maxSizeMB)*1024*1024 {
		return
	}

	_ = os.Rename(logFile, logFile+".1")
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
