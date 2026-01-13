package validators

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func TestRuntimeExitValidator_BlocksDangerousCalls(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled: true,
	}

	validator, err := NewRuntimeExitValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "blocks os.Exit",
			content:   "os.Exit(1)",
			wantBlock: true,
		},
		{
			name:      "blocks log.Fatal",
			content:   "log.Fatal(\"error\")",
			wantBlock: true,
		},
		{
			name:      "blocks log.Fatalf",
			content:   "log.Fatalf(\"error: %v\", err)",
			wantBlock: true,
		},
		{
			name:      "blocks panic",
			content:   "panic(\"something went wrong\")",
			wantBlock: true,
		},
		{
			name:      "allows normal error handling",
			content:   "return fmt.Errorf(\"error: %w\", err)",
			wantBlock: false,
		},
		{
			name:      "allows logging without fatal",
			content:   "log.Error(\"something failed\")",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &core.FileAnalysis{
				Path:    "internal/service.go",
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

func TestRuntimeExitValidator_AllowsInMain(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled:        true,
		ExceptionPaths: []string{"cmd/", "main.go"},
	}

	validator, err := NewRuntimeExitValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name      string
		path      string
		wantBlock bool
	}{
		{
			name:      "allows in cmd directory",
			path:      "cmd/myapp/main.go",
			wantBlock: false,
		},
		{
			name:      "allows in main.go",
			path:      "main.go",
			wantBlock: false,
		},
		{
			name:      "blocks in internal",
			path:      "internal/service.go",
			wantBlock: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &core.FileAnalysis{
				Path:    tt.path,
				Content: "os.Exit(1)",
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

func TestRuntimeExitValidator_AllowsInTests(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ValidatorConfig{
		Enabled:        true,
		TestExceptions: []string{"*_test.go"},
	}

	validator, err := NewRuntimeExitValidator(config, logger)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	file := &core.FileAnalysis{
		Path:    "internal/service_test.go",
		Content: "panic(\"test failure\")",
	}

	result, err := validator.Validate(context.Background(), file)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("should allow panic in test files")
	}
}
