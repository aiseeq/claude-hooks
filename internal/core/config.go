package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config основная конфигурация хуков
type Config struct {
	General    GeneralConfig              `yaml:"general"`
	Validators map[string]ValidatorConfig `yaml:"validators"`
	Tools      map[string]ToolConfig      `yaml:"tools"`
	Logger     LoggerConfig               `yaml:"logger"`
}

// GeneralConfig общие настройки
type GeneralConfig struct {
	LogLevel string `yaml:"log_level"`
	LogFile  string `yaml:"log_file"`
	Timeout  int    `yaml:"timeout"`
}

// ValidatorConfig конфигурация валидатора
type ValidatorConfig struct {
	Enabled           bool     `yaml:"enabled"`
	ExceptionPaths    []string `yaml:"exception_paths"`
	ExceptionFiles    []string `yaml:"exception_files"`
	CustomPatterns    []string `yaml:"custom_patterns"`
	SuggestionMessage string   `yaml:"suggestion_message"`

	// Специфичные для emergency_defaults validator
	CaseSensitive bool `yaml:"case_sensitive"`

	// Специфичные для panic validator
	GoFilesOnly     bool     `yaml:"go_files_only"`
	TestExceptions  []string `yaml:"test_exceptions"`
	ProductionPaths []string `yaml:"production_paths"`

	// Специфичные для secrets validator
	JWTPattern           string   `yaml:"jwt_pattern"`
	WalletPattern        string   `yaml:"wallet_pattern"`
	TestConfigExceptions []string `yaml:"test_config_exceptions"`
}

// ToolConfig конфигурация инструмента
type ToolConfig struct {
	Enabled             bool              `yaml:"enabled"`
	DangerousCommands   []string          `yaml:"dangerous_commands"`
	BlockedPatterns     []string          `yaml:"blocked_patterns"`
	Formatters          map[string]string `yaml:"formatters"`
	GoFormat            bool              `yaml:"go_format"`
	TSFormat            bool              `yaml:"ts_format"`
	KDEOnly             bool              `yaml:"kde_only"`
	FlashDuration       int               `yaml:"flash_duration"`
	WorkDir             string            `yaml:"work_dir"`
	Sound               bool              `yaml:"sound"`
	Desktop             bool              `yaml:"desktop"`
}

// LoadConfig загружает конфигурацию из файла
func LoadConfig(configPath string) (*Config, error) {
	// Если путь не указан, используем значение по умолчанию
	if configPath == "" {
		configPath = getDefaultConfigPath()
	}

	// Проверяем существование файла
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Если файл не существует, создаем дефолтную конфигурацию
		config := DefaultConfig()
		if err := SaveConfig(config, configPath); err != nil {
			return nil, fmt.Errorf("failed to create default config: %w", err)
		}
		return config, nil
	}

	// Читаем файл
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Парсим YAML
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Расширяем ~ в путях конфигурации
	expandConfigPaths(&config)

	// Валидируем конфигурацию
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

// SaveConfig сохраняет конфигурацию в файл
func SaveConfig(config *Config, configPath string) error {
	// Создаем директорию если не существует
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Сериализуем в YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Записываем в файл
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// DefaultConfig возвращает конфигурацию по умолчанию
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	logDir := filepath.Join(homeDir, ".claude", "logs")

	return &Config{
		General: GeneralConfig{
			LogLevel: "info",
			LogFile:  filepath.Join(logDir, "claude-hooks.log"),
		},
		Validators: map[string]ValidatorConfig{
			"emergency_defaults": {
				Enabled:           true,
				CaseSensitive:     false,
				ExceptionPaths:    []string{"docs/", "README"},
				ExceptionFiles:    []string{"*.md", "*.txt", "*.rst"},
				SuggestionMessage: "Use explicit validation, required parameters, error throwing",
			},
			"runtime_exit": {
				Enabled:         true,
				GoFilesOnly:     true,
				TestExceptions:  []string{"*_test.go", "tests/", "test/"},
				ProductionPaths: []string{"backend/", "src/", "internal/"},
			},
			"secrets": {
				Enabled:              true,
				JWTPattern:           "eyJ[a-zA-Z0-9+/]+",
				WalletPattern:        "0x[a-fA-F0-9]{40}",
				TestConfigExceptions: []string{"test-config.ts", "test-config.js", "*test*.json"},
			},
		},
		Tools: map[string]ToolConfig{
			"bash": {
				Enabled:         true,
				BlockedPatterns: []string{"--headed", "rm -rf /", "rm -rf ~", ":(){ :|:& };:"},
			},
			"formatter": {
				Enabled:  true,
				GoFormat: true,
				TSFormat: true,
				Formatters: map[string]string{
					"go":  "gofmt -w",
					"ts":  "prettier --write",
					"tsx": "prettier --write",
					"js":  "prettier --write",
					"jsx": "prettier --write",
				},
			},
			"notifier": {
				Enabled: true,
				Sound:   true,
				Desktop: true,
			},
		},
		Logger: LoggerConfig{
			Level:   "info",
			Format:  "text",
			Output:  "file",
			LogFile: filepath.Join(logDir, "claude-hooks.log"),
		},
	}
}

// validateConfig проверяет корректность конфигурации
func validateConfig(config *Config) error {
	// Проверяем уровень логирования
	validLogLevels := []string{"debug", "info", "warn", "warning", "error"}
	if !contains(validLogLevels, config.General.LogLevel) {
		return fmt.Errorf("invalid log level: %s", config.General.LogLevel)
	}

	// Проверяем конфигурацию логгера
	validOutputs := []string{"stdout", "stderr", "file"}
	if !contains(validOutputs, config.Logger.Output) {
		return fmt.Errorf("invalid logger output: %s", config.Logger.Output)
	}

	if config.Logger.Output == "file" && config.Logger.LogFile == "" {
		return fmt.Errorf("logger.file is required when output is 'file'")
	}

	return nil
}

// getDefaultConfigPath возвращает путь к конфигурации по умолчанию
func getDefaultConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".claude", "hooks", "config.yaml")
}

// contains проверяет содержится ли элемент в слайсе
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, item) {
			return true
		}
	}
	return false
}

// expandPath раскрывает ~ в пути к домашней директории
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

// expandConfigPaths применяет expandPath к всем путям в конфигурации
func expandConfigPaths(config *Config) {
	// Расширяем пути в общих настройках
	config.General.LogFile = expandPath(config.General.LogFile)

	// Расширяем пути в настройках логгера
	config.Logger.LogFile = expandPath(config.Logger.LogFile)
}
