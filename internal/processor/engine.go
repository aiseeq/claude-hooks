package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/tools"
	"github.com/aiseeq/claude-hooks/internal/tools/notifier"
	"github.com/aiseeq/claude-hooks/internal/validators"
)

// fileOperations операции Claude Code, изменяющие содержимое файлов
var fileOperations = map[string]bool{
	"Write":     true,
	"Edit":      true,
	"MultiEdit": true,
}

// Engine центральный процессор хуков
type Engine struct {
	logger     core.Logger
	validators []core.Validator
	tools      []core.ToolValidator
}

// New создает процессор хуков по конфигурации
func New(config *core.Config, logger core.Logger) (*Engine, error) {
	engine := &Engine{logger: logger.With("component", "engine")}

	if err := engine.initValidators(config); err != nil {
		return nil, err
	}
	if err := engine.initTools(config); err != nil {
		return nil, err
	}

	engine.logger.Debug("engine initialized",
		"validators", len(engine.validators),
		"tools", len(engine.tools),
	)

	return engine, nil
}

// initValidators инициализирует валидаторы содержимого файлов
func (e *Engine) initValidators(config *core.Config) error {
	constructors := []struct {
		name string
		new  func(core.ValidatorConfig, core.Logger) (core.Validator, error)
	}{
		{"runtime_exit", func(c core.ValidatorConfig, l core.Logger) (core.Validator, error) {
			return validators.NewRuntimeExitValidator(c, l)
		}},
		{"secrets", func(c core.ValidatorConfig, l core.Logger) (core.Validator, error) {
			return validators.NewSecretsValidator(c, l)
		}},
	}

	for _, constructor := range constructors {
		validatorConfig, exists := config.Validators[constructor.name]
		if !exists || !validatorConfig.Enabled {
			continue
		}

		validator, err := constructor.new(validatorConfig, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create %s validator: %w", constructor.name, err)
		}
		e.validators = append(e.validators, validator)
	}

	return nil
}

// initTools инициализирует инструментальные валидаторы
func (e *Engine) initTools(config *core.Config) error {
	constructors := []struct {
		name string
		new  func(core.ToolConfig, core.Logger) (core.ToolValidator, error)
	}{
		{"bash", func(c core.ToolConfig, l core.Logger) (core.ToolValidator, error) {
			return tools.NewBashTool(c, l)
		}},
		{"formatter", func(c core.ToolConfig, l core.Logger) (core.ToolValidator, error) {
			return tools.NewFormatterTool(c, l)
		}},
		{"notifier", func(c core.ToolConfig, l core.Logger) (core.ToolValidator, error) {
			return notifier.NewNotifierTool(c, l)
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
	e.logger.Debug("processing pre-tool-use hook", "tool", input.ToolName, "file", input.FilePath)

	var violations []core.Violation
	var suggestions []string

	if fileOperations[input.ToolName] {
		if file := core.CreateFileAnalysis(input); file != nil {
			fileViolations, fileSuggestions := e.runValidators(ctx, file)
			violations = append(violations, fileViolations...)
			suggestions = append(suggestions, fileSuggestions...)
		}
	}

	toolViolations, toolSuggestions := e.runToolValidators(core.WithPhase(ctx, core.PhasePre), input)
	violations = append(violations, toolViolations...)
	suggestions = append(suggestions, toolSuggestions...)

	return e.buildResponse(violations, suggestions, start), nil
}

// ProcessPostToolUse обрабатывает PostToolUse хук
func (e *Engine) ProcessPostToolUse(ctx context.Context, input *core.ToolInput) (*core.HookResponse, error) {
	start := time.Now()
	e.logger.Debug("processing post-tool-use hook", "tool", input.ToolName, "file", input.FilePath)

	violations, suggestions := e.runToolValidators(core.WithPhase(ctx, core.PhasePost), input)

	return e.buildResponse(violations, suggestions, start), nil
}

// ProcessStop обрабатывает Stop хук — завершение работы Claude Code
func (e *Engine) ProcessStop(ctx context.Context, input *core.ToolInput) (*core.HookResponse, error) {
	return e.processSessionEvent(ctx, input, core.EventStop, core.PhaseStop)
}

// ProcessNotification обрабатывает Notification хук — запрос разрешения
// или ожидание ответа пользователя
func (e *Engine) ProcessNotification(ctx context.Context, input *core.ToolInput) (*core.HookResponse, error) {
	return e.processSessionEvent(ctx, input, core.EventNotification, core.PhaseNotification)
}

// processSessionEvent обрабатывает события сессии: они ничего не проверяют
// и ничего не блокируют, а лишь запускают инструменты вроде уведомлений
func (e *Engine) processSessionEvent(ctx context.Context, input *core.ToolInput, event string, phase core.HookPhase) (*core.HookResponse, error) {
	start := time.Now()
	e.logger.Debug("processing session event", "event", event)

	eventInput := *input
	eventInput.ToolName = event

	_, suggestions := e.runToolValidators(core.WithPhase(ctx, phase), &eventInput)

	return &core.HookResponse{
		Action:      core.HookActionAllow,
		Message:     event + " processing completed",
		Level:       core.LevelInfo,
		Suggestions: deduplicate(suggestions),
		Timestamp:   start,
		ProcessTime: millisecondsSince(start),
	}, nil
}

// runValidators запускает валидаторы содержимого файла
func (e *Engine) runValidators(ctx context.Context, file *core.FileAnalysis) ([]core.Violation, []string) {
	var violations []core.Violation
	var suggestions []string

	for _, validator := range e.validators {
		result, err := validator.Validate(ctx, file)
		if err != nil {
			// Сбой одного валидатора не должен отключать остальные проверки
			e.logger.Error("validator failed", "validator", validator.Name(), "error", err)
			continue
		}
		violations = append(violations, result.Violations...)
		suggestions = append(suggestions, result.Suggestions...)
	}

	return violations, suggestions
}

// runToolValidators запускает инструментальные валидаторы
func (e *Engine) runToolValidators(ctx context.Context, input *core.ToolInput) ([]core.Violation, []string) {
	var violations []core.Violation
	var suggestions []string

	for _, tool := range e.tools {
		if !supportsOperation(tool, input.ToolName) {
			continue
		}

		result, err := tool.ValidateTool(ctx, input)
		if err != nil {
			e.logger.Error("tool validator failed", "tool", tool.Name(), "error", err)
			continue
		}
		violations = append(violations, result.Violations...)
		suggestions = append(suggestions, result.Suggestions...)
	}

	return violations, suggestions
}

// buildResponse собирает ответ хука по найденным нарушениям
func (e *Engine) buildResponse(violations []core.Violation, suggestions []string, start time.Time) *core.HookResponse {
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
		ProcessTime: millisecondsSince(start),
	}
}

// supportsOperation проверяет поддерживает ли инструмент операцию
func supportsOperation(tool core.ToolValidator, toolName string) bool {
	for _, supported := range tool.SupportedTools() {
		if supported == toolName {
			return true
		}
	}
	return false
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

// millisecondsSince возвращает время обработки в миллисекундах
func millisecondsSince(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}
