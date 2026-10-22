package questioncheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// JudgePrompt — промпт судьи из сухого прогона 22.10
// (~/.claude/improve/runs/2026-10-22/question-checker-dryrun.md) и абзац о
// выдержках из журналов, которых у судьи прогона не было
const JudgePrompt = `Ты проверяешь финальный ответ агента пользователю до показа. Пользователь — техлид: решает приоритеты, деньги, прод, тексты от своего имени, доступы и правила для агентов; продуктовые и бизнес-решения (что видит оператор, тарифы, комиссии) принимают заказчик, операторы, дефайщики.

Найди в ответе вопросы и просьбы к пользователю, включая «Делать?», «ждёт твоего да», «нужно твоё решение», «скажи, если». Цитата чужого вопроса и пример — не вопрос пользователю; вопрос коллеге в черновике — проверяй так же.

НУЖНЫЙ: намерение, вкус или приоритет пользователя, которых нет в его прошлой реплике; то, что видит только он (физический мир, его аккаунты); «да» на прод, деньги, удаление, текст наружу, правку хуков, settings и правил, расширение доступа.
ЛИШНИЙ: ответ есть в прошлой реплике или в самом ответе; факт о мире (кто, где, сколько, есть ли, что решили раньше), который агент может найти в коде, тикетах, чатах, логах, журнале решений и реплик; технический выбор в зоне агента (как сделать, в каком порядке, кто из агентов); продуктовый вопрос не к нему; повтор вопроса, на который уже ответили.

Ниже, кроме ответа, — выдержки, которые агент мог найти сам: прошлые реплики пользователя (~/.claude/user-replies.md) и записи журнала решений (~/.claude/decisions.md), найденные по словам вопроса. Если ответ на вопрос там есть, вопрос ЛИШНИЙ — в "where" укажи файл и время реплики или slug записи. Выдержка не по теме ничего не доказывает.

Ответ — одна строка JSON: {"verdict":"pass"|"block","lishnie":[{"quote":"…","why":"…","where":"где искать"}]}. block — только если есть хотя бы один ЛИШНИЙ.`

// Case — то, что судья видит о сообщении
type Case struct {
	HumanReply string
	Answer     string
	Excerpts   Excerpts
}

// Финальный ответ агента режется до этого размера: судье хватает вопросов и
// контекста вокруг них, а длинный отчёт растит цену и задержку
const answerRunes = 12000

// humanReplyRunes — сколько прошлой реплики пользователя видит судья: длинная
// реплика — обычно вставленный лог или чужой текст
const humanReplyRunes = 3000

// Input — текст для судьи после промпта
func (c Case) Input() string {
	var b strings.Builder
	b.WriteString("[Прошлая реплика пользователя]\n")
	if c.HumanReply == "" {
		b.WriteString("(не найдена)\n")
	} else {
		b.WriteString(truncateRunes(c.HumanReply, humanReplyRunes))
		b.WriteString("\n")
	}
	b.WriteString("\n[Журнал реплик пользователя: выдержки по словам вопроса]\n")
	if len(c.Excerpts.Replies) == 0 {
		b.WriteString("(ничего не найдено)\n")
	}
	for _, excerpt := range c.Excerpts.Replies {
		b.WriteString(excerpt)
		b.WriteString("\n\n")
	}
	b.WriteString("\n[Журнал решений: записи по словам вопроса]\n")
	if len(c.Excerpts.Decisions) == 0 {
		b.WriteString("(ничего не найдено)\n")
	}
	for _, excerpt := range c.Excerpts.Decisions {
		b.WriteString(excerpt)
		b.WriteString("\n\n")
	}
	b.WriteString("\n[Финальный ответ агента]\n")
	b.WriteString(truncateRunes(c.Answer, answerRunes))
	b.WriteString("\n")
	return b.String()
}

// Finding — лишний вопрос по мнению судьи
type Finding struct {
	Quote string `json:"quote"`
	Why   string `json:"why"`
	Where string `json:"where"`
}

// Verdict — ответ судьи
type Verdict struct {
	Verdict string    `json:"verdict"`
	Lishnie []Finding `json:"lishnie"`
}

// Block — судья нашёл лишний вопрос
func (v Verdict) Block() bool {
	return v.Verdict == "block"
}

// Usage — токены вызова: по ним итог недели считает цену по ценам модели
type Usage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  int `json:"cache_write_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens"`
}

// Judge — судья: модель, которой отдают промпт и случай
type Judge interface {
	Model() string
	Judge(ctx context.Context, input string) (Verdict, Usage, error)
}

