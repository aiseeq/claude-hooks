package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("не удалось создать конфигурацию: %v", err)
	}
	return path
}

func TestLoadConfig_ParsesValidators(t *testing.T) {
	path := writeConfig(t, `
validators:
  secrets:
    enabled: true
    exceptions:
      - "*_test.go"
tools:
  bash:
    enabled: true
    blocked_patterns:
      - "rm -rf /"
logger:
  level: "debug"
  output: "stderr"
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("не удалось загрузить конфигурацию: %v", err)
	}

	secrets, exists := config.Validators["secrets"]
	if !exists {
		t.Fatal("валидатор secrets отсутствует")
	}
	if !secrets.Enabled {
		t.Error("валидатор secrets должен быть включён")
	}
	if len(secrets.Exceptions) != 1 || secrets.Exceptions[0] != "*_test.go" {
		t.Errorf("исключения не загружены: %+v", secrets.Exceptions)
	}
	if config.Logger.Level != "debug" {
		t.Errorf("ожидался уровень debug, получен %s", config.Logger.Level)
	}
}

// Опечатка в имени поля раньше молча отключала проверку
func TestLoadConfig_RejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `
validators:
  secrets:
    enabled: true
    exception_paths:
      - "*_test.go"
`)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("неизвестное поле должно приводить к ошибке")
	}
	if !strings.Contains(err.Error(), "exception_paths") {
		t.Errorf("ошибка должна называть проблемное поле, получено: %v", err)
	}
}

func TestLoadConfig_MissingFileUsesDefaults(t *testing.T) {
	config, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("отсутствие файла не должно быть ошибкой: %v", err)
	}
	if !config.Validators["secrets"].Enabled {
		t.Error("конфигурация по умолчанию должна включать валидатор secrets")
	}
}

// Чтение конфигурации не должно ничего записывать: хук запускается часто и параллельно
func TestLoadConfig_DoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")

	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("не удалось загрузить конфигурацию: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("файл конфигурации не должен создаваться при чтении")
	}
}

func TestLoadConfig_LoggerDefaults(t *testing.T) {
	path := writeConfig(t, "validators:\n  secrets:\n    enabled: true\n")

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("не удалось загрузить конфигурацию: %v", err)
	}

	if config.Logger.Level == "" || config.Logger.Output == "" || config.Logger.Format == "" {
		t.Errorf("незаполненные поля логгера должны получать значения по умолчанию: %+v", config.Logger)
	}
	if config.Logger.MaxSizeMB <= 0 {
		t.Error("ротация лога должна быть включена по умолчанию")
	}
}

func TestLoadConfig_InvalidLoggerLevel(t *testing.T) {
	path := writeConfig(t, "logger:\n  level: \"trace\"\n")

	if _, err := LoadConfig(path); err == nil {
		t.Error("некорректный уровень логирования должен приводить к ошибке")
	}
}

func TestLoadConfig_RequiresLogFileForFileOutput(t *testing.T) {
	path := writeConfig(t, "logger:\n  output: \"file\"\n")

	if _, err := LoadConfig(path); err == nil {
		t.Error("output: file без указания пути должен приводить к ошибке")
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")

	if err := SaveConfig(DefaultConfig(), path); err != nil {
		t.Fatalf("не удалось сохранить конфигурацию: %v", err)
	}

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("сохранённая конфигурация должна читаться: %v", err)
	}
	if len(config.Validators) != len(DefaultConfig().Validators) {
		t.Error("состав валидаторов изменился при сохранении")
	}
}
