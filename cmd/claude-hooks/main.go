package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/processor"
)

// Logger для claude hooks
var claudeHooksLogger core.Logger

var (
	configPath string
	verbose    bool
	timeout    time.Duration
	exitCode   int

	// Версионная информация (встраивается через ldflags при сборке)
	Version     = "dev"
	BuildNumber = "0"
	BuildTime   = "unknown"
	GitCommit   = "unknown"
)

func main() {
	// Инициализируем logger
	var err error
	claudeHooksLogger, err = core.NewLogger(core.DefaultLoggerConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	rootCmd := &cobra.Command{
		Use:   "claude-hooks",
		Short: "Claude Code Hooks unified processor",
		Long: `Claude Code Hooks unified Go application for processing PreToolUse, PostToolUse, and Stop hooks.
Replaces multiple bash scripts with a single, efficient, and maintainable solution.`,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}

	// Глобальные флаги
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	rootCmd.PersistentFlags().DurationVar(&timeout, "timeout", 5*time.Second, "Operation timeout")

	// Добавляем подкоманды
	rootCmd.AddCommand(
		newPreToolUseCmd(),
		newPostToolUseCmd(),
		newStopCmd(),
		newTestCmd(),
		newConfigCmd(),
		newVersionCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		claudeHooksLogger.Error("Root command execution failed", "error", err.Error(), "operation", "main_execute", "component", "claude_hooks")
		exitCode = 1
	}

	// Graceful shutdown с возвратом exit code
	os.Exit(exitCode)
}

// newPreToolUseCmd создает команду для PreToolUse hook
func newPreToolUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pre-tool-use",
		Short: "Process PreToolUse hook",
		Long:  "Processes PreToolUse hook for Write, Edit, MultiEdit operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runHook(cmd.Context(), "pre-tool-use")
			exitCode = code
			return err
		},
	}
}

// newPostToolUseCmd создает команду для PostToolUse hook
func newPostToolUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "post-tool-use",
		Short: "Process PostToolUse hook",
		Long:  "Processes PostToolUse hook for auto-formatting and cleanup",
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runHook(cmd.Context(), "post-tool-use")
			exitCode = code
			return err
		},
	}
}

// newStopCmd создает команду для Stop hook
func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Process Stop hook",
		Long:  "Processes Stop hook for notifications and cleanup",
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runHook(cmd.Context(), "stop")
			exitCode = code
			return err
		},
	}
}

// newTestCmd создает команду для тестирования
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test hook rules",
		Long:  "Test hook rules against sample files and commands",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "validators",
			Short: "Test all validators",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runValidatorTests(cmd.Context())
			},
		},
		&cobra.Command{
			Use:   "advisors",
			Short: "Test all advisors",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runAdvisorTests(cmd.Context())
			},
		},
		&cobra.Command{
			Use:   "tools",
			Short: "Test tool validators",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runToolTests(cmd.Context())
			},
		},
	)

	return cmd
}

// newConfigCmd создает команду для работы с конфигурацией
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management",
		Long:  "Manage hook configuration",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Show current configuration",
			RunE: func(cmd *cobra.Command, args []string) error {
				return showConfig(cmd.Context())
			},
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Validate configuration file",
			RunE: func(cmd *cobra.Command, args []string) error {
				return validateConfigFile(cmd.Context())
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Initialize default configuration",
			RunE: func(cmd *cobra.Command, args []string) error {
				return initConfig(cmd.Context())
			},
		},
	)

	return cmd
}

// newVersionCmd создает команду для отображения версии
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long: `Display detailed version information including build number, time, and git commit.
Each build automatically increments the build number through the Makefile system.`,
		Run: func(cmd *cobra.Command, args []string) {
			if verbose {
				claudeHooksLogger.Info("🚀 Claude Hooks Detailed Version Information", "version", Version, "build_number", BuildNumber, "build_time", BuildTime, "git_commit", GitCommit, "built_with", "Go", "operation", "show_version", "component", "claude_hooks")
			} else {
				claudeHooksLogger.Info("Claude Hooks version", "version", Version, "build_number", BuildNumber, "git_commit", GitCommit, "built_with", "Go", "operation", "show_version", "component", "claude_hooks")
			}
		},
	}
}

