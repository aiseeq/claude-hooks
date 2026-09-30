package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/desktop"
	"github.com/aiseeq/claude-hooks/internal/processor"
	"github.com/aiseeq/claude-hooks/internal/statusline"
	"github.com/aiseeq/claude-hooks/internal/tools/notifier"
)

// Exit-коды, которые понимает Claude Code:
// 0 — операция разрешена, 1 — ошибка самого хука (не блокирует),
// 2 — операция заблокирована, stderr передается модели
const (
	exitAllowed = 0
	exitError   = 1
	exitBlocked = 2
)

var (
	configPath string
	verbose    bool
	timeout    time.Duration

	// Version подставляется через ldflags при сборке
	Version = "dev"
)

func main() {
	os.Exit(execute())
}

// execute запускает CLI и возвращает exit-код для Claude Code
func execute() int {
	exitCode := exitAllowed

	rootCmd := &cobra.Command{
		Use:           "claude-hooks",
		Short:         "Claude Code hooks processor",
		Long:          "Обработчик хуков Claude Code: уведомления о завершении и ожидании ответа, строка статуса, проверка стиля Jira-комментариев.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "путь к файлу конфигурации")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "подробный вывод")
	rootCmd.PersistentFlags().DurationVar(&timeout, "timeout", 5*time.Second, "таймаут обработки")

	rootCmd.AddCommand(
		newHookCmd(hookPreToolUse, "Обработать PreToolUse hook", &exitCode),
		newHookCmd(hookStop, "Обработать Stop hook", &exitCode),
		newHookCmd(hookNotification, "Обработать Notification hook", &exitCode),
		newHookCmd(hookUserPromptSubmit, "Обработать UserPromptSubmit hook", &exitCode),
		newNotifyCmd(),
		newStatusLineCmd(),
		newConfigCmd(),
		newVersionCmd(),
		newDeliverAlertCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "claude-hooks: %v\n", err)
		if exitCode == exitAllowed {
			exitCode = exitError
		}
	}

	return exitCode
}

// Имена подкоманд хуков, как их вызывает ~/.claude/settings.json
const (
	hookPreToolUse       = "pre-tool-use"
	hookStop             = "stop"
	hookNotification     = "notification"
	hookUserPromptSubmit = "user-prompt-submit"
)

// newHookCmd создает команду обработки хука, записывающую exit-код по указателю
func newHookCmd(hookType, short string, exitCode *int) *cobra.Command {
	return &cobra.Command{
		Use:   hookType,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runHook(cmd.Context(), hookType)
			*exitCode = code
			return err
		},
	}
}

// runHook выполняет основную логику хука
func runHook(ctx context.Context, hookType string) (int, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return exitError, fmt.Errorf("failed to read input: %w", err)
	}

	config, err := core.LoadConfig(configPath)
	if err != nil {
		return exitError, fmt.Errorf("failed to load config: %w", err)
	}

	logger, err := core.NewLogger(config.Logger)
	if err != nil {
		return exitError, fmt.Errorf("failed to create logger: %w", err)
	}

	switch hookType {
	case hookPreToolUse:
		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return runPreToolUse(hookCtx, data, config, logger)
	case hookUserPromptSubmit:
		// Отправка запроса ничего не проверяет: она лишь отмечает, что работа
		// возобновилась, и без неё ответ без единого вызова инструмента остался
		// бы незамеченным
		input := parseSessionInput(logger, hookType, data)
		saveState(logger, input.SessionID, core.StateWorking)
		return exitAllowed, nil
	case hookStop:
		runSessionEvent(core.EventStop, data, config, logger)
		return exitAllowed, nil
	case hookNotification:
		runSessionEvent(core.EventNotification, data, config, logger)
		return exitAllowed, nil
	default:
		return exitError, fmt.Errorf("unknown hook type: %s", hookType)
	}
}

// runPreToolUse прогоняет вызов инструмента через проверки
func runPreToolUse(ctx context.Context, data []byte, config *core.Config, logger core.Logger) (int, error) {
	input, err := core.ParseToolInput(data)
	if err != nil {
		return exitError, fmt.Errorf("failed to parse input: %w", err)
	}

	engine, err := processor.New(config, logger)
	if err != nil {
		return exitError, fmt.Errorf("failed to create processor: %w", err)
	}

	response, err := engine.ProcessPreToolUse(ctx, input)
	if err != nil {
		logger.Error("hook processing failed", "hook_type", hookPreToolUse, "error", err)
		return exitError, err
	}

	printResponse(logger, response)

	if response.Action == core.HookActionAllow {
		return exitAllowed, nil
	}
	// Warn и Block одинаково возвращают 2: только этот код доносит сообщение до модели
	return exitBlocked, nil
}

