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

// Engine центральный процессор хуков
type Engine struct {
	config     *core.Config
	logger     core.Logger
	validators []core.Validator
	tools      []core.ToolValidator
}

// New создает новый процессор хуков
func New(config *core.Config, logger core.Logger) (*Engine, error) {
	engine := &Engine{
		config: config,
		logger: logger.With("component", "engine"),
	}

	// Инициализируем валидаторы
	if err := engine.initValidators(); err != nil {
		return nil, fmt.Errorf("failed to initialize validators: %w", err)
	}

	// Инициализируем инструменты
	if err := engine.initTools(); err != nil {
		return nil, fmt.Errorf("failed to initialize tools: %w", err)
	}

	engine.logger.Info("engine initialized",
		"validators", len(engine.validators),
		"tools", len(engine.tools),
	)

	return engine, nil
}

// ProcessPreToolUse обрабатывает PreToolUse хук
func (e *Engine) ProcessPreToolUse(ctx context.Context, input *core.ToolInput) (*core.HookResponse, error) {
	start := time.Now()
	e.logger.Debug("processing pre-tool-use hook",
		"tool", input.ToolName,
		"file", input.FilePath,
	)

	// Создаем анализ файла если есть файл
	var fileAnalysis *core.FileAnalysis
	if input.FilePath != "" {
		fileAnalysis = core.CreateFileAnalysis(input)
	}

	var allViolations []core.Violation
	var allSuggestions []string

	// Запускаем валидаторы для Write, Edit, MultiEdit операций
	if e.isFileOperation(input.ToolName) && fileAnalysis != nil {
		violations, suggestions, err := e.runValidators(ctx, fileAnalysis)
		if err != nil {
			e.logger.Error("validators execution failed", "error", err)
			return nil, fmt.Errorf("validators failed: %w", err)
		}
		allViolations = append(allViolations, violations...)
		allSuggestions = append(allSuggestions, suggestions...)
	}

	// Запускаем инструментальные валидаторы
	preCtx := context.WithValue(ctx, "hook_phase", "pre")
	modifiedInput, toolViolations, toolSuggestions, err := e.runToolValidators(preCtx, input)
	if err != nil {
		return nil, fmt.Errorf("tool validators failed: %w", err)
	}
	allViolations = append(allViolations, toolViolations...)
	allSuggestions = append(allSuggestions, toolSuggestions...)

	// Определяем финальное действие
	action := e.determineAction(allViolations)
	level := e.determineLevel(allViolations)
	message := e.generateMessage(action, allViolations)

	response := &core.HookResponse{
		Action:            action,
		Message:           message,
		Suggestions:       e.deduplicateSuggestions(allSuggestions),
		Level:             level,
		Violations:        allViolations,
		Timestamp:         time.Now(),
		ProcessTime:       time.Since(start),
		ModifiedToolInput: nil,
	}

	// Если команда была модифицирована, передаем в response
	if modifiedInput != input {
		response.ModifiedToolInput = modifiedInput
	}

	e.logger.Debug("pre-tool-use processing completed",
		"action", action,
		"violations", len(allViolations),
		"duration", time.Since(start),
	)

	return response, nil
}

// ProcessPostToolUse обрабатывает PostToolUse хук
func (e *Engine) ProcessPostToolUse(ctx context.Context, input *core.ToolInput) (*core.HookResponse, error) {
	start := time.Now()
	e.logger.Debug("processing post-tool-use hook",
		"tool", input.ToolName,
		"file", input.FilePath,
	)

	var allViolations []core.Violation
	var allSuggestions []string

	// Запускаем инструментальные валидаторы для post-processing (formatter)
	postCtx := context.WithValue(ctx, "hook_phase", "post")
	_, toolViolations, toolSuggestions, err := e.runToolValidators(postCtx, input)
	if err != nil {
		e.logger.Error("tool validators failed in post-tool-use", "error", err)
	} else {
		allViolations = append(allViolations, toolViolations...)
		allSuggestions = append(allSuggestions, toolSuggestions...)
	}

	action := e.determineAction(allViolations)
	level := e.determineLevel(allViolations)
	message := e.generatePostProcessMessage(action, allViolations, input.ToolName)

	response := &core.HookResponse{
		Action:      action,
		Message:     message,
		Suggestions: e.deduplicateSuggestions(allSuggestions),
		Level:       level,
		Violations:  allViolations,
		Timestamp:   time.Now(),
		ProcessTime: time.Since(start),
	}

	e.logger.Debug("post-tool-use processing completed",
		"action", action,
		"violations", len(allViolations),
		"tool", input.ToolName,
		"duration", time.Since(start),
	)

	return response, nil
}

