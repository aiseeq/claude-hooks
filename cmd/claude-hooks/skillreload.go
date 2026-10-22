package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/skillreload"
)

// Имена событий в hookSpecificOutput: по ним Claude Code понимает, к какому
// событию относится additionalContext
const (
	eventUserPromptSubmit = "UserPromptSubmit"
	eventPostToolBatch    = "PostToolBatch"
)

// skillReloadTool — ключ в config.yaml. Отсутствие ключа — включено:
// установленный config.yaml при обновлении не перезаписывается, а
// напоминание ничего не блокирует
const skillReloadTool = "skill_reload"

// skillScanBudget — сколько времени событие тратит на чтение транскрипта.
// Таймаут хука 3 секунды; первое чтение сессии в сотни МБ растягивается на
// несколько событий, напоминания ждут, пока транскрипт не дочитан
const skillScanBudget = time.Second

// hookContextOutput — ответ хука с текстом для контекста агента
type hookContextOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// remindStaleSkills кладёт в контекст агента правки скиллов и файлов
// инструкций, сделанные после того, как их текст попал в контекст: diff, а
// при большой правке — просьбу перечитать. Сбой только логируется: выдача не
// повод показывать человеку ошибку хука на каждом ходу
func remindStaleSkills(eventName string, input *core.ToolInput, config *core.Config, logger core.Logger) {
	if tool, exists := config.Tools[skillReloadTool]; exists && !tool.Enabled {
		return
	}
	if input.SessionID == "" || input.TranscriptPath == "" {
		return
	}
	stateDir, err := skillreload.DefaultStateDir()
	if err != nil {
		logger.Warn("skill reload state dir unavailable", "error", err)
		return
	}

	checker := skillreload.Checker{StateDir: stateDir, ScanBudget: skillScanBudget}
	result, err := checker.Check(skillreload.Event{
		SessionID:      input.SessionID,
		AgentID:        input.AgentID,
		AgentType:      input.AgentType,
		TranscriptPath: input.TranscriptPath,
	})
	if err != nil {
		logger.Warn("skill reload check failed", "hook", eventName, "session", input.SessionID, "agent", input.AgentID, "error", err)
		return
	}
	for _, warning := range result.Warnings {
		logger.Warn("skill reload check degraded", "hook", eventName, "session", input.SessionID, "agent", input.AgentID, "error", warning)
	}
	if len(result.Notices) == 0 {
		return
	}

	names := make([]string, 0, len(result.Notices))
	diffs := 0
	for i := range result.Notices {
		names = append(names, result.Notices[i].Name)
		if result.Notices[i].Diff != "" {
			diffs++
		}
	}
	logger.Info("skill reload notified", "hook", eventName, "session", input.SessionID, "agent", input.AgentID, "files", names, "diffs", diffs)

	var output hookContextOutput
	output.HookSpecificOutput.HookEventName = eventName
	output.HookSpecificOutput.AdditionalContext = skillreload.Format(result.Notices)
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		logger.Warn("skill reload reminder not printed", "error", err)
	}
}

func newSkillScanCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "skill-scan [--days N] [корень транскриптов]",
		Short: "Сухой прогон выдачи правок скиллов и CLAUDE.md, сделанных после загрузки в контекст",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := ""
			if len(args) > 0 {
				root = args[0]
			} else {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				root = filepath.Join(home, ".claude", "projects")
			}
			now := time.Now()
			result, err := skillreload.DryRun(cmd.Context(), root, days, now)
			if err != nil {
				return err
			}
			skillreload.WriteDryRun(cmd.OutOrStdout(), result)

			preview, err := skillreload.PreviewInstall(root, now.Add(-24*time.Hour))
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout())
			skillreload.WritePreview(cmd.OutOrStdout(), preview)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "сколько последних дней включить")
	return cmd
}
