package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/questioncheck"
)

// questionCheckTool — ключ в config.yaml. Отсутствие ключа — включено:
// включает проверку регистрация подкоманды в settings.json, а установленный
// config.yaml при обновлении не перезаписывается
const questionCheckTool = "question_check"

// Судья выбран прогоном на 20 размеченных примерах сухого прогона 22.10
// (claude-hooks question-bench, 11 лишних, 7 нужных, 2 цитаты): opus/high
// поймал 7 и 6 из 11 в двух прогонах, ложно не остановил ни одного нужного;
// sol/low — 8 из 11, но 2 из 7 нужных; luna/high — 6 из 11 и 1 из 7
const (
	judgeModel  = "opus"
	judgeEffort = "high"
)

// judgeBudget — сколько ждать судью. Таймаут хука в settings.json — 60 с:
// запас на чтение журналов и хвоста транскрипта
const judgeBudget = 40 * time.Second

// stopBlockOutput — ответ Stop-хука, который не даёт ходу закончиться:
// агент получает reason и продолжает
type stopBlockOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// runQuestionCheck проверяет вопросы пользователю в последнем сообщении.
// Сбои только логируются: конец хода пропускается, человек получает ответ
func runQuestionCheck(ctx context.Context, input *core.ToolInput, config *core.Config, logger core.Logger, out io.Writer) {
	if tool, exists := config.Tools[questionCheckTool]; exists && !tool.Enabled {
		return
	}
	checker, err := newQuestionChecker(logger)
	if err != nil {
		logger.Warn("question check unavailable", "error", err)
		return
	}
	result := checker.Run(ctx, questioncheck.Event{
		SessionID:      input.SessionID,
		TranscriptPath: input.TranscriptPath,
		AgentID:        input.AgentID,
		StopHookActive: input.StopHookActive,
		LastMessage:    input.LastAssistantMessage,
		ClaudePID:      os.Getenv("CLAUDE_PID"),
	})
	if result.Skip != "" {
		logger.Debug("question check skipped", "session", input.SessionID, "reason", result.Skip)
		return
	}
	if !result.Block {
		logger.Info("question check passed", "session", input.SessionID)
		return
	}
	logger.Info("question check blocked stop", "session", input.SessionID)
	if err := json.NewEncoder(out).Encode(stopBlockOutput{Decision: "block", Reason: result.Reason}); err != nil {
		logger.Warn("question check block not printed", "error", err)
	}
}

// newQuestionChecker собирает проверку с путями по умолчанию
func newQuestionChecker(logger core.Logger) (questioncheck.Checker, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return questioncheck.Checker{}, fmt.Errorf("cannot resolve home dir: %w", err)
	}
	journalPath, err := questioncheck.DefaultJournalPath()
	if err != nil {
		return questioncheck.Checker{}, err
	}
	judge, err := newJudge(home, judgeModel, judgeEffort)
	if err != nil {
		return questioncheck.Checker{}, err
	}
	claudeDir := filepath.Join(home, ".claude")
	return questioncheck.Checker{
		ClaudeDir:   claudeDir,
		Targets:     questioncheck.DefaultTargets(home),
		Journals:    questioncheck.DefaultJournals(claudeDir),
		Judge:       judge,
		JournalPath: journalPath,
		Now:         time.Now,
		Budget:      judgeBudget,
		Warn:        logger.Warn,
	}, nil
}

// newJudge — судья из списка одобренных моделей, запускаемый в пустом
// каталоге времени выполнения: там нет CLAUDE.md и AGENTS.md
func newJudge(home, model, effort string) (questioncheck.CLIJudge, error) {
	dir := filepath.Join(core.RuntimeDir(), "question-judge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return questioncheck.CLIJudge{}, fmt.Errorf("create judge dir: %w", err)
	}
	return questioncheck.NewCLIJudge(filepath.Join(home, ".config", "models", "approved"), model, effort, dir)
}

// benchCase — размеченный пример: ответ агента, прошлая реплика пользователя,
// время ответа (журналы режутся по нему) и метка разметки
type benchCase struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"ts"`
	HumanReply string    `json:"prev_reply"`
	Answer     string    `json:"answer"`
	Label      string    `json:"label"`
}

// benchResult — вердикт по примеру
type benchResult struct {
	ID        string                  `json:"id"`
	Label     string                  `json:"label"`
	Filtered  bool                    `json:"filtered"`
	Questions []string                `json:"questions"`
	Verdict   string                  `json:"verdict"`
	Lishnie   []questioncheck.Finding `json:"lishnie,omitempty"`
	Error     string                  `json:"error,omitempty"`
	LatencyMS int64                   `json:"latency_ms"`
	Usage     questioncheck.Usage     `json:"usage"`
	Excerpts  int                     `json:"excerpts"`
}

func newQuestionBenchCmd() *cobra.Command {
	var model, effort string
	cmd := &cobra.Command{
		Use:   "question-bench [--model luna] [--effort low] <примеры.jsonl>",
		Short: "Прогнать судью проверки вопросов по размеченным примерам",
		Long: `Читает JSONL примеров {id, ts, prev_reply, answer, label} и прогоняет каждый
тем же путём, что Stop-хук question-check: фильтр вопросов, выдержки из
журналов реплик и решений (только записи раньше ts), судья. Печатает JSONL
вердиктов. Модель — только из ~/.config/models/approved.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			judge, err := newJudge(home, model, effort)
			if err != nil {
				return err
			}
			journals := questioncheck.DefaultJournals(filepath.Join(home, ".claude"))
			return runQuestionBench(cmd.Context(), args[0], judge, journals, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&model, "model", judgeModel, "модель судьи из ~/.config/models/approved")
	cmd.Flags().StringVar(&effort, "effort", judgeEffort, "усилие рассуждения: low, medium, high")
	return cmd
}

func runQuestionBench(ctx context.Context, path string, judge questioncheck.Judge, journals questioncheck.Journals, out io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open examples: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var example benchCase
		if err := json.Unmarshal(scanner.Bytes(), &example); err != nil {
			return fmt.Errorf("parse example: %w", err)
		}
		result := benchResult{ID: example.ID, Label: example.Label}
		questions := questioncheck.Questions(example.Answer)
		result.Questions = questioncheck.Texts(questions)
		if len(questions) == 0 {
			result.Filtered = true
			result.Verdict = questioncheck.VerdictPass
			if err := encoder.Encode(result); err != nil {
				return fmt.Errorf("print result: %w", err)
			}
			continue
		}
		excerpts, err := journals.Find(questions, example.Time)
		if err != nil {
			return err
		}
		result.Excerpts = len(excerpts.Replies) + len(excerpts.Decisions)
		input := questioncheck.Case{HumanReply: example.HumanReply, Answer: example.Answer, Excerpts: excerpts}.Input()
		judgeCtx, cancel := context.WithTimeout(ctx, judgeBudget)
		started := time.Now()
		verdict, usage, err := judge.Judge(judgeCtx, input)
		cancel()
		result.LatencyMS = time.Since(started).Milliseconds()
		result.Usage = usage
		if err != nil {
			result.Verdict = questioncheck.VerdictError
			result.Error = err.Error()
		} else {
			result.Verdict = verdict.Verdict
			result.Lishnie = verdict.Lishnie
		}
		if err := encoder.Encode(result); err != nil {
			return fmt.Errorf("print result: %w", err)
		}
	}
	return scanner.Err()
}
