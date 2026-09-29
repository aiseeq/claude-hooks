package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newJiraStyleTool(t *testing.T) *JiraStyleTool {
	t.Helper()
	tool, err := NewJiraStyleTool(core.ToolConfig{Enabled: true}, testLogger(t))
	if err != nil {
		t.Fatalf("NewJiraStyleTool: %v", err)
	}
	return tool
}

func validateJiraCommand(t *testing.T, command string) *core.ValidationResult {
	t.Helper()
	result, err := newJiraStyleTool(t).ValidateTool(context.Background(), &core.ToolInput{
		ToolName: "Bash",
		Command:  command,
	})
	if err != nil {
		t.Fatalf("ValidateTool: %v", err)
	}
	return result
}

func TestJiraStyleBlocksEmDashInInlinePayload(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -s -X POST --data-binary '{"body":"Готово — выкачено"}' "$JIRA_BASE_URL/rest/api/3/issue/PROJ-123/comment"`)
	if result.IsValid {
		t.Fatal("длинное тире в теле комментария должно блокироваться")
	}
}

func TestJiraStyleBlocksEscapedEmDash(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -X PUT -d '{"text":"a — b"}' https://x.atlassian.net/rest/api/3/issue/PROJ-1/comment/42`)
	if result.IsValid {
		t.Fatal("длинное тире в escape-форме должно блокироваться")
	}
}

func TestJiraStyleBlocksMarkdownInPayloadFile(t *testing.T) {
	payload := filepath.Join(t.TempDir(), "comment.json")
	if err := os.WriteFile(payload, []byte(`{"body":"**Итог:** сделано"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := validateJiraCommand(t,
		`curl -s -X POST --data-binary @`+payload+` "$JIRA_BASE_URL/rest/api/3/issue/PROJ-123/comment"`)
	if result.IsValid {
		t.Fatal("markdown в файле с телом комментария должен блокироваться")
	}
}

func TestJiraStyleAllowsPlainComment(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -s -X POST --data-binary '{"body":"Выкачено на UAT, версия 2.0.123"}' "$JIRA_BASE_URL/rest/api/3/issue/PROJ-123/comment"`)
	if !result.IsValid {
		t.Fatalf("обычный комментарий не должен блокироваться: %+v", result.Violations)
	}
}

func TestJiraStyleIgnoresNonCommentRequests(t *testing.T) {
	result := validateJiraCommand(t,
		`echo "тире — в обычной команде" && curl "$JIRA_BASE_URL/rest/api/3/issue/PROJ-123?fields=summary"`)
	if !result.IsValid {
		t.Fatal("команда без комментария в Jira не должна блокироваться")
	}
}

func TestJiraStyleIgnoresReadingComments(t *testing.T) {
	result := validateJiraCommand(t,
		`curl -s "$JIRA_BASE_URL/rest/api/3/issue/PROJ-123/comment" | python3 render.py # — сверка`)
	if result.IsValid {
		return
	}
	// GET за комментариями не содержит тела, но тире в остальной команде не повод блокировать чтение
	t.Fatal("чтение комментариев не должно блокироваться")
}

// jiraComment собирает команду, постящую комментарий с телом в формате
// Jira REST v2 ({"body": "..."}): текст экранируется как JSON-строка
func jiraComment(t *testing.T, text string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"body": text})
	if err != nil {
		t.Fatal(err)
	}
	return `curl -s -X POST --data-binary '` + string(body) + `' "$JIRA_BASE_URL/rest/api/2/issue/PROJ-7/comment"`
}

// jiraCommentFile пишет тело комментария в файл и собирает команду, которая
// его отправляет
func jiraCommentFile(t *testing.T, adf string) string {
	t.Helper()
	payload := filepath.Join(t.TempDir(), "comment.json")
	if err := os.WriteFile(payload, []byte(adf), 0o600); err != nil {
		t.Fatal(err)
	}
	return `curl -s -X POST --data-binary @` + payload + ` "$JIRA_BASE_URL/rest/api/3/issue/PROJ-7/comment"`
}

