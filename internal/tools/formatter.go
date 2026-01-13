package tools

import (
	"context"
	"os/exec"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// FormatterTool автоматическое форматирование кода
type FormatterTool struct {
	*BaseTool
	goFormat bool
	tsFormat bool
}

// NewFormatterTool создает новый formatter tool
func NewFormatterTool(config core.ToolConfig, logger core.Logger) (*FormatterTool, error) {
	// Formatter работает в PostToolUse хуке
	supportedTools := []string{"Write", "Edit", "MultiEdit"}
	base := NewBaseTool("formatter", config.Enabled, supportedTools, logger)

	tool := &FormatterTool{
		BaseTool: base,
		goFormat: config.GoFormat,
		tsFormat: config.TSFormat,
	}

	return tool, nil
}

// ValidateTool выполняет форматирование файлов
func (t *FormatterTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Formatter only runs in post-tool-use phase (after file is written)
	phase, _ := ctx.Value("hook_phase").(string)
	if phase != "post" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	var violations []core.Violation
	var suggestions []string

	filePath := extractFilePath(input)
	if filePath == "" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	t.logger.Debug("formatting file", "file", filePath)

	// Форматируем Go файлы
	if t.goFormat && t.isGoFile(filePath) {
		if formatted, err := t.formatGoFile(ctx, filePath); err != nil {
			t.logger.Warn("failed to format Go file", "file", filePath, "error", err)
			violations = append(violations, core.Violation{
				Type:       "format_error",
				Message:    "Ошибка форматирования Go файла: " + err.Error(),
				Suggestion: "Проверь синтаксис Go кода",
				Severity:   core.LevelWarning,
			})
		} else if formatted {
			suggestions = append(suggestions, "Go файл автоматически отформатирован")
		}
	}

	// Форматируем TypeScript файлы
	if t.tsFormat && t.isTSFile(filePath) {
		if formatted, err := t.formatTSFile(ctx, filePath); err != nil {
			t.logger.Warn("failed to format TS file", "file", filePath, "error", err)
			violations = append(violations, core.Violation{
				Type:       "format_error",
				Message:    "Ошибка форматирования TS файла: " + err.Error(),
				Suggestion: "Проверь синтаксис TypeScript кода",
				Severity:   core.LevelWarning,
			})
		} else if formatted {
			suggestions = append(suggestions, "TypeScript файл автоматически отформатирован")
		}
	}

	return &core.ValidationResult{
		IsValid:     true, // Formatter не блокирует операции
		Violations:  violations,
		Suggestions: suggestions,
	}, nil
}

// isGoFile проверяет является ли файл Go файлом
func (t *FormatterTool) isGoFile(filePath string) bool {
	return strings.HasSuffix(filePath, ".go")
}

// isTSFile проверяет является ли файл TypeScript файлом
func (t *FormatterTool) isTSFile(filePath string) bool {
	return strings.HasSuffix(filePath, ".ts") ||
		strings.HasSuffix(filePath, ".tsx") ||
		strings.HasSuffix(filePath, ".js") ||
		strings.HasSuffix(filePath, ".jsx")
}

// formatGoFile форматирует Go файл с помощью gofmt
func (t *FormatterTool) formatGoFile(ctx context.Context, filePath string) (bool, error) {
	// Проверяем существует ли gofmt
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.logger.Debug("gofmt not found, skipping Go formatting")
		return false, nil
	}

	// Выполняем форматирование
	cmd := exec.CommandContext(ctx, "gofmt", "-w", filePath)
	if err := cmd.Run(); err != nil {
		return false, err
	}

	t.logger.Info("formatted Go file", "file", filePath)
	return true, nil
}

// formatTSFile форматирует TypeScript файл с помощью prettier
func (t *FormatterTool) formatTSFile(ctx context.Context, filePath string) (bool, error) {
	// Проверяем существует ли prettier
	if _, err := exec.LookPath("prettier"); err != nil {
		t.logger.Debug("prettier not found, skipping TS formatting")
		return false, nil
	}

	// Выполняем форматирование
	cmd := exec.CommandContext(ctx, "prettier", "--write", filePath)
	if err := cmd.Run(); err != nil {
		return false, err
	}

	t.logger.Info("formatted TS file", "file", filePath)
	return true, nil
}
