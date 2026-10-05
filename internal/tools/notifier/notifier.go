package notifier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/desktop"
)

// WatchCommand имя скрытой подкоманды, которая доставляет оповещение в фоне
const WatchCommand = "deliver-alert"

// Время жизни уведомлений: запрос ответа висит дольше, его ждёт человек
const (
	stopTimeout         = 10 * time.Second
	notificationTimeout = 30 * time.Second
)

// Notifier показывает человеку, что сессия закончила работу или ждёт ответа:
// заголовок окна, звук и уведомление с переходом к окну по клику
type Notifier struct {
	logger          core.Logger
	sound           bool
	desktop         bool
	activateOnClick bool
}

// New создает notifier по конфигурации
func New(config core.ToolConfig, logger core.Logger) *Notifier {
	return &Notifier{
		logger:          logger.With("tool", "notifier"),
		sound:           config.Sound,
		desktop:         config.Desktop,
		activateOnClick: config.ActivateOnClick,
	}
}

// Announce доводит решение до человека. Заголовок окна меняется на каждом
// переходе в «готово» или «ждёт ответа», даже без звонка: это подсказка в
// панели задач. Звук и уведомление уходят в отдельный процесс — хук не ждёт
// ни звука, ни клика
func (n *Notifier) Announce(eventName string, input *core.ToolInput, decision Decision) error {
	if decision.State != core.StateDone && decision.State != core.StateWaiting {
		return nil
	}

	projectName := n.ProjectName(input)
	alert, terminalTitle, err := n.buildAlert(eventName, input, projectName)
	if err != nil {
		return err
	}

	// Без заголовка уведомление всё равно уходит
	if err := desktop.SetTerminalTitle(terminalTitle); err != nil {
		n.logger.Warn("terminal title not set", "error", err)
	}

	if !decision.Alert || !(alert.Sound || alert.Desktop) {
		return nil
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to locate own executable: %w", err)
	}
	if err := desktop.DeliverInBackground(executable, WatchCommand, alert); err != nil {
		return err
	}

	// Info, а не Debug: по этой записи отличают уведомление хука от
	// собственных уведомлений Claude Code — со стороны они неразличимы
	n.logger.Info("alert delivered",
		"event", eventName,
		"project", projectName,
		"activate_pids", len(alert.ActivatePIDs),
	)
	return nil
}

// buildAlert собирает оповещение под конкретное событие
func (n *Notifier) buildAlert(eventName string, input *core.ToolInput, projectName string) (desktop.Alert, string, error) {
	alert := desktop.Alert{
		AppName:     "Claude Code",
		Icon:        "utilities-terminal",
		Sound:       n.sound,
		Desktop:     n.desktop,
		ActionLabel: "Перейти к окну",
	}

	if n.activateOnClick {
		// Окно принадлежит одному из предков: сам хук окна не имеет
		ancestors, err := desktop.ProcessAncestors(os.Getpid())
		if err != nil {
			// Best effort: собранная часть цепочки — настоящие PID, окно может
			// быть у них; без окна клик просто ничего не активирует
			n.logger.Warn("process ancestry incomplete", "error", err)
		}
		alert.ActivatePIDs = ancestors
	}

	var terminalTitle string

	switch eventName {
	case core.EventStop:
		terminalTitle = fmt.Sprintf("✅ %s · готово", projectName)
		alert.Title = "Claude Code завершил работу"
		alert.Message = "Проект: " + projectName
		alert.Timeout = stopTimeout

	case core.EventNotification:
		terminalTitle = fmt.Sprintf("🟡 %s · ждёт ответа", projectName)
		alert.Title = fmt.Sprintf("Claude Code ждёт ответа (%s)", projectName)
		// Claude Code сообщает, чего именно ждёт: разрешения на инструмент или ввода
		alert.Message = input.Message
		if alert.Message == "" {
			alert.Message = "Проект: " + projectName
		}
		alert.Timeout = notificationTimeout

	default:
		return desktop.Alert{}, "", fmt.Errorf("notifier does not handle event %q", eventName)
	}

	return alert, terminalTitle, nil
}

// ProjectName определяет имя проекта по каталогу, где запущена сессия: его
// кодирует каталог транскрипта. Текущий каталог (cwd) агент меняет по ходу
// работы, поэтому он только запасной вариант
func (n *Notifier) ProjectName(input *core.ToolInput) string {
	if transcriptPath := input.TranscriptPath; transcriptPath != "" {
		encoded := filepath.Base(filepath.Dir(transcriptPath))
		if dir := decodeProjectDir(encoded); dir != "" {
			return core.ProjectNameForDir(dir)
		}
	}

	if input.CWD != "" {
		return core.ProjectNameForDir(input.CWD)
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
// Так различаются ~/work/projecta/frontend/admin-app и ~/work/claude-hooks
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

		// Каталога с таким именем нет (снесён или переименован): считаем дефис разделителем
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
