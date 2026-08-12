package notifier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/desktop"
	"github.com/aiseeq/claude-hooks/internal/tools"
)

// WatchCommand имя скрытой подкоманды, которая доставляет оповещение в фоне
const WatchCommand = "deliver-alert"

// Время жизни уведомлений: запрос ответа висит дольше, его ждёт человек
const (
	stopTimeout         = 10 * time.Second
	notificationTimeout = 30 * time.Second
)

// NotifierTool уведомляет о завершении работы и о вопросах Claude Code
type NotifierTool struct {
	*tools.BaseTool
	sound           bool
	desktop         bool
	activateOnClick bool
}

// NewNotifierTool создает инструмент уведомлений
func NewNotifierTool(config core.ToolConfig, logger core.Logger) (*NotifierTool, error) {
	return &NotifierTool{
		BaseTool:        tools.NewBaseTool("notifier", config.Enabled, []string{core.EventStop, core.EventNotification}, logger),
		sound:           config.Sound,
		desktop:         config.Desktop,
		activateOnClick: config.ActivateOnClick,
	}, nil
}

// ValidateTool обрабатывает события сессии: завершение работы и запрос к пользователю
func (t *NotifierTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	projectName := t.ProjectName(input)

	alert, terminalTitle, ok := t.buildAlert(input, projectName)
	if !ok {
		return &core.ValidationResult{IsValid: true}, nil
	}

	// Заголовок окна — подсказка в списке окон и в панели задач
	desktop.SetTerminalTitle(terminalTitle)

	// Пока Claude ждёт, Claude Code напоминает о себе тем же событием.
	// Человека уже позвали один раз, и повторный звонок только отвлекает:
	// по вкладкам он пройдётся сам, когда освободится
	if previous := core.PreviousStateFromContext(ctx); previous != core.StateWorking {
		t.Logger().Debug("alert skipped: session already idle",
			"event", input.ToolName,
			"previous_state", string(previous),
		)
		return &core.ValidationResult{IsValid: true}, nil
	}

	t.Logger().Debug("session event",
		"event", input.ToolName,
		"project", projectName,
		"activate_pids", len(alert.ActivatePIDs),
	)

	// Доставка идёт в отдельном процессе: хук не ждёт ни звука, ни клика
	if alert.Sound || alert.Desktop {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("failed to locate own executable: %w", err)
		}
		if err := desktop.DeliverInBackground(executable, WatchCommand, alert); err != nil {
			t.Logger().Warn("failed to deliver alert", "error", err)
		}
	}

	return &core.ValidationResult{
		IsValid:     true,
		Suggestions: []string{fmt.Sprintf("Уведомления отправлены для проекта [%s]", projectName)},
	}, nil
}

// buildAlert собирает оповещение под конкретное событие
func (t *NotifierTool) buildAlert(input *core.ToolInput, projectName string) (desktop.Alert, string, bool) {
	alert := desktop.Alert{
		AppName:     "Claude Code",
		Icon:        "utilities-terminal",
		Sound:       t.sound,
		Desktop:     t.desktop,
		ActionLabel: "Перейти к окну",
	}

	if t.activateOnClick {
		// Окно принадлежит одному из предков: сам хук окна не имеет
		alert.ActivatePIDs = desktop.ProcessAncestors(os.Getpid())
	}

	var terminalTitle string

	switch input.ToolName {
	case core.EventStop:
		terminalTitle = fmt.Sprintf("✅ %s · готово", strings.ToUpper(projectName))
		alert.Title = "Claude Code завершил работу"
		alert.Message = "Проект: " + projectName
		alert.Timeout = stopTimeout

	case core.EventNotification:
		terminalTitle = fmt.Sprintf("🟡 %s · ждёт ответа", strings.ToUpper(projectName))
		alert.Title = fmt.Sprintf("Claude Code ждёт ответа (%s)", projectName)
		// Claude Code сообщает, чего именно ждёт: разрешения на инструмент или ввода
		alert.Message = input.Message
		if alert.Message == "" {
			alert.Message = "Проект: " + projectName
		}
		alert.Timeout = notificationTimeout

	default:
		return desktop.Alert{}, "", false
	}

	return alert, terminalTitle, true
}

// IsIdleReminder распознаёт минутное напоминание «Claude is waiting for your
// input» (тип idle_prompt). Запрос разрешения инструменту им не является:
// его глушить нельзя — без ответа человека сессия встанет
func IsIdleReminder(message string) bool {
	return strings.Contains(strings.ToLower(message), "waiting for your input")
}

// ProjectName определяет имя проекта: рабочая директория сессии — самый надёжный
// источник, путь транскрипта используется как запасной вариант
func (t *NotifierTool) ProjectName(input *core.ToolInput) string {
	if input.CWD != "" {
		return core.ProjectNameForDir(input.CWD)
	}

	if transcriptPath := input.TranscriptPath; transcriptPath != "" {
		encoded := filepath.Base(filepath.Dir(transcriptPath))
		if dir := decodeProjectDir(encoded); dir != "" {
			return core.ProjectNameForDir(dir)
		}
	}

	if wd, err := os.Getwd(); err == nil {
		return core.ProjectNameForDir(wd)
	}

	return "unknown"
}

// decodeProjectDir восстанавливает путь проекта из имени каталога транскриптов
// Claude Code: /home/user/work/claude-hooks → -home-user-work-claude-hooks.
// Кодирование неоднозначно — дефис может быть как разделителем каталогов,
// так и частью имени, — поэтому вариант проверяется по файловой системе.
// Так различаются ~/work/saga/frontend/admin-app и ~/work/claude-hooks
func decodeProjectDir(encoded string) string {
	if encoded == "" || encoded == "." || encoded == string(filepath.Separator) {
		return ""
	}

	path := string(filepath.Separator)
	// Пустой сегмент означает дефис в начале имени каталога: /tmp/x/-home-user
	pendingDash := false

	for i, segment := range strings.Split(encoded, "-") {
		if segment == "" {
			// Ведущий дефис задаёт абсолютный путь и сегмента не образует
			pendingDash = i != 0
			continue
		}

		if pendingDash {
			segment = "-" + segment
			pendingDash = false
		}

		nested := filepath.Join(path, segment)
		if isDir(nested) {
			path = nested
			continue
		}

		// Дефис оказался частью имени текущего каталога
		if joined := path + "-" + segment; isDir(joined) {
			path = joined
			continue
		}

		// Каталог мог быть удалён или переименован: считаем дефис разделителем
		path = nested
	}

	if path == string(filepath.Separator) {
		return ""
	}
	return path
}

// isDir сообщает, существует ли каталог по указанному пути
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
