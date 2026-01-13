package validators

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func TestEmergencyDefaultsValidator_BlocksFallback(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled: true,
	}

	validator, err := NewEmergencyDefaultsValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "blocks fallback keyword",
			content:   "func getFallbackValue() string { return \"test\" }",
			wantBlock: true,
		},
		{
			name:      "blocks Fallback capitalized",
			content:   "var FallbackHandler = func() {}",
			wantBlock: true,
		},
		{
			name:      "allows valid code",
			content:   "func getValue() string { return \"test\" }",
			wantBlock: false,
		},
		{
			name:      "allows default case in switch",
			content:   "switch x { case 1: break; default: break }",
			wantBlock: false,
		},
		{
			name:      "allows fallback in comments",
			content:   "// fallback to default value if empty",
			wantBlock: false,
		},
		{
			name:      "warns but does not block || pattern",
			content:   "value := x || \"default\"",
			wantBlock: false,
		},
		{
			name:      "warns but does not block ?? pattern",
			content:   "const value = x ?? \"default\"",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &core.FileAnalysis{
				Path:    "test.go",
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

func TestEmergencyDefaultsValidator_Disabled(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled: false,
	}

	validator, err := NewEmergencyDefaultsValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	file := &core.FileAnalysis{
		Path:    "test.go",
		Content: "func getFallbackValue() {}",
	}

	result, err := validator.Validate(context.Background(), file)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("disabled validator should not block")
	}
}

func TestEmergencyDefaultsValidator_ExceptionFiles(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled:        true,
		ExceptionPaths: []string{"docs/", "test/"},
	}

	validator, err := NewEmergencyDefaultsValidator(config, logger)
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
			path:      "src/handler.go",
			wantBlock: true,
		},
		{
			name:      "allows in docs directory",
			path:      "docs/readme.go",
			wantBlock: false,
		},
		{
			name:      "allows in test directory",
			path:      "test/test_helper.go",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &core.FileAnalysis{
				Path:    tt.path,
				Content: "func useFallback() {}",
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
