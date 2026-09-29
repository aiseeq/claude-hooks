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

// Имена событий сессии, на которые отзывается notifier
const (
	EventStop         = "Stop"
	EventNotification = "Notification"
)

// ToolInput представляет входные данные от Claude Code
type ToolInput struct {
	SessionID      string          `json:"session_id"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Command        string          `json:"command,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`

	// Message и NotificationType заполняются для события Notification:
	// текст и тип (permission_prompt, idle_prompt и т.д.)
	Message          string `json:"message,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`

	// BackgroundTasks и SessionCrons приходят в Stop: незавершённые фоновые
	// задачи и будильники сессии. nil означает, что поля не было вовсе
	// (реестр задач недоступен), пустой список — что ждать нечего
	BackgroundTasks *[]BackgroundTask `json:"background_tasks,omitempty"`
	SessionCrons    *[]SessionCron    `json:"session_crons,omitempty"`
}

// BackgroundTask незавершённая фоновая задача сессии: субагент, фоновая
// команда, Monitor, workflow. Описание и команда не читаются: для решения
// хватает идентификатора и типа, а текст задачи в лог попадать не должен
type BackgroundTask struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// SessionCron взведённый будильник сессии: CronCreate, ScheduleWakeup, /loop
type SessionCron struct {
	ID        string `json:"id"`
	Recurring bool   `json:"recurring"`
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

// NewViolation создает нарушение. Единственная точка сборки: новое поле
// добавляется здесь, а не в каждой проверке по отдельности
func NewViolation(violationType, message, suggestion string, severity Level, line, column int) Violation {
	return Violation{
		Type:       violationType,
		Message:    message,
		Suggestion: suggestion,
		Line:       line,
		Column:     column,
		Severity:   severity,
	}
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

// ValidationResult результат проверки
type ValidationResult struct {
	IsValid     bool        `json:"is_valid"`
	Violations  []Violation `json:"violations"`
	Suggestions []string    `json:"suggestions"`
}

// NewValidationResult собирает результат проверки. Годность задаётся явно:
// нарушения-предупреждения операцию не блокируют
func NewValidationResult(isValid bool, violations []Violation, suggestions []string) *ValidationResult {
	return &ValidationResult{
		IsValid:     isValid,
		Violations:  violations,
		Suggestions: suggestions,
	}
}

// ToolValidator интерфейс проверки вызова инструмента Claude Code перед выполнением
type ToolValidator interface {
	Name() string
	ValidateTool(ctx context.Context, input *ToolInput) (*ValidationResult, error)
	IsEnabled() bool
	SupportedTools() []string
}
