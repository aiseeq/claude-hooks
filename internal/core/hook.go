package core

import (
	"context"
	"encoding/json"
	"errors"
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
	LevelError    Level = "error"
	LevelWarning  Level = "warning"
	LevelInfo     Level = "info"
)

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
	Action            HookAction    `json:"action"`
	Message           string        `json:"message"`
	Suggestions       []string      `json:"suggestions,omitempty"`
	Level             Level         `json:"level"`
	Violations        []Violation   `json:"violations,omitempty"`
	Timestamp         time.Time     `json:"timestamp"`
	ProcessTime       time.Duration `json:"process_time_ms"`
	ModifiedToolInput *ToolInput    `json:"modified_tool_input,omitempty"` // Модифицированные параметры для Claude Code
}

// HookProcessor основной интерфейс для обработки хуков
type HookProcessor interface {
	ProcessPreToolUse(ctx context.Context, input *ToolInput) (*HookResponse, error)
	ProcessPostToolUse(ctx context.Context, input *ToolInput) (*HookResponse, error)
	ProcessStop(ctx context.Context) (*HookResponse, error)
}

// Validator интерфейс для TIER-1 критических проверок
type Validator interface {
	Name() string
	Validate(ctx context.Context, file *FileAnalysis) (*ValidationResult, error)
	IsEnabled() bool
	GetExceptions() []string
}

// ValidationResult результат валидации
type ValidationResult struct {
	IsValid           bool        `json:"is_valid"`
	Violations        []Violation `json:"violations"`
	Suggestions       []string    `json:"suggestions"`
	ModifiedToolInput *ToolInput  `json:"modified_tool_input,omitempty"` // Модифицированные параметры инструмента
}

// Advisor интерфейс для TIER-2 стилевых советов
type Advisor interface {
	Name() string
	Advise(ctx context.Context, file *FileAnalysis) (*AdviceResult, error)
	IsEnabled() bool
	GetSeverity() Level
}

// AdviceResult результат получения совета
type AdviceResult struct {
	Advices     []Violation `json:"advices"`
	Suggestions []string    `json:"suggestions"`
}

// ToolValidator интерфейс для валидации специфических инструментов
type ToolValidator interface {
	Name() string
	ValidateTool(ctx context.Context, input *ToolInput) (*ValidationResult, error)
	IsEnabled() bool
	SupportedTools() []string
}

// ErrUnsupportedTool ошибка неподдерживаемого инструмента
var ErrUnsupportedTool = errors.New("unsupported tool operation")
