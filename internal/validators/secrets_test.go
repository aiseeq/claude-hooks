package validators

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func TestSecretsValidator_BlocksJWT(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled: true,
	}

	validator, err := NewSecretsValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "blocks JWT token",
			content:   `token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0"`,
			wantBlock: true,
		},
		{
			name:      "blocks wallet address",
			content:   `wallet := "0x1234567890abcdef1234567890abcdef12345678"`,
			wantBlock: true,
		},
		{
			name:      "allows normal strings",
			content:   `name := "John Doe"`,
			wantBlock: false,
		},
		{
			name:      "allows environment variables",
			content:   `token := os.Getenv("JWT_TOKEN")`,
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &core.FileAnalysis{
				Path:    "config.go",
				Content: tt.content,
			}

			result, err := validator.Validate(context.Background(), file)
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if tt.wantBlock && result.IsValid {
				t.Errorf("expected block but got pass")
			}
			if !tt.wantBlock && !result.IsValid {
				t.Errorf("expected pass but got block")
			}
		})
	}
}

func TestSecretsValidator_AllowsTestFiles(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled:              true,
		TestConfigExceptions: []string{"*_test.go", "test-config.json"},
	}

	validator, err := NewSecretsValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name      string
		path      string
		wantBlock bool
	}{
		{
			name:      "blocks in regular file",
			path:      "config.go",
			wantBlock: true,
		},
		{
			name:      "allows in test file",
			path:      "config_test.go",
			wantBlock: false,
		},
		{
			name:      "allows in test config",
			path:      "test-config.json",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &core.FileAnalysis{
				Path:    tt.path,
				Content: `token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"`,
			}

			result, err := validator.Validate(context.Background(), file)
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if tt.wantBlock && result.IsValid {
				t.Errorf("expected block but got pass")
			}
			if !tt.wantBlock && !result.IsValid {
				t.Errorf("expected pass but got block")
			}
		})
	}
}

func TestSecretsValidator_Disabled(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled: false,
	}

	validator, err := NewSecretsValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	file := &core.FileAnalysis{
		Path:    "config.go",
		Content: `token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"`,
	}

	result, err := validator.Validate(context.Background(), file)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("disabled validator should not block")
	}
}
