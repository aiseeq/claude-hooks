package validators

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// EmergencyDefaultsValidator проверяет использование запасных значений
type EmergencyDefaultsValidator struct {
	*BaseValidator
	caseSensitive bool
	patterns      []*regexp.Regexp
}

// NewEmergencyDefaultsValidator создает новый валидатор запасных значений
func NewEmergencyDefaultsValidator(config core.ValidatorConfig, logger core.Logger) (*EmergencyDefaultsValidator, error) {
	baseValidator := NewBaseValidator("emergency_defaults", config.Enabled, config.ExceptionPaths, logger)

	validator := &EmergencyDefaultsValidator{
		BaseValidator: baseValidator,
		caseSensitive: config.CaseSensitive,
	}

	// Компилируем паттерны для поиска запасных значений
	if err := validator.compilePatterns(); err != nil {
		return nil, fmt.Errorf("failed to compile patterns: %w", err)
	}

	return validator, nil
}

// compilePatterns компилирует regex паттерны согласно дружелюбной философии
func (v *EmergencyDefaultsValidator) compilePatterns() error {
	// ТОЛЬКО критичное блокирующее нарушение - слово "fallback"
	criticalPatterns := []string{
		`(?i)\bfallback\b`, // Только слово "fallback" блокируется критично
	}

	// Предупреждающие паттерны (не блокируют, только предупреждают)
	warningPatterns := []string{
		`\|\|\s*([\"\']*[^\"\']*[\"\']*|\w+|\d+)`,       // || "value" - предупреждение
		`\?\?\s*([\"\']*[^\"\']*[\"\']*|\w+|\d+)`,       // ?? "value" - предупреждение
		`or\s+([\"\']*[^\"\']*[\"\']*|\w+)`,             // or "value" - предупреждение
		`:-[^}]*}`,                                      // ${VAR:-value} - предупреждение
		`getenv\([^)]*,\s*([\"\']*[^\"\']*[\"\']*|\w+)`, // getenv with default - предупреждение
	}

	// Другие подозрительные слова как предупреждения (НЕ блокирующие)
	warningWords := []string{"backup", "emergency", "spare", "reserve"}

	// Компилируем критичные паттерны (блокирующие)
	for _, pattern := range criticalPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("failed to compile critical pattern %s: %w", pattern, err)
		}
		v.patterns = append(v.patterns, compiled)
	}

	// Компилируем предупреждающие паттерны
	for _, pattern := range warningPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("failed to compile warning pattern %s: %w", pattern, err)
		}
		v.patterns = append(v.patterns, compiled)
	}

	// Добавляем предупреждающие слова
	for _, word := range warningWords {
		pattern := fmt.Sprintf(`(?i)\b%s\b`, word)
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("failed to compile warning word %s: %w", word, err)
		}
		v.patterns = append(v.patterns, compiled)
	}

	return nil
}

