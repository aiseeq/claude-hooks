package questioncheck

import (
	"reflect"
	"testing"
)

func TestQuestions(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "прямой вопрос",
			text: "Копии больше нет. Удалить основную страницу?",
			want: []string{"Удалить основную страницу?"},
		},
		{
			name: "вопрос в блоке кода не вопрос",
			text: "Черновик Ивану:\n```\nКакие пары там нужны?\n```\nОтправляю как есть.",
		},
		{
			name: "незакрытый блок кода тянется до конца",
			text: "Пример:\n```\nДелать?\n",
		},
		{
			name: "вопрос во вставке кода не вопрос",
			text: "Флаг `--dry-run?` не нужен.",
		},
		{
			name: "пересказ чужой реплики не вопрос",
			text: "Петров спрашивает: системный блок тогда отображаться не должен, правильно?",
		},
		{
			name: "пересказ с запятой не вопрос",
			text: "Олег пишет, что пайплайн упал: из-за переменных?",
		},
		{
			name: "цитата в ёлочках не вопрос",
			text: "Ты спросил «а это вообще имеет значение?» — имеет.",
		},
		{
			name: "цитата в кавычках не вопрос",
			text: `Пользователь: "почему merge требует разрешения?" Ответил по правилу.`,
		},
		{
			name: "строка цитаты markdown не вопрос",
			text: "> Делаем так или иначе?\nЭто вопрос Ивана.",
		},
		{
			name: "адрес со знаком вопроса не вопрос",
			text: "Страница https://example.com/a?b=1 обновлена.",
		},
		{
			name: "точка внутри имени файла не режет предложение",
			text: "Он распространяется на чаты или только на код, как в CLAUDE.md Shop?",
			want: []string{"Он распространяется на чаты или только на код, как в CLAUDE.md Shop?"},
		},
		{
			name: "маркер без вопросительного знака",
			text: "Тесты зелёные. Выкатка на prod ждёт твоего «да».",
			want: []string{"Выкатка на prod ждёт твоего «да»."},
		},
		{
			name: "маркер решения",
			text: "Нужно твоё решение: как считать комиссию.",
			want: []string{"Нужно твоё решение: как считать комиссию."},
		},
		{
			name: "маркер «скажи, если»",
			text: "Скажи, если нужен ещё один прогон.",
			want: []string{"Скажи, если нужен ещё один прогон."},
		},
		{
			name: "пересказ «спросить» не путается с «просит»",
			text: "Спросить Ивана про пары?",
			want: []string{"Спросить Ивана про пары?"},
		},
		{
			name: "вопрос после пересказа в той же строке",
			text: "Иван пишет: «готово». Отвечать ему?",
			want: []string{"Отвечать ему?"},
		},
		{
			name: "без вопроса",
			text: "Готово: коммит abc123, тесты зелёные.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Texts(Questions(tc.text))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Questions(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// Слова для журналов берутся и из текста перед коротким вопросом
func TestQuestionsContext(t *testing.T) {
	questions := Questions("Артефакт Xq7pTz3mKd9LbWv2 work-admin спрашивал 20.10. Удалить?")
	if len(questions) != 1 {
		t.Fatalf("questions = %+v", questions)
	}
	if questions[0].Text != "Удалить?" {
		t.Fatalf("text = %q", questions[0].Text)
	}
	if want := "Артефакт Xq7pTz3mKd9LbWv2 work-admin спрашивал 20.10. Удалить?"; questions[0].Line != want {
		t.Fatalf("context = %q, want %q", questions[0].Line, want)
	}
}
