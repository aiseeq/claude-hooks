package shared

import (
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// IsExceptionFile проверяет является ли файл исключением
// CANONICAL VERSION - заменяет дублированные функции в BaseValidator и BaseAdvisor
func IsExceptionFile(filePath string, exceptions []string, logger core.Logger) bool {
	// Проверяем по путям из конфигурации
	for _, exception := range exceptions {
		if strings.Contains(filePath, exception) {
			logger.Debug("file matched exception path", "file", filePath, "exception", exception)
			return true
		}
	}

	// Проверяем файлы документации
	if IsDocumentationFile(filePath) {
		logger.Debug("file is documentation", "file", filePath)
		return true
	}

	// Проверяем тестовые файлы
	if IsTestFile(filePath) {
		logger.Debug("file is test file", "file", filePath)
		return true
	}

	return false
}

// IsDocumentationFile проверяет является ли файл документацией
// CANONICAL VERSION - заменяет дублированные функции в BaseValidator и BaseAdvisor
func IsDocumentationFile(filePath string) bool {
	// Расширения документации
	docExtensions := []string{".md", ".txt", ".rst", ".adoc"}
	for _, ext := range docExtensions {
		if strings.HasSuffix(strings.ToLower(filePath), ext) {
			return true
		}
	}

	// Специальные файлы
	docFiles := []string{"README", "CHANGELOG", "LICENSE", "AUTHORS", "CONTRIBUTORS"}
	fileName := GetFileName(filePath)
	for _, docFile := range docFiles {
		if strings.EqualFold(fileName, docFile) {
			return true
		}
	}

	// Директории документации
	docDirs := []string{"/docs/", "/doc/", "/documentation/"}
	for _, dir := range docDirs {
		if strings.Contains(filePath, dir) {
			return true
		}
	}

	return false
}

// IsTestFile проверяет является ли файл тестовым
// CANONICAL VERSION - заменяет дублированные функции в BaseValidator и BaseAdvisor
func IsTestFile(filePath string) bool {
	// Go тестовые файлы
	if strings.HasSuffix(filePath, "_test.go") {
		return true
	}

	// Тестовые директории
	testDirs := []string{"/test/", "/tests/", "/testing/"}
	for _, dir := range testDirs {
		if strings.Contains(filePath, dir) {
			return true
		}
	}

	// TypeScript/JavaScript тестовые файлы
	testPatterns := []string{
		".test.ts", ".test.js", ".test.tsx", ".test.jsx",
		".spec.ts", ".spec.js", ".spec.tsx", ".spec.jsx",
	}

	for _, pattern := range testPatterns {
		if strings.HasSuffix(filePath, pattern) {
			return true
		}
	}

	return false
}

// GetFileName извлекает имя файла без расширения
// CANONICAL VERSION - заменяет дублированные функции в BaseValidator и BaseAdvisor
func GetFileName(filePath string) string {
	parts := strings.Split(filePath, "/")
	if len(parts) == 0 {
		return ""
	}

	fileName := parts[len(parts)-1]
	dotIndex := strings.LastIndex(fileName, ".")
	if dotIndex > 0 {
		return fileName[:dotIndex]
	}

	return fileName
}

// IsSupportedFileType проверяет поддерживается ли тип файла
// CANONICAL VERSION - заменяет дублированные функции в BaseValidator, BaseAdvisor, BaseTool
func IsSupportedFileType(filePath string, supportedExtensions []string) bool {
	for _, ext := range supportedExtensions {
		if strings.HasSuffix(strings.ToLower(filePath), ext) {
			return true
		}
	}
	return false
}
