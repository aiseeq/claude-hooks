package core

import (
	"os"
	"path/filepath"
	"strings"
)

// MatchPath проверяет соответствует ли путь паттерну исключения.
// Поддерживаются три формы записи:
//   - glob по имени файла: "*_test.go", "test-config.*"
//   - glob по всему пути: "internal/*/generated.go"
//   - подстрока пути: "docs/", "/cmd/"
func MatchPath(filePath, pattern string) bool {
	if pattern == "" {
		return false
	}

	// Glob по имени файла — самая частая форма в конфигурации
	if ok, err := filepath.Match(pattern, filepath.Base(filePath)); err == nil && ok {
		return true
	}

	// Glob по полному пути
	if ok, err := filepath.Match(pattern, filePath); err == nil && ok {
		return true
	}

	// Подстрока пути
	return strings.Contains(filePath, pattern)
}

// MatchAnyPath проверяет соответствие пути хотя бы одному паттерну
func MatchAnyPath(filePath string, patterns []string) (string, bool) {
	for _, pattern := range patterns {
		if MatchPath(filePath, pattern) {
			return pattern, true
		}
	}
	return "", false
}

// IsExceptionFile проверяет является ли файл исключением: по паттернам из
// конфигурации, а также по признакам документации и тестового файла
func IsExceptionFile(filePath string, exceptions []string, logger Logger) bool {
	if pattern, ok := MatchAnyPath(filePath, exceptions); ok {
		logger.Debug("file matched exception pattern", "file", filePath, "pattern", pattern)
		return true
	}

	if IsDocumentationFile(filePath) {
		logger.Debug("file is documentation", "file", filePath)
		return true
	}

	if IsTestFile(filePath) {
		logger.Debug("file is test file", "file", filePath)
		return true
	}

	return false
}

// IsDocumentationFile проверяет является ли файл документацией
func IsDocumentationFile(filePath string) bool {
	docExtensions := []string{".md", ".txt", ".rst", ".adoc"}
	if IsSupportedFileType(filePath, docExtensions) {
		return true
	}

	docFiles := []string{"README", "CHANGELOG", "LICENSE", "AUTHORS", "CONTRIBUTORS"}
	fileName := GetFileName(filePath)
	for _, docFile := range docFiles {
		if strings.EqualFold(fileName, docFile) {
			return true
		}
	}

	docDirs := []string{"/docs/", "/doc/", "/documentation/"}
	for _, dir := range docDirs {
		if strings.Contains(filePath, dir) {
			return true
		}
	}

	return false
}

// IsTestFile проверяет является ли файл тестовым
func IsTestFile(filePath string) bool {
	if strings.HasSuffix(filePath, "_test.go") {
		return true
	}

	testDirs := []string{"/test/", "/tests/", "/testing/"}
	for _, dir := range testDirs {
		if strings.Contains(filePath, dir) {
			return true
		}
	}

	testPatterns := []string{
		".test.ts", ".test.js", ".test.tsx", ".test.jsx",
		".spec.ts", ".spec.js", ".spec.tsx", ".spec.jsx",
	}
	return IsSupportedFileType(filePath, testPatterns)
}

// ProjectNameForDir возвращает имя проекта по его каталогу.
//
// Одного последнего сегмента не всегда достаточно: ~/work/saga/backend и
// ~/work/glint/backend дали бы одинаковое имя. Поэтому у вложенных проектов
// показывается и родительский каталог. Домашний каталог и корень своего имени
// не имеют — для них возвращаются понятные обозначения, а не имя пользователя
func ProjectNameForDir(dir string) string {
	dir = filepath.Clean(dir)

	if dir == string(filepath.Separator) {
		return "/"
	}

	if home, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(home)
		if dir == home {
			return "~"
		}

		// Внутри домашнего каталога первый уровень — папка вроде work или git,
		// имени проекта она не добавляет
		if relative, relErr := filepath.Rel(home, dir); relErr == nil && !strings.HasPrefix(relative, "..") {
			segments := strings.Split(relative, string(filepath.Separator))
			if len(segments) >= 3 {
				return filepath.Join(segments[len(segments)-2], segments[len(segments)-1])
			}
		}
	}

	if base := filepath.Base(dir); base != "." && base != string(filepath.Separator) {
		return base
	}
	return "unknown"
}

// GetFileName извлекает имя файла без расширения
func GetFileName(filePath string) string {
	fileName := filepath.Base(filePath)
	if dotIndex := strings.LastIndex(fileName, "."); dotIndex > 0 {
		return fileName[:dotIndex]
	}
	return fileName
}

// GetFileExtension извлекает расширение файла вместе с точкой (".go")
func GetFileExtension(filePath string) string {
	return strings.ToLower(filepath.Ext(filePath))
}

// IsSupportedFileType проверяет входит ли расширение файла в список поддерживаемых
func IsSupportedFileType(filePath string, supportedExtensions []string) bool {
	lower := strings.ToLower(filePath)
	for _, ext := range supportedExtensions {
		if strings.HasSuffix(lower, strings.ToLower(ext)) {
			return true
		}
	}
	return false
}