// ProcessStop обрабатывает Stop хук
func (e *Engine) ProcessStop(ctx context.Context) (*core.HookResponse, error) {
	start := time.Now()
	e.logger.Debug("processing stop hook")

	var allViolations []core.Violation
	var allSuggestions []string

	// Создаем ToolInput для Stop операции
	stopInput := &core.ToolInput{
		ToolName: "Stop",
	}

	// Запускаем инструментальные валидаторы для Stop операций (notifier)
	stopCtx := context.WithValue(ctx, "hook_phase", "stop")
	_, toolViolations, toolSuggestions, err := e.runToolValidators(stopCtx, stopInput)
	if err != nil {
		e.logger.Error("tool validators failed in stop hook", "error", err)
	} else {
		allViolations = append(allViolations, toolViolations...)
		allSuggestions = append(allSuggestions, toolSuggestions...)
	}

	response := &core.HookResponse{
		Action:      core.HookActionAllow,
		Message:     "Stop processing completed",
		Level:       core.LevelInfo,
		Violations:  allViolations,
		Suggestions: e.deduplicateSuggestions(allSuggestions),
		Timestamp:   start,
		ProcessTime: time.Since(start),
	}

	return response, nil
}

// initValidators инициализирует TIER-1 валидаторы
func (e *Engine) initValidators() error {
	// Emergency Defaults Validator
	if config, exists := e.config.Validators["emergency_defaults"]; exists && config.Enabled {
		validator, err := validators.NewEmergencyDefaultsValidator(config, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create emergency defaults validator: %w", err)
		}
		e.validators = append(e.validators, validator)
	}

	// Runtime Exit Validator
	if config, exists := e.config.Validators["runtime_exit"]; exists && config.Enabled {
		validator, err := validators.NewRuntimeExitValidator(config, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create runtime exit validator: %w", err)
		}
		e.validators = append(e.validators, validator)
	}

	// Secrets Validator
	if config, exists := e.config.Validators["secrets"]; exists && config.Enabled {
		validator, err := validators.NewSecretsValidator(config, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create secrets validator: %w", err)
		}
		e.validators = append(e.validators, validator)
	}

	return nil
}

// initTools инициализирует инструментальные валидаторы
func (e *Engine) initTools() error {
	// Notifier Tool для stop hook уведомлений
	if config, exists := e.config.Tools["notifier"]; exists && config.Enabled {
		tool, err := notifier.NewNotifierTool(config, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create notifier tool: %w", err)
		}
		e.tools = append(e.tools, tool)
	}

	// Bash Tool для валидации опасных bash команд
	if config, exists := e.config.Tools["bash"]; exists && config.Enabled {
		tool, err := tools.NewBashTool(config, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create bash tool: %w", err)
		}
		e.tools = append(e.tools, tool)
	}

	// Formatter Tool для автоформатирования
	if config, exists := e.config.Tools["formatter"]; exists && config.Enabled {
		tool, err := tools.NewFormatterTool(config, e.logger)
		if err != nil {
			return fmt.Errorf("failed to create formatter tool: %w", err)
		}
		e.tools = append(e.tools, tool)
	}

	return nil
}

