package core

import "testing"

func TestParseToolInput(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		wantTool      string
		wantFilePath  string
		wantContent   string
		wantNewString string
		wantCommand   string
	}{
		{
			name:         "Write",
			payload:      `{"tool_name":"Write","tool_input":{"file_path":"/tmp/a.go","content":"package a"}}`,
			wantTool:     "Write",
			wantFilePath: "/tmp/a.go",
			wantContent:  "package a",
		},
		{
			name:          "Edit",
			payload:       `{"tool_name":"Edit","tool_input":{"file_path":"/tmp/a.go","new_string":"x := 1"}}`,
			wantTool:      "Edit",
			wantFilePath:  "/tmp/a.go",
			wantNewString: "x := 1",
		},
		{
			name:          "MultiEdit объединяет правки",
			payload:       `{"tool_name":"MultiEdit","tool_input":{"file_path":"/tmp/a.go","edits":[{"new_string":"first"},{"new_string":"second"}]}}`,
			wantTool:      "MultiEdit",
			wantFilePath:  "/tmp/a.go",
			wantNewString: "first\nsecond",
		},
		{
			name:        "Bash",
			payload:     `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`,
			wantTool:    "Bash",
			wantCommand: "ls -la",
		},
		{
			name:         "tool_input в виде строки",
			payload:      `{"tool_name":"Write","tool_input":"{\"file_path\":\"/tmp/a.go\",\"content\":\"body\"}"}`,
			wantTool:     "Write",
			wantFilePath: "/tmp/a.go",
			wantContent:  "body",
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
			if input.FilePath != tt.wantFilePath {
				t.Errorf("FilePath = %q, ожидалось %q", input.FilePath, tt.wantFilePath)
			}
			if input.Content != tt.wantContent {
				t.Errorf("Content = %q, ожидалось %q", input.Content, tt.wantContent)
			}
			if input.NewString != tt.wantNewString {
				t.Errorf("NewString = %q, ожидалось %q", input.NewString, tt.wantNewString)
			}
			if input.Command != tt.wantCommand {
				t.Errorf("Command = %q, ожидалось %q", input.Command, tt.wantCommand)
			}
		})
	}
}

func TestParseToolInput_PreservesSessionFields(t *testing.T) {
	input, err := ParseToolInput([]byte(`{"session_id":"s1","cwd":"/home/user/project","transcript_path":"/tmp/t.jsonl","tool_name":"Stop"}`))
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
}

func TestParseToolInput_InvalidJSON(t *testing.T) {
	if _, err := ParseToolInput([]byte("{not json")); err == nil {
		t.Error("некорректный JSON должен приводить к ошибке")
	}
}

func TestCreateFileAnalysis(t *testing.T) {
	analysis := CreateFileAnalysis(&ToolInput{
		ToolName: "Write",
		FilePath: "/project/internal/service_test.go",
		Content:  "package service",
	})

	if analysis == nil {
		t.Fatal("анализ не должен быть пустым")
	}
	if analysis.Extension != ".go" {
		t.Errorf("Extension = %q", analysis.Extension)
	}
	if !analysis.IsTestFile {
		t.Error("файл должен определяться как тестовый")
	}
	if analysis.IsDocsFile {
		t.Error("Go-файл не является документацией")
	}
}

func TestCreateFileAnalysis_UsesNewStringWhenContentEmpty(t *testing.T) {
	analysis := CreateFileAnalysis(&ToolInput{
		ToolName:  "Edit",
		FilePath:  "/project/main.go",
		NewString: "x := 1",
	})

	if analysis.Content != "x := 1" {
		t.Errorf("Content = %q, ожидалось \"x := 1\"", analysis.Content)
	}
}

func TestCreateFileAnalysis_NoFilePath(t *testing.T) {
	if analysis := CreateFileAnalysis(&ToolInput{ToolName: "Bash", Command: "ls"}); analysis != nil {
		t.Error("без пути к файлу анализ не создается")
	}
}
