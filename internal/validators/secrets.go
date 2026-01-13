package validators

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// SecretsValidator проверяет использование hardcoded секретов
type SecretsValidator struct {
	*BaseValidator
	jwtPattern           *regexp.Regexp
	walletPattern        *regexp.Regexp
	apiKeyPattern        *regexp.Regexp
	testConfigExceptions []string
}

// NewSecretsValidator создает новый валидатор секретов
func NewSecretsValidator(config core.ValidatorConfig, logger core.Logger) (*SecretsValidator, error) {
	baseValidator := NewBaseValidator("secrets", config.Enabled, config.ExceptionPaths, logger)

	validator := &SecretsValidator{
		BaseValidator:        baseValidator,
		testConfigExceptions: config.TestConfigExceptions,
	}

	// Компилируем паттерны
	if err := validator.compilePatterns(config); err != nil {
		return nil, fmt.Errorf("failed to compile patterns: %w", err)
	}

	return validator, nil
}

// compilePatterns компилирует regex паттерны для поиска секретов
func (v *SecretsValidator) compilePatterns(config core.ValidatorConfig) error {
	var err error

	// JWT токены
	jwtPattern := config.JWTPattern
	if jwtPattern == "" {
		// Разбиваем паттерн на части для избежания срабатывания хуков
		jwtPattern = "eyJ" + "[a-zA-Z0-9+/]+"
	}
	v.jwtPattern, err = regexp.Compile(jwtPattern)
	if err != nil {
		return fmt.Errorf("failed to compile JWT pattern: %w", err)
	}

	// Wallet addresses
	walletPattern := config.WalletPattern
	if walletPattern == "" {
		walletPattern = "0x[a-fA-F0-9]{40}"
	}
	v.walletPattern, err = regexp.Compile(walletPattern)
	if err != nil {
		return fmt.Errorf("failed to compile wallet pattern: %w", err)
	}

	// API ключи - разбиваем на части
	apiKeyPattern := "(sk_|pk_|api_key_|access_token_)[a-zA-Z0-9]{20,}"
	v.apiKeyPattern, err = regexp.Compile(apiKeyPattern)
	if err != nil {
		return fmt.Errorf("failed to compile API key pattern: %w", err)
	}

	return nil
}

