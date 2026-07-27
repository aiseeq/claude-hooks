package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config основная конфигурация хуков
type Config struct {
	Validators map[string]ValidatorConfig `yaml:"validators"`
	Tools      map[string]ToolConfig      `yaml:"tools"`
	Logger     LoggerConfig               `yaml:"logger"`
}

// ValidatorConfig конфигурация валидатора.
// Exceptions поддерживает glob-паттерны ("*_test.go") и подстроки пути ("/cmd/")
type ValidatorConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Exceptions []string `yaml:"exceptions"`

	// Специфичные для runtime_exit
	GoFilesOnly bool `yaml:"go_files_only"`

	// Специфичные для secrets
	JWTPattern    string `yaml:"jwt_pattern"`
	WalletPattern string `yaml:"wallet_pattern"`
	APIKeyPattern string `yaml:"api_key_pattern"`
}

// ToolConfig конфигурация инструмента
type ToolConfig struct {
	Enabled bool `yaml:"enabled"`

	// Специфичные для bash
	BlockedPatterns []string `yaml:"blocked_patterns"`

	// Специфичные для formatter
	GoFormat bool `yaml:"go_format"`
	TSFormat bool `yaml:"ts_format"`

	// Специфичные для notifier
	Sound   bool `yaml:"sound"`
	Desktop bool `yaml:"desktop"`
	// ActivateOnClick переводит фокус на окно терминала по клику на уведомление (KDE)
	ActivateOnClick bool `yaml:"activate_on_click"`
}

// LoadConfig загружает конфигурацию из файла.
// Если файл отсутствует, возвращается конфигурация по умолчанию без записи на диск:
// хук вызывается часто и параллельно, поэтому побочные эффекты здесь недопустимы
func LoadConfig(configPath string) (*Config, error) {
	if configPath == "" {
		configPath = DefaultConfigPath()
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Неизвестные ключи — ошибка: молча проигнорированная опечатка в имени поля
	// означает незаметно отключённую проверку
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// io.EOF означает пустой файл — работаем на значениях по умолчанию
	if err := decoder.Decode(&config); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	applyLoggerDefaults(&config.Logger)
	expandConfigPaths(&config)

	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed in %s: %w", configPath, err)
	}

	return &config, nil
}

// SaveConfig сохраняет конфигурацию в файл
func SaveConfig(config *Config, configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// DefaultConfig возвращает конфигурацию по умолчанию
func DefaultConfig() *Config {
	return &Config{
		Validators: map[string]ValidatorConfig{
			"runtime_exit": {
				Enabled:     true,
				GoFilesOnly: true,
				Exceptions:  []string{"*_test.go", "/cmd/", "main.go"},
			},
			"secrets": {
				Enabled:    true,
				Exceptions: []string{"*_test.go", "test-config.*"},
			},
		},
		Tools: map[string]ToolConfig{
			"bash": {
				Enabled: true,
			},
			"formatter": {
				Enabled:  true,
				GoFormat: true,
				TSFormat: true,
			},
			"notifier": {
				Enabled:         true,
				Sound:           true,
				Desktop:         true,
				ActivateOnClick: true,
			},
		},
		Logger: DefaultLoggerConfig(),
	}
}

// validateConfig проверяет корректность конфигурации
func validateConfig(config *Config) error {
	validLevels := []string{"debug", "info", "warn", "warning", "error"}
	if !containsFold(validLevels, config.Logger.Level) {
		return fmt.Errorf("invalid logger level: %q (expected one of %s)",
			config.Logger.Level, strings.Join(validLevels, ", "))
	}

	validFormats := []string{"text", "json"}
	if !containsFold(validFormats, config.Logger.Format) {
		return fmt.Errorf("invalid logger format: %q (expected one of %s)",
			config.Logger.Format, strings.Join(validFormats, ", "))
	}

	validOutputs := []string{"stdout", "stderr", "file"}
	if !containsFold(validOutputs, config.Logger.Output) {
		return fmt.Errorf("invalid logger output: %q (expected one of %s)",
			config.Logger.Output, strings.Join(validOutputs, ", "))
	}

	if config.Logger.Output == "file" && config.Logger.LogFile == "" {
		return fmt.Errorf("logger.file is required when output is 'file'")
	}

	if config.Logger.MaxSizeMB < 0 {
		return fmt.Errorf("logger.max_size_mb must not be negative, got %d", config.Logger.MaxSizeMB)
	}

	return nil
}

// DefaultConfigPath возвращает путь к конфигурации по умолчанию
func DefaultConfigPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".claude", "hooks", "config.yaml")
	}
	return filepath.Join(homeDir, ".claude", "hooks", "config.yaml")
}

// containsFold проверяет содержится ли элемент в слайсе без учета регистра
func containsFold(slice []string, item string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, item) {
			return true
		}
	}
	return false
}

// expandPath раскрывает ~ в пути к домашней директории
func expandPath(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(homeDir, path[2:])
}

// expandConfigPaths применяет expandPath ко всем путям в конфигурации
func expandConfigPaths(config *Config) {
	config.Logger.LogFile = expandPath(config.Logger.LogFile)
}
