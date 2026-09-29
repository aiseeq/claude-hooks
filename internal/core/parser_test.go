package core

import "testing"

func TestParseToolInput(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantTool    string
		wantCommand string
	}{
		{
			name:        "Bash",
			payload:     `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`,
			wantTool:    "Bash",
			wantCommand: "ls -la",
		},
		{
			name:        "tool_input в виде строки",
			payload:     `{"tool_name":"Bash","tool_input":"{\"command\":\"pwd\"}"}`,
			wantTool:    "Bash",
			wantCommand: "pwd",
		},
		{
			name:     "другие инструменты не разбираются",
			payload:  `{"tool_name":"Write","tool_input":{"file_path":"/tmp/a.go","command":"x"}}`,
			wantTool: "Write",
		},
		{
			name:     "Stop без tool_input",
			payload:  `{"session_id":"abc","transcript_path":"/tmp/session.jsonl"}`,
			wantTool: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := ParseToolInput([]byte(tt.payload))
			if err != nil {
				t.Fatalf("разбор не удался: %v", err)
			}

			if input.ToolName != tt.wantTool {
				t.Errorf("ToolName = %q, ожидалось %q", input.ToolName, tt.wantTool)
			}
			if input.Command != tt.wantCommand {
				t.Errorf("Command = %q, ожидалось %q", input.Command, tt.wantCommand)
			}
		})
	}
}

func TestParseToolInput_PreservesSessionFields(t *testing.T) {
	input, err := ParseToolInput([]byte(`{"session_id":"s1","cwd":"/home/user/project","transcript_path":"/tmp/t.jsonl","notification_type":"idle_prompt"}`))
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}

	if input.SessionID != "s1" {
		t.Errorf("SessionID = %q", input.SessionID)
	}
	if input.CWD != "/home/user/project" {
		t.Errorf("CWD = %q", input.CWD)
	}
	if input.TranscriptPath != "/tmp/t.jsonl" {
		t.Errorf("TranscriptPath = %q", input.TranscriptPath)
	}
	if input.NotificationType != "idle_prompt" {
		t.Errorf("NotificationType = %q", input.NotificationType)
	}
}

// Вход Stop снят с Claude Code 2.1.284: фоновая команда и будильник CronCreate
func TestParseToolInput_StopBackgroundWork(t *testing.T) {
	input, err := ParseToolInput([]byte(`{
		"session_id":"b302985e","hook_event_name":"Stop","stop_hook_active":false,
		"background_tasks":[{"id":"bl8voolia","type":"shell","status":"running","description":"sleep 40","command":"sleep 40"}],
		"session_crons":[{"id":"2282851f","schedule":"4 17 29 9 *","recurring":false,"prompt":"say ping"}]
	}`))
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}

	if input.BackgroundTasks == nil || len(*input.BackgroundTasks) != 1 {
		t.Fatalf("background_tasks = %+v", input.BackgroundTasks)
	}
	if task := (*input.BackgroundTasks)[0]; task.ID != "bl8voolia" || task.Type != "shell" || task.Status != "running" {
		t.Errorf("задача разобрана неверно: %+v", task)
	}
	if input.SessionCrons == nil || len(*input.SessionCrons) != 1 || (*input.SessionCrons)[0].Recurring {
		t.Errorf("session_crons = %+v", input.SessionCrons)
	}
}

// Пустой список и отсутствующее поле значат разное: «ждать нечего» против
// «реестр задач недоступен»
func TestParseToolInput_StopRegistryPresence(t *testing.T) {
	empty, err := ParseToolInput([]byte(`{"background_tasks":[],"session_crons":[]}`))
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}
	if empty.BackgroundTasks == nil || empty.SessionCrons == nil {
		t.Error("пустые списки должны отличаться от отсутствующих полей")
	}

	missing, err := ParseToolInput([]byte(`{"session_id":"s1"}`))
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}
	if missing.BackgroundTasks != nil || missing.SessionCrons != nil {
		t.Error("отсутствующие поля должны оставаться nil")
	}
}

func TestParseToolInput_InvalidJSON(t *testing.T) {
	if _, err := ParseToolInput([]byte("{not json")); err == nil {
		t.Error("некорректный JSON должен приводить к ошибке")
	}
}