// runValidators запускает все валидаторы
func (e *Engine) runValidators(ctx context.Context, file *core.FileAnalysis) ([]core.Violation, []string, error) {
	var allViolations []core.Violation
	var allSuggestions []string

	for _, validator := range e.validators {
		result, err := validator.Validate(ctx, file)
		if err != nil {
			e.logger.Error("validator failed",
				"validator", validator.Name(),
				"error", err,
			)
			continue
		}

		if !result.IsValid {
			allViolations = append(allViolations, result.Violations...)
			allSuggestions = append(allSuggestions, result.Suggestions...)
		}
	}

	return allViolations, allSuggestions, nil
}

// runToolValidators запускает инструментальные валидаторы
func (e *Engine) runToolValidators(ctx context.Context, input *core.ToolInput) (*core.ToolInput, []core.Violation, []string, error) {
	var allViolations []core.Violation
	var allSuggestions []string
	modifiedInput := input

	for _, tool := range e.tools {
		if !e.toolSupportsOperation(tool, input.ToolName) {
			continue
		}

		result, err := tool.ValidateTool(ctx, modifiedInput)
		if err != nil {
			e.logger.Error("tool validator failed",
				"tool", tool.Name(),
				"error", err,
			)
			continue
		}

		allViolations = append(allViolations, result.Violations...)
		allSuggestions = append(allSuggestions, result.Suggestions...)

		if result.ModifiedToolInput != nil {
			modifiedInput = result.ModifiedToolInput
		}
	}

	return modifiedInput, allViolations, allSuggestions, nil
}

// isFileOperation проверяет является ли операция файловой
func (e *Engine) isFileOperation(toolName string) bool {
	return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit"
}

// toolSupportsOperation проверяет поддерживает ли инструмент операцию
func (e *Engine) toolSupportsOperation(tool core.ToolValidator, toolName string) bool {
	for _, supported := range tool.SupportedTools() {
		if supported == toolName {
			return true
		}
	}
	return false
}

// determineAction определяет финальное действие
func (e *Engine) determineAction(violations []core.Violation) core.HookAction {
	for _, violation := range violations {
		if violation.Severity == core.LevelCritical {
			return core.HookActionBlock
		}
	}
	for _, violation := range violations {
		if violation.Severity == core.LevelWarning {
			return core.HookActionWarn
		}
	}
	return core.HookActionAllow
}

// determineLevel определяет уровень сообщения
func (e *Engine) determineLevel(violations []core.Violation) core.Level {
	for _, violation := range violations {
		if violation.Severity == core.LevelCritical {
			return core.LevelCritical
		}
	}
	for _, violation := range violations {
		if violation.Severity == core.LevelWarning {
			return core.LevelWarning
		}
	}
	return core.LevelInfo
}

// generateMessage генерирует сообщение ответа
func (e *Engine) generateMessage(action core.HookAction, violations []core.Violation) string {
	switch action {
	case core.HookActionBlock:
		if len(violations) > 0 {
			return violations[0].Message
		}
		return "Operation blocked"
	case core.HookActionWarn:
		if len(violations) > 0 {
			return violations[0].Message
		}
		return "Warning"
	default:
		return "Operation allowed"
	}
}

// generatePostProcessMessage генерирует сообщение для post-processing
func (e *Engine) generatePostProcessMessage(action core.HookAction, violations []core.Violation, toolName string) string {
	switch action {
	case core.HookActionBlock:
		return fmt.Sprintf("Post-processing for %s blocked", toolName)
	case core.HookActionWarn:
		return fmt.Sprintf("Post-processing for %s completed with warnings", toolName)
	default:
		return fmt.Sprintf("Post-processing for %s completed", toolName)
	}
}

// deduplicateSuggestions удаляет дублирующиеся предложения
func (e *Engine) deduplicateSuggestions(suggestions []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, s := range suggestions {
		if !seen[s] {
			seen[s] = true
			unique = append(unique, s)
		}
	}
	return unique
}
