package notifier

import (
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func tasks(items ...core.BackgroundTask) *[]core.BackgroundTask { return &items }

func crons(items ...core.SessionCron) *[]core.SessionCron { return &items }

func TestDecide_Stop(t *testing.T) {
	shell := core.BackgroundTask{ID: "b1", Type: "shell", Status: "running"}
	wakeup := core.SessionCron{ID: "c1"}

	tests := []struct {
		name        string
		input       core.ToolInput
		previous    core.SessionState
		activeTasks int
		want        Decision
	}{
		{
			name:        "живая фоновая задача: пауза без звонка",
			input:       core.ToolInput{BackgroundTasks: tasks(shell), SessionCrons: crons()},
			previous:    core.StateWorking,
			activeTasks: 1,
			want:        Decision{State: core.StatePaused},
		},
		{
			name:     "взведённый будильник: пауза без звонка",
			input:    core.ToolInput{BackgroundTasks: tasks(), SessionCrons: crons(wakeup)},
			previous: core.StateWorking,
			want:     Decision{State: core.StatePaused},
		},
		{
			name:        "брошенная задача не держит паузу",
			input:       core.ToolInput{BackgroundTasks: tasks(shell), SessionCrons: crons()},
			previous:    core.StateWorking,
			activeTasks: 0,
			want:        Decision{State: core.StateDone, Alert: true},
		},
		{
			name:     "завершение работы",
			input:    core.ToolInput{BackgroundTasks: tasks(), SessionCrons: crons()},
			previous: core.StateWorking,
			want:     Decision{State: core.StateDone, Alert: true},
		},
		{
			// Последняя фоновая задача отчиталась, Claude закончил
			name:     "завершение после паузы",
			input:    core.ToolInput{BackgroundTasks: tasks(), SessionCrons: crons()},
			previous: core.StatePaused,
			want:     Decision{State: core.StateDone, Alert: true},
		},
		{
			name:     "повторная остановка без работы",
			input:    core.ToolInput{BackgroundTasks: tasks(), SessionCrons: crons()},
			previous: core.StateDone,
			want:     Decision{State: core.StateDone},
		},
		{
			// Состояние не прочиталось: лишний звонок лучше пропущенного
			name:     "предыдущее состояние неизвестно: звать",
			input:    core.ToolInput{BackgroundTasks: tasks(), SessionCrons: crons()},
			previous: core.StateUnknown,
			want:     Decision{State: core.StateDone, Alert: true},
		},
		{
			name:     "реестр задач не передан: звать",
			input:    core.ToolInput{},
			previous: core.StateWorking,
			want:     Decision{State: core.StateDone, Alert: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			got := Decide(Event{Name: core.EventStop, Input: &input, Previous: tt.previous, ActiveTasks: tt.activeTasks})
			if got.State != tt.want.State || got.Alert != tt.want.Alert {
				t.Errorf("получено %+v, ожидалось состояние %q, звонок %v", got, tt.want.State, tt.want.Alert)
			}
			if !got.Alert && got.Reason == "" {
				t.Error("причина молчания должна быть названа для лога")
			}
		})
	}
}

func TestDecide_Notification(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		previous core.SessionState
		want     Decision
	}{
		{name: "запрос разрешения посреди работы", kind: "permission_prompt", previous: core.StateWorking, want: Decision{State: core.StateWaiting, Alert: true}},
		{name: "запрос разрешения при фоновых задачах", kind: "permission_prompt", previous: core.StatePaused, want: Decision{State: core.StateWaiting, Alert: true}},
		{name: "повторный вопрос", kind: "permission_prompt", previous: core.StateWaiting, want: Decision{State: core.StateWaiting}},
		{name: "напоминание при фоновых задачах глушится", kind: "idle_prompt", previous: core.StatePaused, want: Decision{}},
		{name: "напоминание после завершения", kind: "idle_prompt", previous: core.StateDone, want: Decision{State: core.StateWaiting}},
		{name: "напоминание без Stop", kind: "idle_prompt", previous: core.StateWorking, want: Decision{State: core.StateWaiting, Alert: true}},
		{name: "вопрос при неизвестном предыдущем состоянии", kind: "permission_prompt", previous: core.StateUnknown, want: Decision{State: core.StateWaiting, Alert: true}},
		{name: "напоминание при неизвестном предыдущем состоянии", kind: "idle_prompt", previous: core.StateUnknown, want: Decision{State: core.StateWaiting, Alert: true}},
		{name: "диалог MCP", kind: "elicitation_dialog", previous: core.StateWorking, want: Decision{State: core.StateWaiting, Alert: true}},
		{name: "успешный вход не зовёт", kind: "auth_success", previous: core.StateWorking, want: Decision{}},
		{name: "продолжение после лимита — работа", kind: "quota_auto_resume_fired", previous: core.StateDone, want: Decision{State: core.StateWorking}},
		{name: "неизвестный тип считается вопросом", kind: "brand_new_type", previous: core.StateWorking, want: Decision{State: core.StateWaiting, Alert: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(Event{
				Name:     core.EventNotification,
				Input:    &core.ToolInput{NotificationType: tt.kind, Message: "text"},
				Previous: tt.previous,
			})
			if got.State != tt.want.State || got.Alert != tt.want.Alert {
				t.Errorf("получено %+v, ожидалось состояние %q, звонок %v", got, tt.want.State, tt.want.Alert)
			}
			if !got.Alert && got.Reason == "" {
				t.Error("причина молчания должна быть названа для лога")
			}
		})
	}
}