func TestJiraStyleBlocksFormViolations(t *testing.T) {
	cases := map[string]string{
		"ёлочки":                    `В карточке «Итог за день» теперь сумма без комиссии`,
		"лапки":                     `Статус “в обработке” больше не залипает`,
		"немецкие кавычки":          `Кнопка „Выгрузить“ появилась у всех ролей`,
		"X, а не Y":                 `Повторный прогон перезаписывает запись, а не копит версии`,
		"X, а не только Y":          `Вкладка открыта всем ролям, а не только админу`,
		"это не X, а Y":             `Это не ошибка расчёта, а реальная просадка стратегии`,
		"не из X, а из Y":           `Форма берёт варианты не из зашитого списка, а из ответа провайдера`,
		"приветствие":               `Привет, выкатил на UAT, можно проверять`,
		"приветствие после имени":   `@Иван Петров привет. Фид ордеров теперь идёт с прода`,
		"добрый день":               `Добрый день! Отчёт за вчера пересчитан`,
		"здравствуйте":              `Здравствуйте, ключ перевыпущен`,
		"числительное":              `Осталось три расхождения в журнале`,
		"числительное с заглавной":  `Две причины занижения закрыты`,
		"числительное в падеже":     `Из двух операций у нас нет только одной`,
		"десяток":                   `Прогон занимает двадцать секунд`,
		"числительное творительный": `Отчёт собирается тремя запросами`,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if result := validateJiraCommand(t, jiraComment(t, text)); result.IsValid {
				t.Fatalf("должно блокироваться: %s", text)
			}
		})
	}
}

func TestJiraStyleBlocksEscapedCyrillicInADF(t *testing.T) {
	// {"type":"text","text":"Две причины закрыты"}, записанный json.dumps
	// без ensure_ascii=False: кириллица уходит в \uXXXX
	adf := `{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[` +
		`{"type":"text","text":"Две причины закрыты"}]}]}}`
	if result := validateJiraCommand(t, jiraCommentFile(t, adf)); result.IsValid {
		t.Fatal("числительное прописью в escape-форме должно блокироваться")
	}
}

func TestJiraStyleBlocksGreetingAfterMentionNode(t *testing.T) {
	adf := `{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[` +
		`{"type":"mention","attrs":{"id":"acc-1","text":"@Иван Петров"}},` +
		`{"type":"text","text":" привет, напомню про задачу"}]}]}}`
	if result := validateJiraCommand(t, jiraCommentFile(t, adf)); result.IsValid {
		t.Fatal("приветствие после упоминания должно блокироваться")
	}
}

func TestJiraStyleNamesWhatToReplace(t *testing.T) {
	result := validateJiraCommand(t, jiraComment(t, `Две причины закрыты, а не одна`))
	joined := strings.Join(result.Suggestions, "\n")
	for _, want := range []string{"Две причины", ", а не "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("подсказка должна называть фрагмент %q: %s", want, joined)
		}
	}
}

// Похожие на нарушения, но допустимые тексты: переписаны с ложных
// срабатываний на реальных комментариях
func TestJiraStyleAllowsLookalikes(t *testing.T) {
	cases := map[string]string{
		"не X, а связка предложений":   `Строка без хэша сопоставиться не может по построению, а нулевая расходы не двигает`,
		"это не глагол, а связка":      `Клиент получает 499, в алерты это не попадает, а настоящий сбой виден как раньше`,
		"не X, а без предлога":         `Поле не пускает дальше недели, а запрос за окном получает отказ`,
		"не из X, а связка":            `Конвертер не возьмёт строку из реестра, а остальные посчитает как обычно`,
		"не в X, а другой предлог":     `Платёж не уходит в обход настроек, а по лимиту отклоняется`,
		"числительное перед союзом":    `Причин было три, и все закрыты`,
		"числительное перед предлогом": `Проверил три из них`,
		"слова с числом внутри":        `Опять просто дважды семинар пятиминутный двадцатый`,
		"цифры":                     `Осталось 3 расхождения, прогон за 20 секунд`,
		"привет не в начале":        `Коллеги из поддержки передали привет, фикс им подошёл`,
		"формат суммы в интерфейсе": `Сумма показана в валюте депозита, например 10,50 EUR`,
		"прямые кавычки":            `Статус "в обработке" больше не залипает`,
		"дефис":                     `Готово - выкачено на UAT`,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if result := validateJiraCommand(t, jiraComment(t, text)); !result.IsValid {
				t.Fatalf("не должно блокироваться: %s: %v", text, result.Suggestions)
			}
		})
	}
}

// testLogger — логгер для тестов пакета: пишет в stderr, который go test
// показывает только при провале
func testLogger(t *testing.T) core.Logger {
	t.Helper()
	logger, err := core.NewLogger(core.LoggerConfig{Level: "debug"})
	if err != nil {
		t.Fatalf("не удалось создать логгер: %v", err)
	}
	return logger
}
