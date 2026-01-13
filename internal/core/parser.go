package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolInput парсит JSON входные данные от Claude Code
func ParseToolInput(data []byte) (*ToolInput, error) {
	var input ToolInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("failed to parse tool input: %w", err)
	}

	// Извлекаем специфичные для инструмента данные
	if err := extractToolSpecificData(&input); err != nil {
		return nil, fmt.Errorf("failed to extract tool specific data: %w", err)
	}

	return &input, nil
}

// extractToolSpecificData извлекает данные специфичные для каждого типа инструмента
func extractToolSpecificData(input *ToolInput) error {
	// Если ToolInput пустой или nil, просто возвращаем без ошибки
	if len(input.ToolInput) == 0 {
		return nil
	}

	// Сначала пробуем unmarshal в string (если tool_input содержит JSON строку)
	var toolInputStr string
	var toolData map[string]any

	// Пробуем сначала unmarshal в string
	if err := json.Unmarshal(input.ToolInput, &toolInputStr); err == nil {
		// Если это строка, то unmarshal строку в map
		if err := json.Unmarshal([]byte(toolInputStr), &toolData); err != nil {
			return nil
		}
	} else {
		// Если не строка, пробуем напрямую unmarshal в map
		if err := json.Unmarshal(input.ToolInput, &toolData); err != nil {
			return nil
		}
	}

	switch input.ToolName {
	case "Write":
		if filePath, ok := toolData["file_path"].(string); ok {
			input.FilePath = filePath
		}
		if content, ok := toolData["content"].(string); ok {
			input.Content = content
		}

	case "Edit":
		if filePath, ok := toolData["file_path"].(string); ok {
			input.FilePath = filePath
		}
		if newString, ok := toolData["new_string"].(string); ok {
			input.NewString = newString
		}

	case "MultiEdit":
		if filePath, ok := toolData["file_path"].(string); ok {
			input.FilePath = filePath
		}
		// Для MultiEdit объединяем все new_string из массива edits
		if edits, ok := toolData["edits"].([]any); ok {
			var allNewStrings []string
			for _, edit := range edits {
				if editMap, ok := edit.(map[string]any); ok {
					if newString, ok := editMap["new_string"].(string); ok {
						allNewStrings = append(allNewStrings, newString)
					}
				}
			}
			input.NewString = strings.Join(allNewStrings, " ")
		}

	case "Bash":
		if command, ok := toolData["command"].(string); ok {
			input.Command = command
		}
	}

	return nil
}

// CreateFileAnalysis создает анализ файла из ToolInput
func CreateFileAnalysis(input *ToolInput) *FileAnalysis {
	if input.FilePath == "" {
		return nil
	}

	analysis := &FileAnalysis{
		Path:      input.FilePath,
		Extension: getFileExtension(input.FilePath),
	}

	// Определяем содержимое для анализа
	if input.Content != "" {
		analysis.Content = input.Content
	} else if input.NewString != "" {
		analysis.Content = input.NewString
	}

	// Определяем тип файла
	analysis.IsTestFile = isTestFile(input.FilePath)
	analysis.IsDocsFile = isDocumentationFile(input.FilePath)

	return analysis
}

// getFileExtension извлекает расширение файла
func getFileExtension(filePath string) string {
	parts := strings.Split(filePath, ".")
	if len(parts) > 1 {
		return "." + parts[len(parts)-1]
	}
	return ""
}

// isTestFile определяет является ли файл тестовым
func isTestFile(filePath string) bool {
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

// isDocumentationFile определяет является ли файл документацией
func isDocumentationFile(filePath string) bool {
	// Документация по расширению
	docExtensions := []string{".md", ".txt", ".rst", ".adoc"}
	for _, ext := range docExtensions {
		if strings.HasSuffix(strings.ToLower(filePath), ext) {
			return true
		}
	}

	// Специальные файлы документации
	docFiles := []string{"README", "CHANGELOG", "LICENSE", "AUTHORS", "CONTRIBUTORS"}
	fileName := getFileName(filePath)
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

// getFileName извлекает имя файла без расширения
func getFileName(filePath string) string {
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
