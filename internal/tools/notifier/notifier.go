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
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", terminalTitle)

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
		terminalTitle = fmt.Sprintf("🔔 Claude Code [%s] — готово", projectName)
		alert.Title = "Claude Code завершил работу"
		alert.Message = "Проект: " + projectName
		alert.Timeout = stopTimeout

	case core.EventNotification:
		terminalTitle = fmt.Sprintf("❓ Claude Code [%s] — ждёт ответа", projectName)
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

// ProjectName определяет имя проекта: рабочая директория сессии — самый надёжный
// источник, путь транскрипта используется как запасной вариант
func (t *NotifierTool) ProjectName(input *core.ToolInput) string {
	if input.CWD != "" {
		return projectNameForDir(input.CWD)
	}

	if transcriptPath := input.TranscriptPath; transcriptPath != "" {
		encoded := filepath.Base(filepath.Dir(transcriptPath))
		if dir := decodeProjectDir(encoded); dir != "" {
			return projectNameForDir(dir)
		}
	}

	if wd, err := os.Getwd(); err == nil {
		return projectNameForDir(wd)
	}

	return "unknown"
}

// projectNameForDir возвращает имя проекта по его каталогу.
//
// Одного последнего сегмента не всегда достаточно: ~/work/saga/backend и
// ~/work/glint/backend дали бы одинаковое имя. Поэтому у вложенных проектов
// показывается и родительский каталог. Домашний каталог и корень своего имени
// не имеют — для них возвращаются понятные обозначения, а не имя пользователя
func projectNameForDir(dir string) string {
	dir = filepath.Clean(dir)

	if dir == string(filepath.Separator) {
		return "/"
	}

	home, err := os.UserHomeDir()
	if err == nil {
		home = filepath.Clean(home)
		if dir == home {
			return "~"
		}

		// Внутри домашнего каталога первый уровень — папка вроде work или git,
		// имени проекта она не добавляет
		if relative, relErr := filepath.Rel(home, dir); relErr == nil && !strings.HasPrefix(relative, "..") {
			segments := strings.Split(relative, string(filepath.Separator))
			if len(segments) >= 3 {
				return filepath.Join(segments[len(segments)-2], segments[len(segments)-1])
			}
		}
	}

	if base := filepath.Base(dir); base != "." && base != string(filepath.Separator) {
		return base
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
