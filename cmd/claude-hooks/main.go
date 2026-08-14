package main

import (
	"context"
	"fmt"
	"io"
	"os"
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
		Long:          "Обработчик хуков Claude Code: проверка операций перед выполнением, автоформатирование и уведомления.",
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
		newHookCmd("pre-tool-use", "Обработать PreToolUse hook", &exitCode),
		newHookCmd("post-tool-use", "Обработать PostToolUse hook", &exitCode),
		newHookCmd("stop", "Обработать Stop hook", &exitCode),
		newHookCmd("notification", "Обработать Notification hook", &exitCode),
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	input, err := readInput(os.Stdin, hookType)
	if err != nil {
		return exitError, err
	}

	// Остановка и минутное напоминание при живых фоновых задачах или взведённом
	// будильнике /loop — не события для человека: Claude вернётся к работе сам,
	// когда задача отчитается или будильник сработает. Пропускаются целиком,
	// до записи состояния, — иначе «готово» или «ждёт» соврали бы строке
	// статуса, а финальная остановка выглядела бы повтором и осталась без
	// уведомления. Запрос разрешения проходит всегда: без ответа человека
	// сессия встанет
	if hookType == "stop" || hookType == "notification" {
		// Решение по событию сессии разбирают постфактум («почему позвонило»,
		// «почему не позвонило»), а событие приходит и уходит бесследно
		reason := autoResumeMute(hookType, input)
		logDecision(hookType, input, reason)
		if reason != "" {
			return exitAllowed, nil
		}
	}

	// Строка статуса рисуется отдельным процессом и о ходе сессии не знает —
	// состояние для неё оставляют хуки. Предыдущее состояние идёт в контекст:
	// по нему виден переход, а не только новое состояние
	ctx = core.WithPreviousState(ctx, core.LoadSessionState(input.SessionID))
	if state, ok := hookStates[hookType]; ok {
		core.SaveSessionState(input.SessionID, state)
	}

	// Отправка запроса ничего не проверяет: она лишь отмечает, что работа
	// возобновилась, и без неё ответ без единого вызова инструмента остался бы
	// незамеченным
	if hookType == hookUserPromptSubmit {
		return exitAllowed, nil
	}

	config, err := core.LoadConfig(configPath)
	if err != nil {
		return exitError, fmt.Errorf("failed to load config: %w", err)
	}

	logger, err := core.NewLogger(config.Logger)
	if err != nil {
		return exitError, fmt.Errorf("failed to create logger: %w", err)
	}

	engine, err := processor.New(config, logger)
	if err != nil {
		return exitError, fmt.Errorf("failed to create processor: %w", err)
	}

	var response *core.HookResponse
	switch hookType {
	case "pre-tool-use":
		response, err = engine.ProcessPreToolUse(ctx, input)
	case "post-tool-use":
		response, err = engine.ProcessPostToolUse(ctx, input)
	case "stop":
		response, err = engine.ProcessStop(ctx, input)
	case "notification":
		response, err = engine.ProcessNotification(ctx, input)
	default:
		return exitError, fmt.Errorf("unknown hook type: %s", hookType)
	}

	if err != nil {
		logger.Error("hook processing failed", "hook_type", hookType, "error", err)
		return exitError, err
	}

	printResponse(logger, response)

	if response.Action == core.HookActionAllow {
		return exitAllowed, nil
	}
	// Warn и Block одинаково возвращают 2: только этот код доносит сообщение до модели
	return exitBlocked, nil
}

// autoResumeMute сообщает, глушится ли событие из-за того, что сессия вернётся
// к работе сама — по живой фоновой задаче или взведённому будильнику /loop, —
// и называет причину; пустая строка означает «не глушить». Остановка глушится
// всегда, уведомление — только минутное напоминание об ожидании
func autoResumeMute(hookType string, input *core.ToolInput) string {
	switch hookType {
	case "stop":
	case "notification":
		if !notifier.IsIdleReminder(input.Message) {
			return ""
		}
	default:
		return ""
	}
	if n := core.PendingBackgroundTasks(input.TranscriptPath); n > 0 {
		return fmt.Sprintf("живых фоновых задач: %d", n)
	}
	if core.AwaitingScheduledWakeup(input.TranscriptPath) {
		return "взведён будильник /loop"
	}
	return ""
}

