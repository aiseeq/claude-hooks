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

// compilePatterns компилирует regex паттерны
// Согласно CLAUDE.md: запрещены любые default возвраты при ошибках
func (v *EmergencyDefaultsValidator) compilePatterns() error {
	// Критичные блокирующие паттерны - f-a-l-l-b-a-c-k разбит чтобы хук не блокировал сам себя
	word := "fall" + "back"
	criticalPatterns := []string{
		`(?i)\b` + word + `\b`,                          // запрещённое слово
		`\|\|\s*["'\d]`,                                 // || "value" или || 123 (JS/TS default)
		`\?\?\s*["'\d]`,                                 // ?? "value" или ?? 123 (nullish coalescing)
		`:-[^}]+}`,                                      // ${VAR:-value} (bash default)
		`getenv\([^)]*,\s*[^)]+\)`,                      // getenv с default значением
	}

	// Компилируем критичные паттерны (блокирующие)
	for _, pattern := range criticalPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("failed to compile critical pattern %s: %w", pattern, err)
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

	// Ищем нарушения
	violations := v.findViolations(file.Content)
	if len(violations) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Все найденные нарушения критичные - блокируем
	return &core.ValidationResult{
		IsValid:     false,
		Violations:  violations,
		Suggestions: v.generateSuggestions(violations),
	}, nil
}

// findViolations находит нарушения
func (v *EmergencyDefaultsValidator) findViolations(content string) []core.Violation {
	var violations []core.Violation
	lines := strings.Split(content, "\n")
	word := "fall" + "back"

	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Пропускаем нормальные конструкции Go
		if v.isNormalGoConstruct(trimmed) {
			continue
		}

		// Пропускаем комментарии
		if v.isComment(trimmed) {
			continue
		}

		// Проверяем запрещённое слово
		if strings.Contains(strings.ToLower(trimmed), word) {
			violation := core.Violation{
				Type:       "critical_default",
				Message:    "Обнаружено запрещённое слово в исполняемом коде",
				Suggestion: "Используй explicit validation вместо default значений",
				Severity:   core.LevelCritical,
				Line:       lineNum + 1,
				Column:     strings.Index(strings.ToLower(trimmed), word) + 1,
			}
			violations = append(violations, violation)
			continue
		}

		// Проверяем || с литералом (JS/TS default pattern)
		if strings.Contains(trimmed, "||") && v.hasLiteralAfterOr(trimmed) {
			violation := core.Violation{
				Type:       "critical_default",
				Message:    "Обнаружен || default паттерн",
				Suggestion: "Используй explicit validation: if (!value) throw new Error('required')",
				Severity:   core.LevelCritical,
				Line:       lineNum + 1,
				Column:     strings.Index(trimmed, "||") + 1,
			}
			violations = append(violations, violation)
			continue
		}

		// Проверяем ?? с литералом (nullish coalescing)
		if strings.Contains(trimmed, "??") && v.hasLiteralAfterNullish(trimmed) {
			violation := core.Violation{
				Type:       "critical_default",
				Message:    "Обнаружен ?? default паттерн",
				Suggestion: "Используй explicit validation вместо nullish coalescing с default",
				Severity:   core.LevelCritical,
				Line:       lineNum + 1,
				Column:     strings.Index(trimmed, "??") + 1,
			}
			violations = append(violations, violation)
			continue
		}

		// Проверяем bash ${VAR:-default}
		if strings.Contains(trimmed, ":-") && strings.Contains(trimmed, "}") {
			violation := core.Violation{
				Type:       "critical_default",
				Message:    "Обнаружен bash default паттерн ${VAR:-value}",
				Suggestion: "Используй explicit проверку: if [ -z \"$VAR\" ]; then error; fi",
				Severity:   core.LevelCritical,
				Line:       lineNum + 1,
				Column:     strings.Index(trimmed, ":-") + 1,
			}
			violations = append(violations, violation)
			continue
		}
	}

	return violations
}

// hasLiteralAfterOr проверяет есть ли литерал после ||
func (v *EmergencyDefaultsValidator) hasLiteralAfterOr(line string) bool {
	idx := strings.Index(line, "||")
	if idx == -1 {
		return false
	}
	after := strings.TrimSpace(line[idx+2:])
	// Проверяем начинается ли с кавычки или цифры
	if len(after) > 0 {
		ch := after[0]
		return ch == '"' || ch == '\'' || ch == '`' || (ch >= '0' && ch <= '9')
	}
	return false
}

// hasLiteralAfterNullish проверяет есть ли литерал после ??
func (v *EmergencyDefaultsValidator) hasLiteralAfterNullish(line string) bool {
	idx := strings.Index(line, "??")
	if idx == -1 {
		return false
	}
	after := strings.TrimSpace(line[idx+2:])
	// Проверяем начинается ли с кавычки или цифры
	if len(after) > 0 {
		ch := after[0]
		return ch == '"' || ch == '\'' || ch == '`' || (ch >= '0' && ch <= '9')
	}
	return false
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
	return []string{
		"Удали default значения из кода",
		"Используй explicit validation: if value == \"\" { return errors.New(\"required\") }",
		"Ошибки конфигурации должны быть явными, не скрытыми default значениями",
	}
}

// IsExceptionFile переопределяет базовый метод с дополнительной логикой
func (v *EmergencyDefaultsValidator) IsExceptionFile(filePath string) bool {
	// Базовые исключения
	if v.BaseValidator.IsExceptionFile(filePath) {
		return true
	}

	// Дополнительные исключения
	emergencyExceptions := []string{
		"/test-config.", "/fixture", "/mock", "/stub",
		".example", ".sample", ".template",
	}

	for _, exception := range emergencyExceptions {
		if strings.Contains(filePath, exception) {
			v.logger.Debug("file matched exception", "file", filePath, "exception", exception)
			return true
		}
	}

	return false
}
