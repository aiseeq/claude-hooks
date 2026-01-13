BINARY_NAME=claude-hooks
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DIR=bin
GO_FILES=$(shell find . -name '*.go' -not -path './vendor/*')

LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

.PHONY: all build install uninstall test clean help

all: build

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/claude-hooks

install: build ## Install to ~/bin and config to ~/.claude/hooks
	@mkdir -p $(HOME)/bin
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(HOME)/bin/
	@mkdir -p $(HOME)/.claude/hooks
	@mkdir -p $(HOME)/.claude/logs
	@cp configs/hooks.yaml $(HOME)/.claude/hooks/config.yaml
	@echo "Installed $(BINARY_NAME) to $(HOME)/bin/"
	@echo "Config at $(HOME)/.claude/hooks/config.yaml"
	@echo ""
	@echo "Add to ~/.claude/settings.json:"
	@echo '  "hooks": {'
	@echo '    "PreToolUse": ['
	@echo '      {"matcher": "Write|Edit|MultiEdit", "hooks": [{"type": "command", "command": "$$HOME/bin/claude-hooks pre-tool-use", "timeout": 5000}]},'
	@echo '      {"matcher": "Bash", "hooks": [{"type": "command", "command": "$$HOME/bin/claude-hooks pre-tool-use", "timeout": 3000}]}'
	@echo '    ],'
	@echo '    "PostToolUse": ['
	@echo '      {"matcher": "Write|Edit|MultiEdit", "hooks": [{"type": "command", "command": "$$HOME/bin/claude-hooks post-tool-use", "timeout": 5000}]}'
	@echo '    ],'
	@echo '    "Stop": ['
	@echo '      {"matcher": "", "hooks": [{"type": "command", "command": "$$HOME/bin/claude-hooks stop", "timeout": 3000}]}'
	@echo '    ]'
	@echo '  }'

uninstall: ## Remove installed files
	rm -f $(HOME)/bin/$(BINARY_NAME)
	rm -f $(HOME)/.claude/hooks/config.yaml
	@echo "Uninstalled $(BINARY_NAME)"

test: ## Run all tests
	go test -v ./...

test-integration: build ## Run integration tests
	@./scripts/test-integration.sh

clean: ## Clean build artifacts
	rm -rf $(BUILD_DIR)
	go clean

fmt: ## Format code
	gofmt -w $(GO_FILES)

lint: ## Run linter
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed"; \
	fi

version: ## Show version
	@echo $(VERSION)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'
