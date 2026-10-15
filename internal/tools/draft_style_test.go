package tools

import (
	"strings"
	"testing"
)

// fence оборачивает текст в блок кода без языка, как черновик в ответе
func fence(text string) string {
	return "Разбор для пользователя.\n\n```\n" + text + "\n```\n"
}

func TestReviewDraftsFindsFormMarkers(t *testing.T) {
	cases := map[string]string{
		"длинное тире":         "Фикс в тесте — можно проверять",
		"типографские кавычки": "Нажми «Сохранить» на экране выплат",
		"markdown-жирный":      "**Итог:** выкатил на тест",
		"вопрос-проверка":      "Экран поправил, выкатываю в тест. Получилось?",
		"отчёт в начале":       "Нашёл. Плашка мерилась по ширине кнопки, поправил",
		"отчёт после @":        "@Ivan Petrov понял. Делаю так, как ты написал",
		"срок датой":           "Экран в админке делаю, на UAT выложу к 28.09",
		"обещание на завтра":   "Ага, сделаю, скину завтра",
		"оборот X, а не Y":     "Комиссия вычитается из курса, а не из суммы",
	}
	for name, draft := range cases {
		t.Run(name, func(t *testing.T) {
			if ReviewDrafts(fence(draft)) == "" {
				t.Errorf("черновик %q должен получить подсказку", draft)
			}
		})
	}
}

func TestReviewDraftsPassesCleanDraft(t *testing.T) {
	clean := []string{
		"Фикс в тесте. Вместе с двумя тикетами: PROJ-13 и PROJ-14",
		"Привет. Скинь, пожалуйста, код от namecheap",
		"Тикеты при выкатке на UAT сейчас назначаются на тебя. Переключить на Александра (QA)?",
		"Можно выкатывать обновления на прод? Ок?",
	}
	for _, draft := range clean {
		if hint := ReviewDrafts(fence(draft)); hint != "" {
			t.Errorf("чистый черновик %q получил подсказку:\n%s", draft, hint)
		}
	}
}

// Приветствие запрещено только в Jira: в личке «Привет.» — норма
func TestReviewDraftsAllowsGreeting(t *testing.T) {
	if hint := ReviewDrafts(fence("Привет. Тикеты на UAT теперь идут на QA")); hint != "" {
		t.Errorf("приветствие в черновике для чата не нарушение:\n%s", hint)
	}
}

func TestReviewDraftsSkipsNonDrafts(t *testing.T) {
	messages := map[string]string{
		"текст вне блока":   "Готово — выкатил. Получилось?",
		"блок с языком":     "```bash\necho 'Готово — выкатил'\n```",
		"команда в блоке":   "```\ngit commit -m \"фикс — выкатка\"\n```",
		"английский блок":   "```\nThe build failed — see logs for the stack trace\n```",
		"короткое значение": "```\nк 28.09\n```",
	}
	for name, message := range messages {
		t.Run(name, func(t *testing.T) {
			if hint := ReviewDrafts(message); hint != "" {
				t.Errorf("не черновик получил подсказку:\n%s", hint)
			}
		})
	}
}

func TestReviewDraftsQuotesFragment(t *testing.T) {
	hint := ReviewDrafts(fence("Фикс в тесте — можно проверять") + fence("Нашёл. Всё сходится"))
	for _, want := range []string{"длинное тире", "отчёт о своём действии", "write-as-user"} {
		if !strings.Contains(hint, want) {
			t.Errorf("в подсказке нет %q:\n%s", want, hint)
		}
	}
}