// logDecision записывает решение по событию сессии вместе с тем, из чего оно
// сделано: без пути транскрипта и счётчиков непонятно, глушение промахнулось
// или сессии правда нечего ждать. Логгер поднимается отдельно — до этого места
// конфиг ещё не читался; при любой осечке событие просто останется без записи
func logDecision(hookType string, input *core.ToolInput, reason string) {
	config, err := core.LoadConfig(configPath)
	if err != nil {
		return
	}
	logger, err := core.NewLogger(config.Logger)
	if err != nil {
		return
	}
	if reason != "" {
		logger.Info("alert muted: session resumes on its own",
			"hook", hookType, "reason", reason)
		return
	}
	logger.Info("alert allowed: nothing to wait for",
		"hook", hookType,
		"transcript", input.TranscriptPath,
		"session", input.SessionID,
		"pending_tasks", core.PendingBackgroundTasks(input.TranscriptPath),
		"wakeup_armed", core.AwaitingScheduledWakeup(input.TranscriptPath),
	)
}

// hookUserPromptSubmit хук отправки запроса пользователем
const hookUserPromptSubmit = "user-prompt-submit"

// hookStates сопоставляет хук состоянию сессии, которое он подтверждает.
//
// Вызовы инструментов состояние не меняют, хотя и означают работу: их делают
// и субагенты, в том числе фоновые. Такой вызов посреди ожидания выглядел бы
// возобновлением работы, и следующее напоминание Claude Code снова зазвонило
// бы. Границы хода задают только запрос человека и остановка
var hookStates = map[string]core.SessionState{
	hookUserPromptSubmit: core.StateWorking,
	"stop":               core.StateDone,
	"notification":       core.StateWaiting,
}

// sessionEvents события сессии, приходящие без tool_input:
// имя инструмента для них задаёт сам хук
var sessionEvents = map[string]string{
	"stop":               core.EventStop,
	"notification":       core.EventNotification,
	hookUserPromptSubmit: core.EventUserPromptSubmit,
}

// readInput читает и разбирает данные хука из stdin.
// Для событий сессии ошибка разбора не критична: уведомить можно и без деталей
func readInput(stdin io.Reader, hookType string) (*core.ToolInput, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	event, isSessionEvent := sessionEvents[hookType]

	input, err := core.ParseToolInput(data)
	if err != nil {
		if isSessionEvent {
			return &core.ToolInput{ToolName: event}, nil
		}
		return nil, fmt.Errorf("failed to parse input: %w", err)
	}

	if isSessionEvent {
		input.ToolName = event
	}

	return input, nil
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
				path := configPathOrDefault()
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
				path := configPathOrDefault()
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
	path := configPathOrDefault()
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

	fmt.Println("Валидаторы:")
	for name, validatorConfig := range config.Validators {
		fmt.Printf("  %-20s %s\n", name, enabledLabel(validatorConfig.Enabled))
	}

	fmt.Println("\nИнструменты:")
	for name, toolConfig := range config.Tools {
		fmt.Printf("  %-20s %s\n", name, enabledLabel(toolConfig.Enabled))
	}

	return nil
}

// configPathOrDefault возвращает заданный путь конфигурации либо путь по умолчанию
func configPathOrDefault() string {
	if configPath != "" {
		return configPath
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
  claude-hooks notify --title "opencode ждёт ответа" --message "Проект: saga" --window-pid $$`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if alert.Title == "" {
				return fmt.Errorf("не задан --title")
			}

			if activate {
				if windowPID == 0 {
					windowPID = os.Getpid()
				}
				// Окно принадлежит одному из процессов в цепочке до эмулятора терминала
				alert.ActivatePIDs = desktop.ProcessAncestors(windowPID)
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
			alert.ActivatePIDs = desktop.ParseInts(pids)
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
			line, err := statusline.Render(cmd.Context(), os.Stdin)
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