// runHook выполняет основную логику хука
func runHook(ctx context.Context, hookType string) (int, error) {
	// Создаем контекст с таймаутом
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Загружаем конфигурацию
	config, err := core.LoadConfig(configPath)
	if err != nil {
		return 1, fmt.Errorf("failed to load config: %w", err)
	}

	// Создаем логгер
	logger, err := core.NewLogger(&config.Logger)
	if err != nil {
		return 1, fmt.Errorf("failed to create logger: %w", err)
	}

	// Создаем процессор
	proc, err := processor.New(config, logger)
	if err != nil {
		return 1, fmt.Errorf("failed to create processor: %w", err)
	}

	// Читаем входные данные из stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 1, fmt.Errorf("failed to read input: %w", err)
	}

	// Обрабатываем в зависимости от типа хука
	var response *core.HookResponse
	switch hookType {
	case "stop":
		// Для stop hook парсим входные данные для получения transcript_path
		toolInput, parseErr := core.ParseToolInput(input)
		if parseErr != nil {
			logger.Debug("failed to parse stop hook input, using empty ToolInput", "error", parseErr, "input_size", len(input))
			// Создаем минимальный ToolInput для stop hook без transcript_path
			toolInput = &core.ToolInput{
				ToolName: "Stop",
			}
		}
		// Гарантируем правильный ToolName независимо от успеха парсинга
		toolInput.ToolName = "Stop"

		// ProcessStop doesn't need toolInput parameter
		response, err = proc.ProcessStop(ctx)
	case "pre-tool-use", "post-tool-use":
		// Парсим входные данные для tool hooks
		toolInput, parseErr := core.ParseToolInput(input)
		if parseErr != nil {
			return 1, fmt.Errorf("failed to parse input: %w", parseErr)
		}

		if hookType == "pre-tool-use" {
			if verbose {
				fmt.Printf("🚨 CALLING ProcessPreToolUse with tool=%s, file=%s\n", toolInput.ToolName, toolInput.FilePath)
			}
			response, err = proc.ProcessPreToolUse(ctx, toolInput)
			if verbose {
				fmt.Printf("🚨 ProcessPreToolUse RETURNED: err=%v\n", err)
			}
		} else {
			response, err = proc.ProcessPostToolUse(ctx, toolInput)
		}
	default:
		return 1, fmt.Errorf("unknown hook type: %s", hookType)
	}

	if err != nil {
		logger.Error("hook processing failed", "hook_type", hookType, "error", err)
		return 1, err
	}

	// Выводим результат
	if err := outputResponse(response, verbose); err != nil {
		return 1, fmt.Errorf("failed to output response: %w", err)
	}

	// Возвращаем соответствующий exit code
	switch response.Action {
	case core.HookActionBlock:
		return 2, nil // Блокируем операцию
	case core.HookActionWarn:
		return 2, nil // Blocking warning для видимости в интерфейсе Claude Code
	case core.HookActionAllow:
		return 0, nil // Разрешаем
	}

	return 0, nil
}

// outputResponse выводит ответ хука
func outputResponse(response *core.HookResponse, verbose bool) error {
	// Минимальное логирование согласно CLAUDE.md принципам
	if verbose {
		claudeHooksLogger.Debug("Hook response", "action", string(response.Action), "operation", "output_response")
	}

	// КРИТИЧЕСКОЕ: если есть модифицированный tool input, выводим его в stdout в JSON формате
	// Claude Code использует stdout для получения модифицированных параметров
	if response.ModifiedToolInput != nil {
		modifiedJSON, err := json.Marshal(response.ModifiedToolInput)
		if err != nil {
			claudeHooksLogger.Error("❌ ERROR: Failed to serialize modified tool input", "error", err.Error(), "operation", "output_response", "component", "claude_hooks")
			fmt.Fprintf(os.Stderr, "❌ ERROR: Failed to serialize modified tool input: %v\n", err)
		} else {
			// Убрано избыточное логирование modified tool input согласно CLAUDE.md
			// Выводим модифицированные параметры в stdout для Claude Code
			fmt.Print(string(modifiedJSON))
		}
	}

	switch response.Action {
	case core.HookActionBlock:
		// Минимальное WARN логирование - только ключевая информация
		claudeHooksLogger.Warn("Hook blocked operation", "message", response.Message)

		// Просто выводим сообщение как есть - без префиксов
		fmt.Fprintf(os.Stderr, "%s\n", response.Message)
		if len(response.Suggestions) > 0 {
			fmt.Fprintf(os.Stderr, "💡 Suggestions:\n")
			for _, suggestion := range response.Suggestions {
				fmt.Fprintf(os.Stderr, "   • %s\n", suggestion)
				// Убрано избыточное логирование suggestions согласно CLAUDE.md
			}
		}
	case core.HookActionWarn:
		// Минимальное WARN логирование согласно CLAUDE.md
		claudeHooksLogger.Warn("Hook warning", "message", response.Message)

		fmt.Fprintf(os.Stderr, "⚠️  WARNING: %s\n", response.Message)
		if len(response.Suggestions) > 0 {
			fmt.Fprintf(os.Stderr, "💡 Suggestions:\n")
			for _, suggestion := range response.Suggestions {
				fmt.Fprintf(os.Stderr, "   • %s\n", suggestion)
				// Убрано избыточное логирование suggestions согласно CLAUDE.md
			}
		}
	case core.HookActionAllow:
		// Минимальное INFO логирование только в verbose режиме
		if verbose {
			claudeHooksLogger.Info("Hook allowed", "message", response.Message)
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "✅ ALLOWED: Operation passed all checks\n")
		}
		// Показываем модификации команд в stderr без избыточного логирования
		if response.ModifiedToolInput != nil {
			fmt.Fprintf(os.Stderr, "🔄 COMMAND MODIFIED: %s\n", response.ModifiedToolInput.Command)
		}
	}

	// Выводим детальную информацию о нарушениях в verbose режиме
	if verbose && len(response.Violations) > 0 {
		// Убрано избыточное логирование violations согласно CLAUDE.md

		fmt.Fprintf(os.Stderr, "\n📋 Violations:\n")
		for _, v := range response.Violations {
			// Убрано избыточное логирование violation details

			fmt.Fprintf(os.Stderr, "   • %s: %s\n", v.Type, v.Message)
			if v.Suggestion != "" {
				fmt.Fprintf(os.Stderr, "     💡 %s\n", v.Suggestion)
			}
		}
	}

	// Показываем directory warnings ВСЕГДА (не только в verbose)
	if len(response.Violations) > 0 {
		for _, v := range response.Violations {
			if v.Type == "directory_navigation_warning" {
				// Убрано избыточное логирование directory warnings

				fmt.Fprintf(os.Stderr, "\n%s\n", v.Message)
				if v.Suggestion != "" {
					fmt.Fprintf(os.Stderr, "%s\n", v.Suggestion)
				}
			}
		}
	}

	if verbose {
		// Убрано избыточное логирование processing time
		fmt.Fprintf(os.Stderr, "⏱️  Processing time: %v\n", response.ProcessTime)
	}

	return nil
}

