package validators

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newRuntimeExitValidator(t *testing.T, config core.ValidatorConfig) *RuntimeExitValidator {
	t.Helper()

	validator, err := NewRuntimeExitValidator(config, core.NewTestLogger())
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}
	return validator
}

func TestRuntimeExitValidator_BlocksDangerousCalls(t *testing.T) {
	validator := newRuntimeExitValidator(t, core.ValidatorConfig{Enabled: true})

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{name: "os.Exit", content: "os.Exit(1)", wantBlock: true},
		{name: "log.Fatal", content: "log.Fatal(\"error\")", wantBlock: true},
		{name: "log.Fatalf", content: "log.Fatalf(\"error: %v\", err)", wantBlock: true},
		{name: "panic", content: "panic(\"something went wrong\")", wantBlock: true},
		{name: "возврат ошибки", content: "return fmt.Errorf(\"error: %w\", err)", wantBlock: false},
		{name: "обычное логирование", content: "log.Error(\"something failed\")", wantBlock: false},
		{name: "слово panic внутри идентификатора", content: "recoverFromPanicHandler()", wantBlock: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := validator.Validate(context.Background(), &core.FileAnalysis{
				Path:    "internal/service.go",
				Content: tt.content,
			})
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if result.IsValid == tt.wantBlock {
				t.Errorf("wantBlock=%v, получено IsValid=%v", tt.wantBlock, result.IsValid)
			}
		})
	}
}

func TestRuntimeExitValidator_Exceptions(t *testing.T) {
	validator := newRuntimeExitValidator(t, core.ValidatorConfig{
		Enabled:    true,
		Exceptions: []string{"cmd/", "main.go"},
	})

	tests := []struct {
		name      string
		path      string
		wantBlock bool
	}{
		{name: "каталог cmd", path: "cmd/myapp/main.go", wantBlock: false},
		{name: "main.go в корне", path: "main.go", wantBlock: false},
		{name: "тестовый файл", path: "internal/service_test.go", wantBlock: false},
		{name: "библиотечный код", path: "internal/service.go", wantBlock: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := validator.Validate(context.Background(), &core.FileAnalysis{
				Path:    tt.path,
				Content: "os.Exit(1)",
			})
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if result.IsValid == tt.wantBlock {
				t.Errorf("wantBlock=%v, получено IsValid=%v", tt.wantBlock, result.IsValid)
			}
		})
	}
}

// Упоминание конструкции в комментарии или в тексте сообщения — не её использование
func TestRuntimeExitValidator_IgnoresCommentsAndStrings(t *testing.T) {
	validator := newRuntimeExitValidator(t, core.ValidatorConfig{Enabled: true})

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "однострочный комментарий",
			content:   "// не используй panic() в библиотечном коде\nreturn nil",
			wantBlock: false,
		},
		{
			name:      "многострочный комментарий",
			content:   "/*\n\tos.Exit(1) здесь недопустим\n*/\nreturn nil",
			wantBlock: false,
		},
		{
			name:      "текст сообщения об ошибке",
			content:   `return errors.New("вызов log.Fatal( запрещён")`,
			wantBlock: false,
		},
		{
			name:      "код после комментария",
			content:   "// комментарий про panic(\nos.Exit(1)",
			wantBlock: true,
		},
		{
			name:      "код и комментарий в одной строке",
			content:   "os.Exit(1) // выход",
			wantBlock: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := validator.Validate(context.Background(), &core.FileAnalysis{
				Path:    "internal/service.go",
				Content: tt.content,
			})
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if result.IsValid == tt.wantBlock {
				t.Errorf("wantBlock=%v, получено IsValid=%v (нарушения: %+v)", tt.wantBlock, result.IsValid, result.Violations)
			}
		})
	}
}

func TestRuntimeExitValidator_ReportsPosition(t *testing.T) {
	validator := newRuntimeExitValidator(t, core.ValidatorConfig{Enabled: true})

	result, err := validator.Validate(context.Background(), &core.FileAnalysis{
		Path:    "internal/service.go",
		Content: "package service\n\n// комментарий\n\tos.Exit(1)\n",
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if len(result.Violations) != 1 {
		t.Fatalf("ожидалось одно нарушение, получено %d", len(result.Violations))
	}
	if got := result.Violations[0].Line; got != 4 {
		t.Errorf("ожидалась строка 4, получена %d", got)
	}
	// Очистка комментариев не должна смещать колонки
	if got := result.Violations[0].Column; got != 2 {
		t.Errorf("ожидалась колонка 2, получена %d", got)
	}
}

func TestRuntimeExitValidator_GoFilesOnly(t *testing.T) {
	validator := newRuntimeExitValidator(t, core.ValidatorConfig{Enabled: true, GoFilesOnly: true})

	result, err := validator.Validate(context.Background(), &core.FileAnalysis{
		Path:    "internal/service.py",
		Content: "os.Exit(1)",
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if !result.IsValid {
		t.Error("при go_files_only проверяются только Go-файлы")
	}
}

func TestRuntimeExitValidator_Disabled(t *testing.T) {
	validator := newRuntimeExitValidator(t, core.ValidatorConfig{Enabled: false})

	result, err := validator.Validate(context.Background(), &core.FileAnalysis{
		Path:    "internal/service.go",
		Content: "os.Exit(1)",
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if !result.IsValid {
		t.Error("выключенный валидатор не должен блокировать")
	}
}
