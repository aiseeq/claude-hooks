package tools

import (
	"github.com/aiseeq/claude-hooks/internal/core"
)

// BaseTool общая часть всех инструментальных валидаторов
type BaseTool struct {
	name           string
	enabled        bool
	supportedTools []string
	logger         core.Logger
}

// NewBaseTool создает базовый инструмент
func NewBaseTool(name string, enabled bool, supportedTools []string, logger core.Logger) *BaseTool {
	return &BaseTool{
		name:           name,
		enabled:        enabled,
		supportedTools: supportedTools,
		logger:         logger.With("tool", name),
	}
}

// Name возвращает имя инструмента
func (t *BaseTool) Name() string {
	return t.name
}

// IsEnabled проверяет включен ли инструмент
func (t *BaseTool) IsEnabled() bool {
	return t.enabled
}

// SupportedTools возвращает список поддерживаемых операций Claude Code
func (t *BaseTool) SupportedTools() []string {
	return t.supportedTools
}

// Logger возвращает логгер инструмента
func (t *BaseTool) Logger() core.Logger {
	return t.logger
}
