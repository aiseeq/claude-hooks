package validators

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// Паттерны секретов по умолчанию
var (
	// Сам паттерн под себя не подходит: за префиксом идет `[`, которого нет в классе символов
	defaultJWTPattern = `eyJ[A-Za-z0-9_/+=-]{10,}(?:\.[A-Za-z0-9_/+=-]+){0,2}`
	// Wallet-адрес: ровно 40 hex-символов с границами слова
	defaultWalletPattern = `\b0x[a-fA-F0-9]{40}\b`
	// Ключи вида sk_live_..., pk_test_..., ghp_..., xoxb-...: разделители входят
	// в набор символов, иначе реальные ключи не находятся
	defaultAPIKeyPattern = `\b(?:sk|pk|api_key|apikey|access_token|secret_key|ghp|gho|ghs|xox[baprs])[-_][A-Za-z0-9_-]{16,}`
)

// secretPattern описывает искомый тип секрета
type secretPattern struct {
	regexp     *regexp.Regexp
	violation  string
	message    string
	suggestion string
}

// SecretsValidator блокирует секреты, попадающие в исходный код
type SecretsValidator struct {
	*BaseValidator
	patterns []secretPattern
}

// NewSecretsValidator создает валидатор секретов
func NewSecretsValidator(config core.ValidatorConfig, logger core.Logger) (*SecretsValidator, error) {
	specs := []struct {
		pattern     string
		defaultExpr string
		violation   string
		message     string
		suggestion  string
	}{
		{
			pattern:     config.JWTPattern,
			defaultExpr: defaultJWTPattern,
			violation:   "hardcoded_jwt",
			message:     "Обнаружен hardcoded JWT токен",
			suggestion:  "Используй переменные окружения или тестовую конфигурацию",
		},
		{
			pattern:     config.WalletPattern,
			defaultExpr: defaultWalletPattern,
			violation:   "hardcoded_wallet",
			message:     "Обнаружен hardcoded wallet address",
			suggestion:  "Вынеси адреса в конфигурацию или переменные окружения",
		},
		{
			pattern:     config.APIKeyPattern,
			defaultExpr: defaultAPIKeyPattern,
			violation:   "hardcoded_api_key",
			message:     "Обнаружен hardcoded API ключ",
			suggestion:  "Используй переменные окружения или секрет-менеджер",
		},
	}

	patterns := make([]secretPattern, 0, len(specs))
	for _, spec := range specs {
		expression := spec.pattern
		if expression == "" {
			expression = spec.defaultExpr
		}
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("failed to compile %s pattern: %w", spec.violation, err)
		}
		patterns = append(patterns, secretPattern{
			regexp:     compiled,
			violation:  spec.violation,
			message:    spec.message,
			suggestion: spec.suggestion,
		})
	}

	return &SecretsValidator{
		BaseValidator: NewBaseValidator("secrets", config, logger),
		patterns:      patterns,
	}, nil
}

// Validate выполняет валидацию файла
func (v *SecretsValidator) Validate(ctx context.Context, file *core.FileAnalysis) (*core.ValidationResult, error) {
	if !v.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	if v.IsExceptionFile(file.Path) {
		v.logger.Debug("file is exception, skipping validation", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	supportedExtensions := []string{".go", ".ts", ".js", ".tsx", ".jsx", ".py", ".json", ".yaml", ".yml", ".env", ".sh"}
	if !core.IsSupportedFileType(file.Path, supportedExtensions) {
		v.logger.Debug("file type not supported, skipping", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	var violations []core.Violation
	for _, pattern := range v.patterns {
		for _, match := range core.FindPatternMatches(file.Content, []*regexp.Regexp{pattern.regexp}) {
			violations = append(violations, core.CreateViolation(
				match,
				pattern.violation,
				pattern.message,
				pattern.suggestion,
				core.LevelCritical,
			))
		}
	}

	if len(violations) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	v.logger.Info("secrets detected in code", "file", file.Path, "violations", len(violations))

	return &core.ValidationResult{
		IsValid:     false,
		Violations:  violations,
		Suggestions: v.suggestions(file),
	}, nil
}

// suggestions генерирует рекомендации с учетом языка файла
func (v *SecretsValidator) suggestions(file *core.FileAnalysis) []string {
	var suggestions []string

	switch strings.ToLower(file.Extension) {
	case ".go":
		suggestions = append(suggestions,
			"Читай значение через os.Getenv() и проверяй его при старте приложения",
		)
	case ".ts", ".js", ".tsx", ".jsx":
		suggestions = append(suggestions,
			"Читай значение через process.env и держи тестовые данные в отдельном модуле",
		)
	case ".json", ".yaml", ".yml":
		suggestions = append(suggestions,
			"Подставляй значения из переменных окружения вместо записи в конфигурацию",
		)
	}

	return append(suggestions,
		"Не коммить реальные секреты: используй .env (добавленный в .gitignore) или секрет-менеджер",
		"Если секрет уже попал в репозиторий — отзови его, ротация обязательна",
	)
}
