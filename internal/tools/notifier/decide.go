package notifier

import (
	"fmt"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// Типы события Notification (hooks.md, раздел Notification)
const (
	TypeIdlePrompt          = "idle_prompt"
	typeQuotaAutoResumeFire = "quota_auto_resume_fired"
)

// waitingTypes — уведомления, после которых сессия стоит, пока человек не
// ответит: запрос разрешения, минутное напоминание, диалог MCP, вопрос
// агента, продолжение после сна по Enter, отказ продолжать после лимита
var waitingTypes = map[string]bool{
	"permission_prompt":          true,
	TypeIdlePrompt:               true,
	"elicitation_dialog":         true,
	"elicitation_url_dialog":     true,
	"agent_needs_input":          true,
	"quota_auto_resume_stale":    true,
	"quota_auto_resume_disabled": true,
}

// informationalTypes — уведомления о том, что уже случилось без участия
// человека: ответа они не ждут, звать не за чем
var informationalTypes = map[string]bool{
	"auth_success":         true,
	"elicitation_complete": true,
	"elicitation_response": true,
	"agent_completed":      true,
}

// Event — событие сессии и всё, что о нём известно к моменту решения
type Event struct {
	// Name — core.EventStop или core.EventNotification
	Name     string
	Input    *core.ToolInput
	Previous core.SessionState
	// ActiveTasks — фоновые задачи, которых Claude ещё ждёт (только Stop):
	// список Claude Code без брошенных, см. core.ActiveBackgroundTasks
	ActiveTasks int
}

// Decision — что сделать по событию
type Decision struct {
	// State — новое состояние сессии; пусто — состояние не меняется
	State core.SessionState
	// Alert — позвать человека звуком и уведомлением
	Alert bool
	// Reason объясняет, почему человека не зовут или что пошло не так;
	// уходит в лог, по нему разбирают «почему не позвонило»
	Reason string
}

// Decide решает, что делать по событию сессии. Функция чистая: всё, что
// нужно, приходит в Event
func Decide(event Event) Decision {
	switch event.Name {
	case core.EventStop:
		return decideStop(event)
	case core.EventNotification:
		return decideNotification(event)
	default:
		return Decision{Reason: "событие не относится к notifier: " + event.Name}
	}
}

// decideStop: остановка при живых фоновых задачах или взведённом будильнике —
// не конец работы, Claude вернётся к ней сам
func decideStop(event Event) Decision {
	input := event.Input
	if input.BackgroundTasks == nil || input.SessionCrons == nil {
		// Claude Code не передал реестр задач: лишний звонок лучше пропущенного
		return finish(core.StateDone, event.Previous, "реестр фоновых задач не передан, считаю, что ждать нечего")
	}

	if event.ActiveTasks > 0 {
		return Decision{State: core.StatePaused, Reason: fmt.Sprintf("живых фоновых задач: %d", event.ActiveTasks)}
	}
	if crons := len(*input.SessionCrons); crons > 0 {
		return Decision{State: core.StatePaused, Reason: fmt.Sprintf("взведённых будильников: %d", crons)}
	}

	return finish(core.StateDone, event.Previous, "")
}

// decideNotification разбирает уведомление по его типу
func decideNotification(event Event) Decision {
	kind := event.Input.NotificationType

	switch {
	case kind == TypeIdlePrompt && event.Previous == core.StatePaused:
		// Минутное напоминание приходит и тогда, когда Claude ждёт свои
		// фоновые задачи; запрос разрешения при этом проходит всегда —
		// без ответа человека сессия встанет
		return Decision{Reason: "сессия ждёт фоновые задачи или будильник"}
	case waitingTypes[kind]:
		return finish(core.StateWaiting, event.Previous, "")
	case kind == typeQuotaAutoResumeFire:
		// Лимит сброшен, Claude продолжает сам: это работа, а не ожидание
		return Decision{State: core.StateWorking, Reason: "работа продолжена после лимита"}
	case informationalTypes[kind]:
		return Decision{Reason: "уведомление не ждёт ответа: " + kind}
	default:
		// Новый тип Claude Code: лишний звонок лучше пропущенного вопроса
		return finish(core.StateWaiting, event.Previous, fmt.Sprintf("неизвестный notification_type %q, считаю ожиданием ответа", kind))
	}
}

// finish переводит сессию в состояние, где она ждёт человека. Звать нужно
// только на переходе из работы: пока Claude ждёт, Claude Code напоминает о
// себе тем же событием, а человека уже позвали один раз
func finish(state core.SessionState, previous core.SessionState, note string) Decision {
	decision := Decision{State: state, Reason: note}
	if previous == core.StateWorking || previous == core.StatePaused {
		decision.Alert = true
		return decision
	}
	if note == "" {
		decision.Reason = "человека уже позвали: предыдущее состояние " + string(previous)
	}
	return decision
}
