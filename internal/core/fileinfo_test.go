package core

import "testing"

func TestMatchPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		pattern  string
		expected bool
	}{
		{name: "glob по имени файла", path: "internal/service_test.go", pattern: "*_test.go", expected: true},
		{name: "glob не совпадает", path: "internal/service.go", pattern: "*_test.go", expected: false},
		{name: "подстрока пути", path: "/home/user/cmd/app/main.go", pattern: "/cmd/", expected: true},
		{name: "glob по полному пути", path: "src/api/generated.go", pattern: "src/*/generated.go", expected: true},
		{name: "пустой паттерн", path: "main.go", pattern: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchPath(tt.path, tt.pattern); got != tt.expected {
				t.Errorf("MatchPath(%q, %q) = %v, ожидалось %v", tt.path, tt.pattern, got, tt.expected)
			}
		})
	}
}

func TestIsExceptionFile(t *testing.T) {
	logger := NewTestLogger()
	exceptions := []string{"*.generated.go", "/vendor/"}

	tests := []struct {
		path     string
		expected bool
	}{
		{path: "internal/service.go", expected: false},
		{path: "internal/api.generated.go", expected: true},
		{path: "project/vendor/lib/file.go", expected: true},
		{path: "internal/service_test.go", expected: true}, // тесты исключаются всегда
		{path: "README.md", expected: true},                // документация исключается всегда
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsExceptionFile(tt.path, exceptions, logger); got != tt.expected {
				t.Errorf("IsExceptionFile(%q) = %v, ожидалось %v", tt.path, got, tt.expected)
			}
		})
	}
}

func TestIsTestFile(t *testing.T) {
	tests := map[string]bool{
		"internal/service_test.go": true,
		"src/tests/helper.go":      true,
		"src/app.spec.ts":          true,
		"src/app.test.tsx":         true,
		"internal/service.go":      false,
		"src/latest.go":            false,
	}

	for path, expected := range tests {
		t.Run(path, func(t *testing.T) {
			if got := IsTestFile(path); got != expected {
				t.Errorf("IsTestFile(%q) = %v, ожидалось %v", path, got, expected)
			}
		})
	}
}

func TestGetFileNameAndExtension(t *testing.T) {
	if got := GetFileName("/home/user/project/config.yaml"); got != "config" {
		t.Errorf("GetFileName = %q, ожидалось \"config\"", got)
	}
	if got := GetFileName("/home/user/.gitignore"); got != ".gitignore" {
		t.Errorf("GetFileName для скрытого файла = %q", got)
	}
	if got := GetFileExtension("/home/user/App.TSX"); got != ".tsx" {
		t.Errorf("GetFileExtension = %q, ожидалось \".tsx\"", got)
	}
	if got := GetFileExtension("Makefile"); got != "" {
		t.Errorf("GetFileExtension для файла без расширения = %q", got)
	}
}
