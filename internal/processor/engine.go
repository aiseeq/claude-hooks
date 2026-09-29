package processor

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/tools"
)

// Engine прогоняет вызов инструмента через проверки перед выполнением
type Engine struct {
	logger core.Logger
	tools  []core.ToolValidator
}

// New создает процессор хуков по конфигурации
func New(config *core.Config, logger core.Logger) (*Engine, error) {
	engine := &Engine{logger: logger.With("component", "engine")}

	if err := engine.initTools(config); err != nil {
		return nil, err
	}

	engine.logger.Debug("engine initialized", "tools", len(engine.tools))
	return engine, nil
}

// initTools инициализирует проверки вызовов инструментов
func (e *Engine) initTools(config *core.Config) error {
	constructors := []struct {
		name string
		new  func(core.ToolConfig, core.Logger) (core.ToolValidator, error)
	}{
		{"jira_style", func(c core.ToolConfig, l core.Logger) (core.ToolValidator, error) {
			return tools.NewJiraStyleTool(c, l)
		}},
	}

	for _, constructor := range constructors {
		toolConfig, exists := config.Tools[constructor.name]
		if !exists || !toolConfig.Enabled {
			continue
		}

		tool, err := constructor.new(toolConfig, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create %s tool: %w", constructor.name, err)
		}
		e.tools = append(e.tools, tool)
	}

	return nil
}

// ProcessPreToolUse обрабатывает PreToolUse хук
func (e *Engine) ProcessPreToolUse(ctx context.Context, input *core.ToolInput) (*core.HookResponse, error) {
	start := time.Now()
	e.logger.Debug("processing pre-tool-use hook", "tool", input.ToolName)

	var violations []core.Violation
	var suggestions []string

	for _, tool := range e.tools {
		if !slices.Contains(tool.SupportedTools(), input.ToolName) {
			continue
		}

		result, err := tool.ValidateTool(ctx, input)
		if err != nil {
			// Сбой одной проверки не должен отключать остальные
			e.logger.Error("tool validator failed", "tool", tool.Name(), "error", err)
			continue
		}
		violations = append(violations, result.Violations...)
		suggestions = append(suggestions, result.Suggestions...)
	}

	return buildResponse(violations, suggestions, start), nil
}

// buildResponse собирает ответ хука по найденным нарушениям
func buildResponse(violations []core.Violation, suggestions []string, start time.Time) *core.HookResponse {
	level := highestLevel(violations)

	action := core.HookActionAllow
	switch level {
	case core.LevelCritical:
		action = core.HookActionBlock
	case core.LevelWarning:
		action = core.HookActionWarn
	}

	return &core.HookResponse{
		Action:      action,
		Message:     buildMessage(action, violations),
		Suggestions: deduplicate(suggestions),
		Level:       level,
		Violations:  violations,
		Timestamp:   start,
		ProcessTime: float64(time.Since(start).Microseconds()) / 1000,
	}
}

// highestLevel возвращает максимальный уровень серьезности среди нарушений
func highestLevel(violations []core.Violation) core.Level {
	level := core.LevelInfo
	for _, violation := range violations {
		if violation.Severity == core.LevelCritical {
			return core.LevelCritical
		}
		if violation.Severity == core.LevelWarning {
			level = core.LevelWarning
		}
	}
	return level
}

// buildMessage формирует сообщение ответа из нарушений
func buildMessage(action core.HookAction, violations []core.Violation) string {
	if action == core.HookActionAllow {
		return "Operation allowed"
	}

	for _, violation := range violations {
		if violation.Severity == core.LevelCritical || action == core.HookActionWarn {
			if violation.Line > 0 {
				return fmt.Sprintf("%s (строка %d)", violation.Message, violation.Line)
			}
			return violation.Message
		}
	}

	return "Operation blocked"
}

// deduplicate удаляет дублирующиеся предложения, сохраняя порядок
func deduplicate(suggestions []string) []string {
	seen := make(map[string]bool, len(suggestions))
	unique := make([]string, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if !seen[suggestion] {
			seen[suggestion] = true
			unique = append(unique, suggestion)
		}
	}
	return unique
}