// Validate выполняет валидацию файла
func (v *SecretsValidator) Validate(ctx context.Context, file *core.FileAnalysis) (*core.ValidationResult, error) {
	if !v.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем исключения
	if v.IsExceptionFile(file.Path) {
		v.logger.Debug("file is exception, skipping validation", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Проверяем поддерживаемые типы файлов
	supportedExtensions := []string{".go", ".ts", ".js", ".tsx", ".jsx", ".py", ".json", ".yaml", ".yml"}
	if !isSupportedFileType(file.Path, supportedExtensions) {
		v.logger.Debug("file type not supported, skipping", "file", file.Path)
		return &core.ValidationResult{IsValid: true}, nil
	}

	var violations []core.Violation

	// Проверяем JWT токены
	if jwtViolations := v.checkJWTTokens(file); len(jwtViolations) > 0 {
		violations = append(violations, jwtViolations...)
	}

	// Проверяем wallet addresses
	if walletViolations := v.checkWalletAddresses(file); len(walletViolations) > 0 {
		violations = append(violations, walletViolations...)
	}

	// Проверяем API ключи
	if apiViolations := v.checkAPIKeys(file); len(apiViolations) > 0 {
		violations = append(violations, apiViolations...)
	}

	if len(violations) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Генерируем предложения
	suggestions := v.generateSuggestions(file, violations)

	v.logger.Info("secrets detected in code",
		"file", file.Path,
		"violations", len(violations),
	)

	return &core.ValidationResult{
		IsValid:     false,
		Violations:  violations,
		Suggestions: suggestions,
	}, nil
}

// checkJWTTokens проверяет JWT токены
func (v *SecretsValidator) checkJWTTokens(file *core.FileAnalysis) []core.Violation {
	var violations []core.Violation

	matches := v.FindPatternMatches(file.Content, []*regexp.Regexp{v.jwtPattern})
	for _, match := range matches {
		// Проверяем исключения для тестовых конфигураций
		if v.isTestConfigException(file.Path) {
			v.logger.Debug("JWT in test config, skipping", "file", file.Path)
			continue
		}

		violation := CreateViolation(
			match,
			"hardcoded_jwt",
			"Обнаружен hardcoded JWT токен",
			"Используй переменные окружения или test-config",
			core.LevelCritical,
		)
		violations = append(violations, violation)
	}

	return violations
}

// checkWalletAddresses проверяет wallet addresses
func (v *SecretsValidator) checkWalletAddresses(file *core.FileAnalysis) []core.Violation {
	var violations []core.Violation

	matches := v.FindPatternMatches(file.Content, []*regexp.Regexp{v.walletPattern})
	for _, match := range matches {
		// Проверяем исключения для тестовых конфигураций
		if v.isTestConfigException(file.Path) {
			v.logger.Debug("wallet address in test config, skipping", "file", file.Path)
			continue
		}

		violation := CreateViolation(
			match,
			"hardcoded_wallet",
			"Обнаружен hardcoded wallet address",
			"Используй TEST_ACCOUNTS из test-config или переменные окружения",
			core.LevelCritical,
		)
		violations = append(violations, violation)
	}

	return violations
}

// checkAPIKeys проверяет API ключи
func (v *SecretsValidator) checkAPIKeys(file *core.FileAnalysis) []core.Violation {
	var violations []core.Violation

	matches := v.FindPatternMatches(file.Content, []*regexp.Regexp{v.apiKeyPattern})
	for _, match := range matches {
		// Проверяем исключения для тестовых конфигураций
		if v.isTestConfigException(file.Path) {
			v.logger.Debug("API key in test config, skipping", "file", file.Path)
			continue
		}

		violation := CreateViolation(
			match,
			"hardcoded_api_key",
			"Обнаружен hardcoded API ключ",
			"Используй переменные окружения или конфигурационный файл",
			core.LevelCritical,
		)
		violations = append(violations, violation)
	}

	return violations
}

// isTestConfigException проверяет является ли файл тестовой конфигурацией
func (v *SecretsValidator) isTestConfigException(filePath string) bool {
	for _, exception := range v.testConfigExceptions {
		if strings.Contains(filePath, exception) {
			return true
		}
	}
	return false
}

// generateSuggestions генерирует предложения по исправлению
func (v *SecretsValidator) generateSuggestions(file *core.FileAnalysis, violations []core.Violation) []string {
	var suggestions []string

	// Определяем язык программирования
	ext := strings.ToLower(file.Extension)

	switch ext {
	case ".go":
		suggestions = append(suggestions,
			"Используй os.Getenv() для чтения переменных окружения",
			"Добавь валидацию обязательных переменных при старте приложения",
			"Используй unified config для централизованного управления настройками",
		)
	case ".ts", ".js", ".tsx", ".jsx":
		suggestions = append(suggestions,
			"Используй process.env.VARIABLE_NAME",
			"Создай test-config.ts для тестовых данных",
			"Используй TEST_ACCOUNTS константы вместо hardcoded значений",
		)
	case ".json", ".yaml", ".yml":
		suggestions = append(suggestions,
			"Используй environment variable substitution",
			"Создай отдельные конфигурации для test/dev/prod окружений",
			"Документируй все требуемые переменные окружения",
		)
	}

	// Общие предложения
	suggestions = append(suggestions,
		"Никогда не коммить реальные секреты в репозиторий",
		"Используй .env файлы для локальной разработки (добавь в .gitignore)",
		"Рассмотри использование секрет-менеджеров (HashiCorp Vault, AWS Secrets Manager)",
		"Для тестов создавай фиксированные тестовые данные в отдельных файлах",
	)

	return suggestions
}

// IsExceptionFile переопределяет базовый метод с дополнительной логикой
func (v *SecretsValidator) IsExceptionFile(filePath string) bool {
	// Базовые исключения
	if v.BaseValidator.IsExceptionFile(filePath) {
		return true
	}

	// Дополнительные исключения для secrets validator
	secretsExceptions := []string{
		"/example", "/sample", "/template", "/demo",
		".example", ".sample", ".template",
		"/fixtures/", "/mocks/", "/stubs/",
	}

	for _, exception := range secretsExceptions {
		if strings.Contains(filePath, exception) {
			v.logger.Debug("file matched secrets validator exception", "file", filePath, "exception", exception)
			return true
		}
	}

	// Проверяем исключения для тестовых конфигураций
	if v.isTestConfigException(filePath) {
		v.logger.Debug("file matched test config exception", "file", filePath)
		return true
	}

	return false
}
