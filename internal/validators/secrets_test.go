package validators

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newSecretsValidator(t *testing.T, config core.ValidatorConfig) *SecretsValidator {
	t.Helper()

	validator, err := NewSecretsValidator(config, core.NewTestLogger())
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}
	return validator
}

func TestSecretsValidator_Detection(t *testing.T) {
	validator := newSecretsValidator(t, core.ValidatorConfig{Enabled: true})

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "JWT токен",
			content:   `token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc"`,
			wantBlock: true,
		},
		{
			name:      "wallet address",
			content:   `wallet := "0x1234567890abcdef1234567890abcdef12345678"`,
			wantBlock: true,
		},
		// Значения намеренно короче и с разделителями внутри: полноформатные ключи
		// в тестовых данных блокируются сканерами секретов при отправке в репозиторий
		{
			name:      "секретный ключ платежного провайдера",
			content:   `key := "sk_live_sample-key-value-0000"`,
			wantBlock: true,
		},
		{
			name:      "токен системы контроля версий",
			content:   `token := "ghp_sample-token-value-0000"`,
			wantBlock: true,
		},
		{
			name:      "обычная строка",
			content:   `name := "John Doe"`,
			wantBlock: false,
		},
		{
			name:      "чтение из переменной окружения",
			content:   `token := os.Getenv("JWT_TOKEN")`,
			wantBlock: false,
		},
		{
			name:      "короткий hex не является адресом",
			content:   `hash := "0xdeadbeef"`,
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := validator.Validate(context.Background(), &core.FileAnalysis{
				Path:    "internal/config.go",
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

func TestSecretsValidator_Exceptions(t *testing.T) {
	validator := newSecretsValidator(t, core.ValidatorConfig{
		Enabled:    true,
		Exceptions: []string{"test-config.*"},
	})

	tests := []struct {
		name      string
		path      string
		wantBlock bool
	}{
		{name: "обычный файл", path: "internal/config.go", wantBlock: true},
		{name: "тестовый файл", path: "internal/config_test.go", wantBlock: false},
		{name: "тестовая конфигурация", path: "internal/test-config.json", wantBlock: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := validator.Validate(context.Background(), &core.FileAnalysis{
				Path:    tt.path,
				Content: `token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.sig"`,
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

func TestSecretsValidator_CustomPattern(t *testing.T) {
	validator := newSecretsValidator(t, core.ValidatorConfig{
		Enabled:       true,
		WalletPattern: `\bZZ[0-9]{6}\b`,
	})

	result, err := validator.Validate(context.Background(), &core.FileAnalysis{
		Path:    "internal/config.go",
		Content: `id := "ZZ123456"`,
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if result.IsValid {
		t.Error("пользовательский паттерн должен срабатывать")
	}
}

func TestSecretsValidator_InvalidPattern(t *testing.T) {
	_, err := NewSecretsValidator(core.ValidatorConfig{
		Enabled:    true,
		JWTPattern: "([a-z",
	}, core.NewTestLogger())

	if err == nil {
		t.Error("некорректный regex должен приводить к ошибке создания валидатора")
	}
}

func TestSecretsValidator_Disabled(t *testing.T) {
	validator := newSecretsValidator(t, core.ValidatorConfig{Enabled: false})

	result, err := validator.Validate(context.Background(), &core.FileAnalysis{
		Path:    "internal/config.go",
		Content: `token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.sig"`,
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if !result.IsValid {
		t.Error("выключенный валидатор не должен блокировать")
	}
}