// runSessionEvent обрабатывает остановку и уведомление Claude Code. Всё
// нужное приходит в stdin: транскрипт не читается, звук и уведомление уходят
// в отдельный процесс, поэтому хук укладывается в миллисекунды. Сбои здесь
// только логируются: событие сессии нечего блокировать, а ошибка хука
// показалась бы человеку в интерфейсе на каждой остановке
func runSessionEvent(eventName string, data []byte, config *core.Config, logger core.Logger) {
	input := parseSessionInput(logger, eventName, data)

	// Неинтерактивную сессию человек не ждёт вовсе — ни её остановку, ни её
	// вопросы: ответить в неё всё равно некому
	printMode, err := core.PrintModeSession()
	if err != nil {
		logger.Warn("session mode unknown, assuming interactive", "error", err)
	}
	if printMode {
		logger.Info("alert muted", "hook", eventName, "reason", "неинтерактивная сессия claude -p")
		return
	}

	// Строка статуса рисуется отдельным процессом и о ходе сессии не знает —
	// состояние для неё оставляют хуки. Предыдущее состояние нужно решению:
	// по нему виден переход, а не только новое состояние
	previous, err := core.LoadSessionState(input.SessionID)
	if err != nil {
		logger.Warn("session state unavailable", "session", input.SessionID, "error", err)
	}

	event := notifier.Event{Name: eventName, Input: input, Previous: previous}
	if eventName == core.EventStop && input.BackgroundTasks != nil {
		active, err := core.ActiveBackgroundTasks(input.SessionID, *input.BackgroundTasks, time.Now())
		if err != nil {
			logger.Warn("background task bookkeeping failed", "session", input.SessionID, "error", err)
		}
		event.ActiveTasks = len(active)
	}

	decision := notifier.Decide(event)
	logDecision(logger, event, decision)

	if decision.State != "" {
		saveState(logger, input.SessionID, decision.State)
	}

	toolConfig, exists := config.Tools["notifier"]
	if !exists || !toolConfig.Enabled {
		return
	}
	if err := notifier.New(toolConfig, logger).Announce(eventName, input, decision); err != nil {
		logger.Warn("failed to deliver alert", "hook", eventName, "error", err)
	}
}

// parseSessionInput разбирает вход события сессии. Нечитаемый вход — ошибка
// в логе, но не повод молчать: уведомить можно и без деталей события
func parseSessionInput(logger core.Logger, hookType string, data []byte) *core.ToolInput {
	input, err := core.ParseToolInput(data)
	if err != nil {
		logger.Error("session event input unreadable, handling it without details", "hook", hookType, "error", err)
		return &core.ToolInput{}
	}
	return input
}

// saveState запоминает состояние сессии для строки статуса и следующего
// события. Сбой хранилища хук не срывает: строка статуса — не повод ломать хук
func saveState(logger core.Logger, sessionID string, state core.SessionState) {
	if err := core.SaveSessionState(sessionID, state); err != nil {
		logger.Warn("session state not saved", "session", sessionID, "error", err)
	}
}

// logDecision записывает решение по событию сессии вместе с тем, из чего оно
// сделано: событие приходит и уходит бесследно, а разбирают его постфактум
// («почему позвонило», «почему не позвонило»)
func logDecision(logger core.Logger, event notifier.Event, decision notifier.Decision) {
	input := event.Input
	fields := []any{
		"hook", event.Name,
		"session", input.SessionID,
		"previous_state", string(event.Previous),
		"state", string(decision.State),
	}

	switch event.Name {
	case core.EventStop:
		fields = append(fields, "active_tasks", event.ActiveTasks)
		if input.BackgroundTasks != nil {
			fields = append(fields, "background_tasks", taskTypes(*input.BackgroundTasks))
		}
		if input.SessionCrons != nil {
			fields = append(fields, "session_crons", len(*input.SessionCrons))
		}
	case core.EventNotification:
		fields = append(fields, "notification_type", input.NotificationType)
	}

	if decision.Reason != "" {
		fields = append(fields, "reason", decision.Reason)
	}

	if decision.Alert {
		logger.Info("alert allowed", fields...)
		return
	}
	logger.Info("alert muted", fields...)
}