// runValidatorTests запускает тесты валидаторов
func runValidatorTests(ctx context.Context) error {
	claudeHooksLogger.Warn("⚠️ Validator testing not implemented yet", "operation", "run_validator_tests", "component", "claude_hooks")
	fmt.Println("⚠️ NOTICE: Validator testing is not implemented yet")
	fmt.Println("📝 TODO: Implement comprehensive validator tests")
	return fmt.Errorf("not implemented: validator testing functionality")
}

// runAdvisorTests запускает тесты советчиков
func runAdvisorTests(ctx context.Context) error {
	claudeHooksLogger.Warn("⚠️ Advisor testing not implemented yet", "operation", "run_advisor_tests", "component", "claude_hooks")
	fmt.Println("⚠️ NOTICE: Advisor testing is not implemented yet")
	fmt.Println("📝 TODO: Implement comprehensive advisor tests")
	return fmt.Errorf("not implemented: advisor testing functionality")
}

// runToolTests запускает тесты инструментов
func runToolTests(ctx context.Context) error {
	claudeHooksLogger.Warn("⚠️ Tool testing not implemented yet", "operation", "run_tool_tests", "component", "claude_hooks")
	fmt.Println("⚠️ NOTICE: Tool testing is not implemented yet")
	fmt.Println("📝 TODO: Implement comprehensive tool tests")
	return fmt.Errorf("not implemented: tool testing functionality")
}

// showConfig показывает текущую конфигурацию
func showConfig(ctx context.Context) error {
	config, err := core.LoadConfig(configPath)
	if err != nil {
		return err
	}

	claudeHooksLogger.Info("📋 Current configuration", "config_file", configPath, "log_level", config.General.LogLevel, "timeout_ms", config.General.Timeout, "operation", "show_config", "component", "claude_hooks")

	claudeHooksLogger.Info("🔍 Validators", "operation", "show_config", "component", "claude_hooks")
	for name, cfg := range config.Validators {
		status := "disabled"
		if cfg.Enabled {
			status = "enabled"
		}
		claudeHooksLogger.Info("Validator status", "name", name, "status", status, "enabled", cfg.Enabled, "operation", "show_config", "component", "claude_hooks")
	}

	return nil
}

// validateConfigFile валидирует конфигурационный файл
func validateConfigFile(ctx context.Context) error {
	_, err := core.LoadConfig(configPath)
	if err != nil {
		claudeHooksLogger.Error("❌ Configuration validation failed", "error", err.Error(), "config_path", configPath, "operation", "validate_config_file", "component", "claude_hooks")
		return err
	}

	claudeHooksLogger.Info("✅ Configuration is valid", "config_path", configPath, "operation", "validate_config_file", "component", "claude_hooks")
	return nil
}

// initConfig создает конфигурационный файл по умолчанию
func initConfig(ctx context.Context) error {
	if configPath == "" {
		homeDir, _ := os.UserHomeDir()
		configPath = homeDir + "/.claude/hooks/config.yaml"
		claudeHooksLogger.Info("Default config path set", "config_path", configPath, "home_dir", homeDir, "operation", "init_config", "component", "claude_hooks")
	}

	config := core.DefaultConfig()
	if err := core.SaveConfig(config, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	claudeHooksLogger.Info("✅ Default configuration created", "config_path", configPath, "operation", "init_config", "component", "claude_hooks")
	return nil
}