// ParseVerdict достаёт вердикт из ответа модели: JSON-объект, возможно в
// блоке кода или с текстом вокруг. Вердикт вне pass/block и block без
// единого лишнего вопроса — ошибка: такой ответ не повод останавливать агента
func ParseVerdict(text string) (Verdict, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return Verdict{}, fmt.Errorf("no JSON object in judge answer: %q", truncateRunes(text, 200))
	}
	var verdict Verdict
	if err := json.Unmarshal([]byte(text[start:end+1]), &verdict); err != nil {
		return Verdict{}, fmt.Errorf("judge answer is not a verdict: %w", err)
	}
	switch verdict.Verdict {
	case "pass":
		return verdict, nil
	case "block":
		if len(verdict.Lishnie) == 0 {
			return Verdict{}, fmt.Errorf("judge blocked without naming a redundant question")
		}
		return verdict, nil
	default:
		return Verdict{}, fmt.Errorf("unknown verdict %q", verdict.Verdict)
	}
}

// CLIJudge зовёт модель через её CLI: opus — `claude -p` без инструментов и
// со своим системным промптом, sol и luna — `codex exec` в пустом каталоге
// без пользовательского конфига. Подписки те же, что у агентов
type CLIJudge struct {
	// Name — имя модели из ~/.config/models/approved (opus, sol, luna), ID — её id там
	Name string
	ID   string
	// Effort — усилие рассуждения (low, medium, high)
	Effort string
	// Dir — пустой каталог запуска: в нём нет CLAUDE.md и AGENTS.md
	Dir string
}

// Model — имя модели для журнала
func (j CLIJudge) Model() string {
	return j.Name + "/" + j.Effort
}

// Judge отдаёт модели промпт и случай и разбирает вердикт
func (j CLIJudge) Judge(ctx context.Context, input string) (Verdict, Usage, error) {
	if j.Name == "opus" {
		return j.judgeClaude(ctx, input)
	}
	return j.judgeCodex(ctx, input)
}

// judgeEnv — окружение вызова без признаков сессии-родителя: вложенный
// claude иначе принял бы чужое имя сессии и CLAUDE_PID
func judgeEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_NAME=") || strings.HasPrefix(kv, "CLAUDE_PID=") ||
			strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") || strings.HasPrefix(kv, "CLAUDECODE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func (j CLIJudge) judgeClaude(ctx context.Context, input string) (Verdict, Usage, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--model", j.Name,
		"--system-prompt", JudgePrompt,
		"--tools", "",
		"--setting-sources", "",
		"--strict-mcp-config",
		"--no-session-persistence",
		"--disable-slash-commands",
		"--effort", j.Effort,
		"--output-format", "json")
	out, err := runCLI(cmd, j.Dir, input)
	if err != nil {
		return Verdict{}, Usage{}, err
	}
	var result struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Usage   struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return Verdict{}, Usage{}, fmt.Errorf("claude output is not JSON: %w", err)
	}
	usage := Usage{
		InputTokens:       result.Usage.InputTokens,
		CachedInputTokens: result.Usage.CacheReadInputTokens,
		CacheWriteTokens:  result.Usage.CacheCreationInputTokens,
		OutputTokens:      result.Usage.OutputTokens,
	}
	if result.IsError {
		return Verdict{}, usage, fmt.Errorf("claude returned an error: %s", truncateRunes(result.Result, 300))
	}
	verdict, err := ParseVerdict(result.Result)
	return verdict, usage, err
}

func (j CLIJudge) judgeCodex(ctx context.Context, input string) (Verdict, Usage, error) {
	cmd := exec.CommandContext(ctx, "codex", "exec",
		"--json", "--skip-git-repo-check", "--ephemeral",
		"--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only",
		"-m", j.ID,
		"-c", fmt.Sprintf("model_reasoning_effort=%q", j.Effort),
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
		"-")
	out, err := runCLI(cmd, j.Dir, JudgePrompt+"\n\nКоманд не запускай и файлов не читай: всё нужное ниже.\n\n"+input)
	if err != nil {
		return Verdict{}, Usage{}, err
	}
	var message string
	var usage Usage
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				InputTokens       int `json:"input_tokens"`
				CachedInputTokens int `json:"cached_input_tokens"`
				OutputTokens      int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch {
		case event.Type == "item.completed" && event.Item.Type == "agent_message":
			message = event.Item.Text
		case event.Type == "turn.completed":
			// Кэш codex входит в input_tokens: некэшированная часть — разность
			usage.InputTokens += event.Usage.InputTokens - event.Usage.CachedInputTokens
			usage.CachedInputTokens += event.Usage.CachedInputTokens
			// reasoning_output_tokens — часть output_tokens, не добавка
			usage.OutputTokens += event.Usage.OutputTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return Verdict{}, usage, fmt.Errorf("read codex output: %w", err)
	}
	verdict, err := ParseVerdict(message)
	return verdict, usage, err
}

// runCLI запускает вызов с текстом на stdin; ошибка несёт хвост stderr
func runCLI(cmd *exec.Cmd, dir, stdin string) ([]byte, error) {
	cmd.Dir = dir
	cmd.Env = judgeEnv()
	// По таймауту контекст убивает сам CLI, а его потомки держат stdout:
	// без WaitDelay хук ждал бы их и выходил за свой таймаут
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", cmd.Args[0], err, truncateRunes(strings.TrimSpace(stderr.String()), 300))
	}
	return out, nil
}