// taskTypes перечисляет типы фоновых задач: shell, subagent, monitor…
// Описания и команды в лог не идут
func taskTypes(tasks []core.BackgroundTask) string {
	types := make([]string, 0, len(tasks))
	for _, task := range tasks {
		types = append(types, task.Type)
	}
	return strings.Join(types, ",")
}

// printResponse выводит результат: stderr читает Claude Code при exit-коде 2
func printResponse(logger core.Logger, response *core.HookResponse) {
	switch response.Action {
	case core.HookActionBlock, core.HookActionWarn:
		logger.Warn("hook blocked operation",
			"action", string(response.Action),
			"message", response.Message,
			"violations", len(response.Violations),
		)

		fmt.Fprintln(os.Stderr, response.Message)
		for _, suggestion := range response.Suggestions {
			fmt.Fprintf(os.Stderr, "  • %s\n", suggestion)
		}
	case core.HookActionAllow:
		if verbose {
			fmt.Fprintln(os.Stderr, "✅ проверки пройдены")
		}
	}

	if !verbose {
		return
	}

	for _, violation := range response.Violations {
		fmt.Fprintf(os.Stderr, "  [%s] %s:%d %s\n",
			violation.Severity, violation.Type, violation.Line, violation.Message)
	}
	fmt.Fprintf(os.Stderr, "⏱  %.1f ms\n", response.ProcessTime)
}

// newConfigCmd создает команду управления конфигурацией
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Управление конфигурацией",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Показать текущую конфигурацию",
			RunE: func(cmd *cobra.Command, args []string) error {
				return showConfig()
			},
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Проверить файл конфигурации",
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := configPathOrDefault()
				if err != nil {
					return err
				}
				if _, err := core.LoadConfig(path); err != nil {
					return err
				}
				fmt.Printf("конфигурация корректна: %s\n", path)
				return nil
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Создать конфигурацию по умолчанию",
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := configPathOrDefault()
				if err != nil {
					return err
				}
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("файл уже существует: %s", path)
				}
				if err := core.SaveConfig(core.DefaultConfig(), path); err != nil {
					return err
				}
				fmt.Printf("конфигурация создана: %s\n", path)
				return nil
			},
		},
	)

	return cmd
}

// showConfig печатает состояние валидаторов и инструментов
func showConfig() error {
	path, err := configPathOrDefault()
	if err != nil {
		return err
	}
	config, err := core.LoadConfig(path)
	if err != nil {
		return err
	}

	fmt.Printf("Конфигурация: %s\n", path)
	if config.Logger.Output == "file" {
		fmt.Printf("Логи: %s (уровень %s, ротация %d МБ)\n\n",
			config.Logger.LogFile, config.Logger.Level, config.Logger.MaxSizeMB)
	} else {
		fmt.Printf("Логи: %s (уровень %s)\n\n", config.Logger.Output, config.Logger.Level)
	}

	fmt.Println("Инструменты:")
	names := make([]string, 0, len(config.Tools))
	for name := range config.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("  %-20s %s\n", name, enabledLabel(config.Tools[name].Enabled))
	}

	return nil
}

// configPathOrDefault возвращает заданный путь конфигурации либо путь по умолчанию
func configPathOrDefault() (string, error) {
	if configPath != "" {
		return configPath, nil
	}
	return core.DefaultConfigPath()
}

// enabledLabel возвращает текстовое состояние компонента
func enabledLabel(enabled bool) string {
	if enabled {
		return "включён"
	}
	return "выключен"
}

