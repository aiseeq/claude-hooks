package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// formatter описывает внешний форматтер для группы расширений
type formatter struct {
	command    string
	args       []string
	extensions []string
}

// FormatterTool форматирует файлы после записи (PostToolUse)
type FormatterTool struct {
	*BaseTool
	formatters []formatter
}

// NewFormatterTool создает инструмент автоформатирования
func NewFormatterTool(config core.ToolConfig, logger core.Logger) (*FormatterTool, error) {
	var formatters []formatter

	if config.GoFormat {
		formatters = append(formatters, formatter{
			command:    "gofmt",
			args:       []string{"-w"},
			extensions: []string{".go"},
		})
	}
	if config.TSFormat {
		formatters = append(formatters, formatter{
			command:    "prettier",
			args:       []string{"--write"},
			extensions: []string{".ts", ".tsx", ".js", ".jsx"},
		})
	}

	return &FormatterTool{
		BaseTool:   NewBaseTool("formatter", config.Enabled, []string{"Write", "Edit", "MultiEdit"}, logger),
		formatters: formatters,
	}, nil
}

// ValidateTool выполняет форматирование файла
func (t *FormatterTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	// Форматирование имеет смысл только после записи файла на диск
	if !t.IsEnabled() || core.PhaseFromContext(ctx) != core.PhasePost || input.FilePath == "" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	var violations []core.Violation
	var suggestions []string

	for _, f := range t.formatters {
		if !core.IsSupportedFileType(input.FilePath, f.extensions) {
			continue
		}

		formatted, err := t.run(ctx, f, input.FilePath)
		switch {
		case err != nil:
			t.Logger().Warn("formatting failed", "file", input.FilePath, "formatter", f.command, "error", err)
			violations = append(violations, core.NewViolation(
				"format_error",
				fmt.Sprintf("%s не смог отформатировать файл: %v", f.command, err),
				"Проверь синтаксис файла",
				core.LevelWarning,
				0,
				0,
			))
		case formatted:
			suggestions = append(suggestions, fmt.Sprintf("Файл отформатирован через %s", f.command))
		}
	}

	// Форматирование не блокирует операцию: файл уже записан
	return core.NewValidationResult(true, violations, suggestions), nil
}

// run запускает форматтер. Возвращает false если инструмент не установлен
func (t *FormatterTool) run(ctx context.Context, f formatter, filePath string) (bool, error) {
	if _, err := exec.LookPath(f.command); err != nil {
		t.Logger().Debug("formatter not found, skipping", "formatter", f.command)
		return false, nil
	}

	args := append(append([]string{}, f.args...), filePath)
	output, err := exec.CommandContext(ctx, f.command, args...).CombinedOutput()
	if err != nil {
		if message := strings.TrimSpace(string(output)); message != "" {
			return false, fmt.Errorf("%s: %s", err, message)
		}
		return false, err
	}

	t.Logger().Debug("file formatted", "file", filePath, "formatter", f.command)
	return true, nil
}
