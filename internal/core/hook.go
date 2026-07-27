package core

import (
	"context"
	"encoding/json"
	"time"
)

// HookAction определяет действие, которое должен выполнить Claude Code
type HookAction string

const (
	HookActionAllow HookAction = "allow"
	HookActionBlock HookAction = "block"
	HookActionWarn  HookAction = "warn"
)

// Level определяет уровень важности сообщения
type Level string

const (
	LevelCritical Level = "critical"
	LevelWarning  Level = "warning"
	LevelInfo     Level = "info"
)

// HookPhase определяет этап, на котором вызван инструмент
type HookPhase string

const (
	PhasePre          HookPhase = "pre"
	PhasePost         HookPhase = "post"
	PhaseStop         HookPhase = "stop"
	PhaseNotification HookPhase = "notification"
)

// Имена событий, под которыми инструменты объявляют поддержку сессионных хуков
const (
	EventStop         = "Stop"
	EventNotification = "Notification"
)

// phaseContextKey приватный тип ключа контекста — исключает коллизии между пакетами
type phaseContextKey struct{}

// WithPhase помещает этап выполнения хука в контекст
func WithPhase(ctx context.Context, phase HookPhase) context.Context {
	return context.WithValue(ctx, phaseContextKey{}, phase)
}

// PhaseFromContext извлекает этап выполнения хука из контекста
func PhaseFromContext(ctx context.Context) HookPhase {
	phase, _ := ctx.Value(phaseContextKey{}).(HookPhase)
	return phase
}

// ToolInput представляет входные данные от Claude Code
type ToolInput struct {
	SessionID      string          `json:"session_id"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	FilePath       string          `json:"file_path,omitempty"`
	Content        string          `json:"content,omitempty"`
	NewString      string          `json:"new_string,omitempty"`
	Command        string          `json:"command,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	// Message заполняется Claude Code для события Notification:
	// текст запроса разрешения или ожидания ответа
	Message string `json:"message,omitempty"`
}

// FileAnalysis содержит анализируемую информацию о файле
type FileAnalysis struct {
	Path       string
	Content    string
	Extension  string
	IsTestFile bool
	IsDocsFile bool
}

// Violation представляет найденное нарушение
type Violation struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
	Severity   Level  `json:"severity"`
}

// HookResponse представляет ответ хука
type HookResponse struct {
	Action      HookAction  `json:"action"`
	Message     string      `json:"message"`
	Suggestions []string    `json:"suggestions,omitempty"`
	Level       Level       `json:"level"`
	Violations  []Violation `json:"violations,omitempty"`
	Timestamp   time.Time   `json:"timestamp"`
	ProcessTime float64     `json:"process_time_ms"`
}

// Validator интерфейс для проверок содержимого файлов
type Validator interface {
	Name() string
	Validate(ctx context.Context, file *FileAnalysis) (*ValidationResult, error)
	IsEnabled() bool
}

// ValidationResult результат валидации
type ValidationResult struct {
	IsValid     bool        `json:"is_valid"`
	Violations  []Violation `json:"violations"`
	Suggestions []string    `json:"suggestions"`
}

// ToolValidator интерфейс для обработки конкретных инструментов Claude Code
type ToolValidator interface {
	Name() string
	ValidateTool(ctx context.Context, input *ToolInput) (*ValidationResult, error)
	IsEnabled() bool
	SupportedTools() []string
}
