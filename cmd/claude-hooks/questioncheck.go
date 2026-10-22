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
	"github.com/aiseeq/claude-hooks/internal/tools/notifier"
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

// reasonAwaitsQuestionCheck — почему Stop-хук не позвонил: звонок отдан
// question-check до вердикта
const reasonAwaitsQuestionCheck = "ждёт question-check"

// runQuestionCheck проверяет вопросы пользователю в последнем сообщении.
// Сбои только логируются: конец хода пропускается, человек получает ответ
func runQuestionCheck(ctx context.Context, input *core.ToolInput, config *core.Config, logger core.Logger, out io.Writer) {
	started := time.Now()
	if !questionCheckEnabled(config) {
		return
	}
	// Тем же решением Stop-хук только что отдал звонок сюда
	awaited := stopAwaitsQuestionCheck(input, config, logger, started)
	checker, err := newQuestionChecker(logger)
	if err != nil {
		logger.Warn("question check unavailable", "error", err)
		if awaited {
			ringAfterQuestionCheck(input, config, logger, started)
		}
		return
	}
	checkStop(ctx, checker, stopCheck{input: input, config: config, logger: logger, out: out, awaited: awaited, started: started})
}

// stopCheck — конец хода, который проверяет question-check. awaited — Stop-хук
// не позвонил и ждёт вердикта; started — когда хук запустился
type stopCheck struct {
	input   *core.ToolInput
	config  *core.Config
	logger  core.Logger
	out     io.Writer
	awaited bool
	started time.Time
}

// checkStop прогоняет проверку и, если Stop-хук отдал звонок, звонит сам на
// всё, кроме остановки: на pass, сбой и таймаут судьи ход кончился, на block
// агент продолжает и звонить рано. Возвращает решение по звонку, nil — не звонил
func checkStop(ctx context.Context, checker questioncheck.Checker, check stopCheck) (questioncheck.Result, *notifier.Decision) {
	input, logger := check.input, check.logger
	result := checker.Run(ctx, questionCheckEvent(input))
	switch {
	case result.Block:
		logger.Info("question check blocked stop", "session", input.SessionID)
		if err := json.NewEncoder(check.out).Encode(stopBlockOutput{Decision: "block", Reason: result.Reason}); err != nil {
			logger.Warn("question check block not printed", "error", err)
		}
		return result, nil
	case result.Skip != "":
		logger.Debug("question check skipped", "session", input.SessionID, "reason", result.Skip)
	default:
		logger.Info("question check let stop through", "session", input.SessionID, "verdict", result.Verdict)
	}
	if !check.awaited {
		return result, nil
	}
	return result, ringAfterQuestionCheck(input, check.config, logger, check.started)
}

// ringAfterQuestionCheck доставляет оповещение конца хода, которое Stop-хук
// отдал проверке. Если за время проверки состояние сессии записал кто-то ещё
// (человек отправил новое сообщение), конец хода уже не новость: звонка нет
func ringAfterQuestionCheck(input *core.ToolInput, config *core.Config, logger core.Logger, started time.Time) *notifier.Decision {
	changed, err := core.SessionStateChangedSince(input.SessionID, started)
	if err != nil {
		// Явный failover: лишний звонок лучше пропущенного
		logger.Warn("session state change unknown, ringing", "session", input.SessionID, "error", err)
	}
	if changed {
		logger.Info("alert muted", "hook", hookQuestionCheck, "session", input.SessionID, "reason", "состояние сессии записано во время проверки")
		return nil
	}
	decision := announceSessionEvent(core.EventStop, input, config, logger)
	return &decision
}

// stopAwaitsQuestionCheck — отдаёт ли Stop-хук звонок question-check. Решает
// questioncheck.Scope.Gate — та же функция, что внутри проверки; сверх неё —
// включена ли проверка в config.yaml и стоит ли она в settings.json. Любая
// неясность — звонит сам Stop-хук
func stopAwaitsQuestionCheck(input *core.ToolInput, config *core.Config, logger core.Logger, now time.Time) bool {
	if !questionCheckEnabled(config) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Warn("question check scope unknown, stop rings itself", "error", err)
		return false
	}
	if defaultScope(home).Gate(now, questionCheckEvent(input)).Status != questioncheck.GateJudge {
		return false
	}
	registered, err := questioncheck.Registered(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		logger.Warn("question check registration unknown, stop rings itself", "error", err)
		return false
	}
	return registered
}

// questionCheckEnabled — отсутствие ключа в config.yaml значит «включено»
func questionCheckEnabled(config *core.Config) bool {
	tool, exists := config.Tools[questionCheckTool]
	return !exists || tool.Enabled
}

// questionCheckEvent — вход проверки из входа Stop-хука
func questionCheckEvent(input *core.ToolInput) questioncheck.Event {
	return questioncheck.Event{
		SessionID:      input.SessionID,
		TranscriptPath: input.TranscriptPath,
		AgentID:        input.AgentID,
		StopHookActive: input.StopHookActive,
		LastMessage:    input.LastAssistantMessage,
		ClaudePID:      os.Getenv("CLAUDE_PID"),
	}
}

// defaultScope — сессии лида и work-admin по записям ~/.claude/sessions
func defaultScope(home string) questioncheck.Scope {
	return questioncheck.Scope{
		ClaudeDir: filepath.Join(home, ".claude"),
		Targets:   questioncheck.DefaultTargets(home),
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
	scope := defaultScope(home)
	return questioncheck.Checker{
		Scope:       scope,
		Journals:    questioncheck.DefaultJournals(scope.ClaudeDir),
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
