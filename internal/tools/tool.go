package tools

import (
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/shared"
)

// BaseTool базовая реализация tool validator
type BaseTool struct {
	name           string
	enabled        bool
	supportedTools []string
	logger         core.Logger
}

// NewBaseTool создает новый базовый tool
func NewBaseTool(name string, enabled bool, supportedTools []string, logger core.Logger) *BaseTool {
	return &BaseTool{
		name:           name,
		enabled:        enabled,
		supportedTools: supportedTools,
		logger:         logger.With("tool", name),
	}
}

// Name возвращает имя tool
func (t *BaseTool) Name() string {
	return t.name
}

// IsEnabled проверяет включен ли tool
func (t *BaseTool) IsEnabled() bool {
	return t.enabled
}

// SupportedTools возвращает список поддерживаемых операций
func (t *BaseTool) SupportedTools() []string {
	return t.supportedTools
}

// Logger возвращает логгер tool'а
func (t *BaseTool) Logger() core.Logger {
	return t.logger
}

// Дублированные функции теперь используются из shared пакета
// Алиасы для обратной совместимости
type PatternMatch = shared.PatternMatch

var CreateViolation = shared.CreateViolation

// FindPatternMatches ищет совпадения с паттернами в тексте
// CANONICAL VERSION - использует shared.FindPatternMatches
func (t *BaseTool) FindPatternMatches(content string, patterns []*regexp.Regexp) []shared.PatternMatch {
	return shared.FindPatternMatches(content, patterns)
}

// isTestOperation проверяет является ли операция тестовой
func isTestOperation(toolName string) bool {
	testOperations := []string{"test", "Test", "TEST"}
	for _, testOp := range testOperations {
		if strings.Contains(toolName, testOp) {
			return true
		}
	}
	return false
}

// isSupportedFile алиас для shared.IsSupportedFileType для обратной совместимости
var isSupportedFile = shared.IsSupportedFileType

// extractCommand извлекает команду из Bash tool input
func extractCommand(input *core.ToolInput) string {
	if input.ToolName != "Bash" {
		return ""
	}
	return input.Command
}

// extractFilePath извлекает путь файла из tool input
func extractFilePath(input *core.ToolInput) string {
	return input.FilePath
}

// extractContent извлекает содержимое файла из tool input
func extractContent(input *core.ToolInput) string {
	if input.Content != "" {
		return input.Content
	}
	return input.NewString
}
