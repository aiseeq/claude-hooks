package validators

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// exitPatterns паттерны аварийного завершения процесса.
// Собирать имена из кусков не требуется: проверка идет по коду,
// очищенному от строковых литералов и комментариев
var exitPatterns = []string{
	`\bpanic\s*\(`,
	`\blog\.Fatal[fn]?\s*\(`,
	`\bos\.Exit\s*\(`,
}

// RuntimeExitValidator блокирует аварийное завершение процесса в библиотечном коде
type RuntimeExitValidator struct {
	*BaseValidator
	goFilesOnly bool
	patterns    []*regexp.Regexp
}

// NewRuntimeExitValidator создает валидатор аварийных выходов
func NewRuntimeExitValidator(config core.ValidatorConfig, logger core.Logger) (*RuntimeExitValidator, error) {
	patterns, err := compilePatterns(exitPatterns)
	if err != nil {
		return nil, fmt.Errorf("failed to compile runtime exit patterns: %w", err)
	}

	return &RuntimeExitValidator{
		BaseValidator: NewBaseValidator("runtime_exit", config, logger),
		goFilesOnly:   config.GoFilesOnly,
		patterns:      patterns,
	}, nil
}

// Validate выполняет валидацию файла
func (v *RuntimeExitValidator) Validate(ctx context.Context, file *core.FileAnalysis) (*core.ValidationResult, error) {
	if !v.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	if v.goFilesOnly && !strings.HasSuffix(file.Path, ".go") {
		v.logger.Debug("not a Go file, skipping", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	if v.IsExceptionFile(file.Path) {
		v.logger.Debug("file is exception, skipping validation", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Упоминание в комментарии или в тексте сообщения не является использованием
	matches := v.FindPatternMatches(StripNonCode(file.Content), v.patterns)
	if len(matches) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	violations := make([]core.Violation, 0, len(matches))
	for _, match := range matches {
		violationType := determineViolationType(match.Text)
		violations = append(violations, core.CreateViolation(
			match,
			violationType,
			violationMessage(violationType),
			violationSuggestion(violationType),
			core.LevelCritical,
		))
	}

	v.logger.Info("runtime exit usage detected", "file", file.Path, "violations", len(violations))

	return &core.ValidationResult{
		IsValid:     false,
		Violations:  violations,
		Suggestions: v.suggestions(file),
	}, nil
}

// determineViolationType определяет тип нарушения по тексту совпадения
func determineViolationType(matchText string) string {
	switch {
	case strings.Contains(matchText, "panic"):
		return "runtime_exit_usage"
	case strings.Contains(matchText, "Fatal"):
		return "log_fatal_usage"
	default:
		return "critical_exit"
	}
}

// violationMessage генерирует сообщение для нарушения
func violationMessage(violationType string) string {
	switch violationType {
	case "runtime_exit_usage":
		return "Использование критического выхода в production коде запрещено"
	case "log_fatal_usage":
		return "Использование критического логирования в production коде не рекомендуется"
	default:
		return "Критическое завершение программы в production коде"
	}
}

// violationSuggestion генерирует предложение по исправлению
func violationSuggestion(violationType string) string {
	switch violationType {
	case "runtime_exit_usage":
		return "Используй return fmt.Errorf(\"error: %w\", err) вместо критического выхода"
	case "log_fatal_usage":
		return "Используй logger.Error() и graceful shutdown вместо критического логирования"
	default:
		return "Реализуй graceful error handling вместо принудительного завершения"
	}
}

// suggestions генерирует общие рекомендации
func (v *RuntimeExitValidator) suggestions(file *core.FileAnalysis) []string {
	suggestions := []string{
		"Возвращай ошибку из функции: func() error { return fmt.Errorf(...) }",
		"Реализуй graceful error handling на уровне приложения",
		"Обрабатывай ошибку на верхнем уровне — там, где известен контекст",
	}

	if strings.Contains(file.Content, "func main()") {
		suggestions = append(suggestions,
			"Точка входа приложения — исключение: вынеси её в cmd/ или main.go",
			"Для CLI используй cobra.Command с RunE и возвратом ошибки",
		)
	}

	return suggestions
}
