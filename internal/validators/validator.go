package validators

import (
	"regexp"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/shared"
)

// BaseValidator базовая реализация валидатора
type BaseValidator struct {
	name       string
	enabled    bool
	exceptions []string
	patterns   []*regexp.Regexp
	logger     core.Logger
}

// NewBaseValidator создает новый базовый валидатор
func NewBaseValidator(name string, enabled bool, exceptions []string, logger core.Logger) *BaseValidator {
	return &BaseValidator{
		name:       name,
		enabled:    enabled,
		exceptions: exceptions,
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

// GetExceptions возвращает список исключений
func (v *BaseValidator) GetExceptions() []string {
	return v.exceptions
}

// AddPattern добавляет regex паттерн
func (v *BaseValidator) AddPattern(pattern string) error {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	v.patterns = append(v.patterns, compiled)
	return nil
}

// IsExceptionFile проверяет является ли файл исключением
// CANONICAL VERSION - использует shared.IsExceptionFile
func (v *BaseValidator) IsExceptionFile(filePath string) bool {
	return shared.IsExceptionFile(filePath, v.exceptions, v.logger)
}

// FindPatternMatches ищет совпадения с паттернами
// CANONICAL VERSION - использует shared.FindPatternMatches
func (v *BaseValidator) FindPatternMatches(content string, patterns []*regexp.Regexp) []shared.PatternMatch {
	return shared.FindPatternMatches(content, patterns)
}

// PatternMatch и CreateViolation теперь используются из shared пакета
// Алиасы для обратной совместимости
type PatternMatch = shared.PatternMatch

var CreateViolation = shared.CreateViolation

// Все utility функции теперь используются из shared пакета
// Алиасы для обратной совместимости с существующим кодом

var isDocumentationFile = shared.IsDocumentationFile
var isTestFile = shared.IsTestFile
var getFileName = shared.GetFileName
var isSupportedFileType = shared.IsSupportedFileType