// newNotifyCmd создает команду отправки уведомления из сторонних инструментов.
// Claude Code присылает события сам, а другим агентам (например opencode)
// нужен способ показать такое же уведомление с переходом к окну по клику
func newNotifyCmd() *cobra.Command {
	var (
		alert     desktop.Alert
		windowPID int
		activate  bool
	)

	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Показать уведомление с переходом к окну по клику",
		Long: `Показывает уведомление и по клику переводит фокус на окно терминала.

Окно определяется по процессу: указанному через --window-pid либо самому вызывающему.
Команда возвращает управление сразу, не дожидаясь ни звука, ни клика.

Пример вызова из плагина стороннего агента:
  claude-hooks notify --title "opencode ждёт ответа" --message "Проект: projecta" --window-pid $$`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if alert.Title == "" {
				return fmt.Errorf("не задан --title")
			}

			if activate {
				if windowPID == 0 {
					windowPID = os.Getpid()
				}
				// Окно принадлежит одному из процессов в цепочке до эмулятора
				// терминала; оборванная цепочка всё равно годится — окно может
				// быть у собранной части
				ancestors, err := desktop.ProcessAncestors(windowPID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "claude-hooks: process ancestry incomplete: %v\n", err)
				}
				alert.ActivatePIDs = ancestors
			}

			executable, err := os.Executable()
			if err != nil {
				return fmt.Errorf("failed to locate own executable: %w", err)
			}

			return desktop.DeliverInBackground(executable, notifier.WatchCommand, alert)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&alert.Title, "title", "", "заголовок уведомления")
	flags.StringVar(&alert.Message, "message", "", "текст уведомления")
	flags.StringVar(&alert.AppName, "app-name", "claude-hooks", "имя приложения в уведомлении")
	flags.StringVar(&alert.Icon, "icon", "utilities-terminal", "значок уведомления")
	flags.DurationVar(&alert.Timeout, "timeout", 30*time.Second, "время показа уведомления")
	flags.BoolVar(&alert.Sound, "sound", true, "проиграть звук")
	flags.BoolVar(&alert.Desktop, "desktop", true, "показать уведомление")
	flags.IntVar(&windowPID, "window-pid", 0, "процесс, чьё окно активируется по клику (по умолчанию — вызывающий)")
	flags.BoolVar(&activate, "activate-window", true, "переводить фокус на окно по клику")
	flags.StringVar(&alert.ActionLabel, "action-label", "Перейти к окну", "подпись действия уведомления")

	return cmd
}

// newDeliverAlertCmd создает команду фоновой доставки оповещения.
// Хук запускает её отдельным процессом и сразу завершается: ожидание клика
// по уведомлению длится дольше, чем Claude Code готов ждать хук
func newDeliverAlertCmd() *cobra.Command {
	var (
		alert       desktop.Alert
		pids        string
		actionLabel string
	)

	cmd := &cobra.Command{
		Use:    notifier.WatchCommand,
		Short:  "Показать уведомление и обработать клик по нему",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			activatePIDs, err := desktop.ParseInts(pids)
			if err != nil {
				return err
			}
			alert.ActivatePIDs = activatePIDs
			alert.ActionLabel = actionLabel
			return desktop.Deliver(cmd.Context(), alert)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&alert.Title, "title", "", "заголовок уведомления")
	flags.StringVar(&alert.Message, "message", "", "текст уведомления")
	flags.StringVar(&alert.AppName, "app-name", "Claude Code", "имя приложения")
	flags.StringVar(&alert.Icon, "icon", "utilities-terminal", "значок уведомления")
	flags.DurationVar(&alert.Timeout, "timeout", 10*time.Second, "время показа уведомления")
	flags.BoolVar(&alert.Sound, "sound", true, "проиграть звук")
	flags.BoolVar(&alert.Desktop, "desktop", true, "показать уведомление")
	flags.StringVar(&pids, "pids", "", "процессы, чьё окно активируется по клику")
	flags.StringVar(&actionLabel, "action-label", "", "подпись действия уведомления")

	return cmd
}

// newStatusLineCmd создает команду отрисовки строки статуса Claude Code
func newStatusLineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "statusline",
		Short: "Отрисовать строку статуса Claude Code",
		Long: `Читает JSON сессии со stdin и печатает две строки статуса:
плашку с именем проекта и подробности о ветке, модели и контексте.

Подключается в ~/.claude/settings.json:
  "statusLine": {"type": "command", "command": "~/.claude/hooks/claude-hooks statusline"}`,
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := core.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			logger, err := core.NewLogger(config.Logger)
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}
			line, err := statusline.Render(cmd.Context(), os.Stdin, logger)
			if err != nil {
				return err
			}
			fmt.Println(line)
			return nil
		},
	}
}

// newVersionCmd создает команду вывода версии
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Показать версию",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("claude-hooks %s\n", Version)
		},
	}
}