// Validate выполняет валидацию файла
func (v *EmergencyDefaultsValidator) Validate(ctx context.Context, file *core.FileAnalysis) (*core.ValidationResult, error) {
	if !v.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем исключения
	if v.IsExceptionFile(file.Path) {
		v.logger.Debug("file is exception, skipping validation", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем поддерживаемые типы файлов
	supportedExtensions := []string{".go", ".ts", ".js", ".tsx", ".jsx", ".py", ".sh", ".bash"}
	if !isSupportedFileType(file.Path, supportedExtensions) {
		v.logger.Debug("file type not supported, skipping", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Ищем совпадения с правильным определением серьезности
	violations := v.findViolationsWithSeverity(file.Content)
	if len(violations) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем есть ли критичные нарушения (только они блокируют)
	hasCritical := false
	for _, violation := range violations {
		if violation.Severity == core.LevelCritical {
			hasCritical = true
			break
		}
	}

	return &core.ValidationResult{
		IsValid:     !hasCritical, // Блокируем только при критичных нарушениях
		Violations:  violations,
		Suggestions: v.generateSuggestions(violations),
	}, nil
}

// findViolationsWithSeverity находит нарушения с правильной серьезностью
func (v *EmergencyDefaultsValidator) findViolationsWithSeverity(content string) []core.Violation {
	var violations []core.Violation
	lines := strings.Split(content, "\n")

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Пропускаем нормальные конструкции Go
		if v.isNormalGoConstruct(line) {
			continue
		}

		// Пропускаем комментарии - слово fallback в комментарии допустимо
		// (используется для документирования альтернативных подходов)
		if v.isComment(line) {
			continue
		}

		// Проверяем критичные паттерны (только fallback в исполняемом коде)
		if strings.Contains(strings.ToLower(line), "fallback") {
			violation := core.Violation{
				Type:       "critical_fallback",
				Message:    "🚨 КРИТИЧНО: Обнаружено слово 'fallback' в исполняемом коде",
				Suggestion: "Используй explicit validation вместо fallback значений",
				Severity:   core.LevelCritical, // Блокирует операцию
				Line:       lineNum + 1,
				Column:     strings.Index(strings.ToLower(line), "fallback") + 1,
			}
			violations = append(violations, violation)
		}

		// Проверяем предупреждающие паттерны
		warningPatterns := []string{"||", "??", " or ", "getenv(", "backup", "emergency", "spare", "reserve"}
		for _, pattern := range warningPatterns {
			if strings.Contains(line, pattern) {
				violation := core.Violation{
					Type:       "warning_default",
					Message:    fmt.Sprintf("💡 ПРЕДУПРЕЖДЕНИЕ: Возможно запасное значение с '%s'", pattern),
					Suggestion: "Рассмотри использование explicit validation",
					Severity:   core.LevelWarning, // НЕ блокирует операцию
					Line:       lineNum + 1,
					Column:     strings.Index(line, pattern) + 1,
				}
				violations = append(violations, violation)
				break // Только одно предупреждение на строку
			}
		}
	}

	return violations
}

// isNormalGoConstruct проверяет нормальные конструкции языка
func (v *EmergencyDefaultsValidator) isNormalGoConstruct(line string) bool {
	// Go switch/select default case
	if strings.Contains(line, "default:") {
		return true
	}

	// Go struct tags
	if strings.Contains(line, "`") && strings.Contains(line, "default") {
		return true
	}

	// Go функции с default в имени
	if strings.Contains(line, "func") && strings.Contains(line, "Default") {
		return true
	}

	return false
}

// isComment проверяет является ли строка комментарием
func (v *EmergencyDefaultsValidator) isComment(line string) bool {
	// Go/JS/TS однострочные комментарии
	if strings.HasPrefix(line, "//") {
		return true
	}

	// Python/Shell комментарии
	if strings.HasPrefix(line, "#") {
		return true
	}

	// Многострочные комментарии (начало)
	if strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "/**") {
		return true
	}

	// Строки внутри многострочного комментария (обычно начинаются с *)
	if strings.HasPrefix(line, "*") {
		return true
	}

	return false
}

// generateSuggestions генерирует предложения по исправлению
func (v *EmergencyDefaultsValidator) generateSuggestions(violations []core.Violation) []string {
	suggestions := []string{}

	hasCritical := false
	hasWarnings := false

	for _, violation := range violations {
		if violation.Severity == core.LevelCritical {
			hasCritical = true
		} else {
			hasWarnings = true
		}
	}

	if hasCritical {
		suggestions = append(suggestions, "🚨 КРИТИЧНО: Удали все слова 'fallback' из кода")
		suggestions = append(suggestions, "Используй explicit validation: if value == \"\" { return errors.New(\"required\") }")
	}

	if hasWarnings {
		suggestions = append(suggestions, "💡 Рассмотри замену запасных значений на explicit validation")
		suggestions = append(suggestions, "Пример: cfg.GetPort() вместо os.Getenv(\"PORT\") || \"8080\"")
	}

	return suggestions
}

// IsExceptionFile переопределяет базовый метод с дополнительной логикой
func (v *EmergencyDefaultsValidator) IsExceptionFile(filePath string) bool {
	// Базовые исключения
	if v.BaseValidator.IsExceptionFile(filePath) {
		return true
	}

	// Дополнительные исключения для emergency defaults
	emergencyExceptions := []string{
		"/test-config.", "/fixture", "/mock", "/stub",
		".example", ".sample", ".template",
	}

	for _, exception := range emergencyExceptions {
		if strings.Contains(filePath, exception) {
			v.logger.Debug("file matched emergency defaults exception", "file", filePath, "exception", exception)
			return true
		}
	}

	return false
}
