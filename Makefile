BINARY_NAME=claude-hooks
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DIR=bin
INSTALL_DIR=$(HOME)/.claude/hooks
CONFIG_FILE=$(INSTALL_DIR)/config.yaml

LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

.PHONY: all build install uninstall test test-race cover fmt lint version help smoke commit

all: build

build: ## Собрать бинарь в bin/
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/claude-hooks

install: build ## Установить бинарь и конфигурацию в ~/.claude/hooks
	@mkdir -p $(INSTALL_DIR) $(HOME)/.claude/logs
	@# Замена через переименование: прямая перезапись падает с "Text file busy",
	@# пока работает фоновый процесс уведомления
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME).new
	@mv -f $(INSTALL_DIR)/$(BINARY_NAME).new $(INSTALL_DIR)/$(BINARY_NAME)
	@if [ -f $(CONFIG_FILE) ]; then \
		cp configs/hooks.yaml $(CONFIG_FILE).new; \
		echo "Конфигурация сохранена: существующий $(CONFIG_FILE) не изменён"; \
		echo "Новая версия: $(CONFIG_FILE).new"; \
	else \
		cp configs/hooks.yaml $(CONFIG_FILE); \
		echo "Конфигурация создана: $(CONFIG_FILE)"; \
	fi
	@echo "Бинарь установлен: $(INSTALL_DIR)/$(BINARY_NAME)"
	@echo ""
	@echo "Добавь блок hooks из configs/settings-snippet.json в ~/.claude/settings.json"

uninstall: ## Удалить установленные файлы
	rm -f $(INSTALL_DIR)/$(BINARY_NAME)
	@echo "Удалён $(INSTALL_DIR)/$(BINARY_NAME) (конфигурация оставлена)"

test: ## Запустить тесты
	go test ./...

test-race: ## Запустить тесты с детектором гонок
	go test -race ./...

cover: ## Показать покрытие тестами
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt: ## Отформатировать код
	gofmt -w .

lint: ## Проверить код линтером
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint не установлен, выполнен только go vet"; \
	fi

clean: ## Удалить артефакты сборки
	rm -rf $(BUILD_DIR) coverage.out

version: ## Показать версию
	@echo $(VERSION)

smoke: ## Быстрая проверка перед коммитом: сборка и тесты
	go build ./...
	go test ./...

# MESSAGE идёт в рецепт через окружение, а не через $(MESSAGE): подстановка make
# разрывает рецепт на первом переносе строки в тексте коммита
export MESSAGE

commit: smoke ## Закоммитить всё и запушить (make commit MESSAGE="...")
	@if [ -z "$$MESSAGE" ]; then \
		echo "Использование: make commit MESSAGE=\"сообщение коммита\""; \
		exit 1; \
	fi
	@git add -A
	@printf '%s\n\n%s\n\n%s\n' \
		"$$MESSAGE" \
		"🤖 Generated with Claude Code" \
		"Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>" \
		| git commit -F -
	@echo "Committed: $$(printf '%s' "$$MESSAGE" | head -1)"
	@git remote | xargs -I% git push % HEAD

help: ## Показать список целей
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'
