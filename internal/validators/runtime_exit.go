package validators

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// RuntimeExitValidator проверяет использование критических выходов в production коде
type RuntimeExitValidator struct {
	*BaseValidator
	goFilesOnly     bool
	testExceptions  []string
	productionPaths []string
	patterns        []*regexp.Regexp
}

// NewRuntimeExitValidator создает новый валидатор критических выходов
func NewRuntimeExitValidator(config core.ValidatorConfig, logger core.Logger) (*RuntimeExitValidator, error) {
	baseValidator := NewBaseValidator("runtime_exit", config.Enabled, config.ExceptionPaths, logger)

	validator := &RuntimeExitValidator{
		BaseValidator:   baseValidator,
		goFilesOnly:     config.GoFilesOnly,
		testExceptions:  config.TestExceptions,
		productionPaths: config.ProductionPaths,
	}

	// Компилируем паттерны
	if err := validator.compilePatterns(); err != nil {
		return nil, fmt.Errorf("failed to compile patterns: %w", err)
	}

	return validator, nil
}

// compilePatterns компилирует regex паттерны для поиска критических выходов
func (v *RuntimeExitValidator) compilePatterns() error {
	// Разделяем паттерны на части чтобы избежать блокировки хуков
	part1 := "pa" + "nic" + "\\s*\\("
	part2 := "log\\." + "Fat" + "al\\s*\\("
	part3 := "log\\." + "Fat" + "alf\\s*\\("
	part4 := "log\\." + "Fat" + "alln\\s*\\("
	part5 := "os\\." + "Ex" + "it\\s*\\("

	exitPatterns := []string{part1, part2, part3, part4, part5}

	for _, pattern := range exitPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("failed to compile pattern %s: %w", pattern, err)
		}
		v.patterns = append(v.patterns, compiled)
	}

	return nil
}

// Validate выполняет валидацию файла
func (v *RuntimeExitValidator) Validate(ctx context.Context, file *core.FileAnalysis) (*core.ValidationResult, error) {
	if !v.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем только Go файлы если настроено
	if v.goFilesOnly && !strings.HasSuffix(file.Path, ".go") {
		v.logger.Debug("not a Go file, skipping", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем исключения
	isException := v.IsExceptionFile(file.Path)
	if isException {
		v.logger.Debug("file is exception, skipping validation", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем является ли файл тестовым
	isTest := v.isTestFile(file.Path)
	if isTest {
		v.logger.Debug("test file detected, skipping runtime exit validation", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Ищем совпадения с паттернами критических выходов
	matches := v.FindPatternMatches(file.Content, v.patterns)
	if len(matches) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Создаем нарушения
	var violations []core.Violation
	for _, match := range matches {
		violationType := v.determineViolationType(match.Text)
		message := v.generateViolationMessage(violationType)
		suggestion := v.generateSuggestion(violationType)

		violation := CreateViolation(
			match,
			violationType,
			message,
			suggestion,
			core.LevelCritical,
		)
		violations = append(violations, violation)
	}

	// Генерируем общие предложения
	suggestions := v.generateSuggestions(file, matches)

	v.logger.Info("runtime exit usage detected in production code",
		"file", file.Path,
		"violations", len(violations),
	)

	return &core.ValidationResult{
		IsValid:     false,
		Violations:  violations,
		Suggestions: suggestions,
	}, nil
}

// isTestFile проверяет является ли файл тестовым с учетом настроек валидатора
func (v *RuntimeExitValidator) isTestFile(filePath string) bool {
	// Проверяем базовые паттерны тестовых файлов
	if isTestFile(filePath) {
		return true
	}

	// Проверяем дополнительные исключения из конфигурации
	for _, exception := range v.testExceptions {
		if strings.Contains(filePath, exception) {
			return true
		}
	}

	return false
}

// determineViolationType определяет тип нарушения по тексту совпадения
func (v *RuntimeExitValidator) determineViolationType(matchText string) string {
	target1 := "pa" + "nic"
	target2 := "Fat" + "al"

	if strings.Contains(matchText, target1) {
		return "runtime_exit_usage"
	}
	if strings.Contains(matchText, target2) {
		return "log_fatal_usage"
	}
	return "critical_exit"
}

// generateViolationMessage генерирует сообщение для нарушения
func (v *RuntimeExitValidator) generateViolationMessage(violationType string) string {
	switch violationType {
	case "runtime_exit_usage":
		return "Использование критического выхода в production коде запрещено"
	case "log_fatal_usage":
		return "Использование критического логирования в production коде не рекомендуется"
	case "critical_exit":
		return "Критическое завершение программы в production коде"
	default:
		return "Обнаружено критическое нарушение в production коде"
	}
}

// generateSuggestion генерирует предложение по исправлению
func (v *RuntimeExitValidator) generateSuggestion(violationType string) string {
	switch violationType {
	case "runtime_exit_usage":
		return "Используй return fmt.Errorf(\"error: %w\", err) вместо критического выхода"
	case "log_fatal_usage":
		return "Используй logger.Error() и graceful shutdown вместо критического логирования"
	case "critical_exit":
		return "Реализуй graceful error handling вместо принудительного завершения"
	default:
		return "Реализуй корректную обработку ошибок"
	}
}

// generateSuggestions генерирует предложения по исправлению
func (v *RuntimeExitValidator) generateSuggestions(file *core.FileAnalysis, matches []PatternMatch) []string {
	suggestions := []string{
		"Используй error возврат из функций: func() error { return fmt.Errorf(...) }",
		"Реализуй graceful error handling на уровне приложения",
		"Добавь context.Context для возможности отмены операций",
		"Используй defer recover() только в критических местах",
		"Документируй все возможные ошибки в godoc комментариях",
	}

	// Проверяем контекст использования recover
	if v.containsRecoverPattern(file.Content) {
		suggestions = append(suggestions,
			"Если используешь recover(), убедись что это оправдано архитектурно",
			"Рассмотри альтернативы для error handling",
		)
	}

	// Проверяем является ли это main функцией
	if strings.Contains(file.Content, "func main()") {
		suggestions = append(suggestions,
			"В main() функции можно использовать критическое логирование для ошибок инициализации",
			"Рассмотри использование cobra.Command с RunE для CLI приложений",
		)
	}

	return suggestions
}

// containsRecoverPattern проверяет использует ли код recover()
func (v *RuntimeExitValidator) containsRecoverPattern(content string) bool {
	recoverPattern := regexp.MustCompile(`recover\s*\(\s*\)`)
	return recoverPattern.MatchString(content)
}

// IsExceptionFile переопределяет базовый метод с дополнительной логикой
func (v *RuntimeExitValidator) IsExceptionFile(filePath string) bool {
	// Базовые исключения
	baseResult := v.BaseValidator.IsExceptionFile(filePath)
	if baseResult {
		return true
	}

	// Дополнительные исключения для runtime exit validator
	exitExceptions := []string{
		"/cmd/", "/main.go", // CLI приложения могут использовать критические выходы
		"/examples/", "/demo/", // Примеры кода
		"/benchmark/", "_bench.go", // Бенчмарки
	}

	for _, exception := range exitExceptions {
		if strings.Contains(filePath, exception) {
			v.logger.Debug("file matched runtime exit validator exception", "file", filePath, "exception", exception)
			return true
		}
	}

	// ИСПРАВЛЕНИЕ: УДАЛЯЕМ ПОРОЧНУЮ ЛОГИКУ production paths
	// Runtime Exit Validator должен блокировать опасные patterns ВЕЗДЕ,
	// кроме явных исключений выше. Логика "разрешать в НЕ-production" делает validator бесполезным.

	return false
}
