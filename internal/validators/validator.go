package validators

import (
	"regexp"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// BaseValidator общая часть всех валидаторов
type BaseValidator struct {
	name       string
	enabled    bool
	exceptions []string
	logger     core.Logger
}

// NewBaseValidator создает базовый валидатор
func NewBaseValidator(name string, config core.ValidatorConfig, logger core.Logger) *BaseValidator {
	return &BaseValidator{
		name:       name,
		enabled:    config.Enabled,
		exceptions: config.Exceptions,
		logger:     logger.With("validator", name),
	}
}

// Name возвращает имя валидатора
func (v *BaseValidator) Name() string {
	return v.name
}

// IsEnabled проверяет включен ли валидатор
func (v *BaseValidator) IsEnabled() bool {
	return v.enabled
}

// IsExceptionFile проверяет является ли файл исключением
func (v *BaseValidator) IsExceptionFile(filePath string) bool {
	return core.IsExceptionFile(filePath, v.exceptions, v.logger)
}

// FindPatternMatches ищет совпадения с паттернами
func (v *BaseValidator) FindPatternMatches(content string, patterns []*regexp.Regexp) []core.PatternMatch {
	return core.FindPatternMatches(content, patterns)
}

// compilePatterns компилирует список regex паттернов
func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}
