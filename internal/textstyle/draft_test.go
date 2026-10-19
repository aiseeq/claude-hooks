package textstyle

import (
	"slices"
	"testing"
)

func descriptions(findings []Finding) []string {
	var names []string
	for _, f := range findings {
		names = append(names, f.Marker.Description)
	}
	return names
}

func TestCheckDraftFindsFormViolations(t *testing.T) {
	cases := map[string]string{
		"Готово — выкачено":                EmDash.Description,
		"Готово – выкачено":                EnDash.Description,
		"**Итог:** сделано":                Bold.Description,
		"## Итог":                          Heading.Description,
		"| Что | Кто |":                    MarkdownTable.Description,
		"Смотри [тикет](https://x/PROJ-1)": MarkdownLink.Description,
		"Статус «в работе»":                TypographicQuotes.Description,
		"Перезаписывает, а не дописывает":  ButNot.Description,
		"Это не баг кода, а данные":        ThisIsNotBut.Description,
		"Не из списка, а из ответа":        RepeatedPreposition.Description,
		"Нашлось три расхождения":          NumberWord.Description,
		"Быстрее в 2,5 раза":               DecimalComma.Description,
		"Выкатил 🚀":                        Emoji.Description,
		"Отличный вопрос! Лимит сброшен":   Preamble.Description,
		"Дай знать, если что":              Preamble.Description,
		"Нашёл. Лимит сбрасывается в 4:00": ReportOpening.Description,
	}
	for text, want := range cases {
		got := descriptions(CheckDraft(text))
		if !slices.Contains(got, want) {
			t.Errorf("%q: нет маркера %q, найдено %v", text, want, got)
		}
	}
}

// Его обычные реплики проходят: приветствие в личке, дефис, смайл, числа
// цифрами, точка в дроби, перечень строками "- "
func TestCheckDraftAllowsHisVoice(t *testing.T) {
	draft := "Привет. Выкатил в тест - глянь, когда будет минутка =)\n" +
		"- лимит 1 платёж в день\n" +
		"- комиссия 1.5%, было 3%\n" +
		"Ок, тогда оставляю. Завтра на встрече как раз и зафиксим\n" +
		"PROJ-12, PROJ-13 и PROJ-14 посмотрю до созвона"
	if findings := CheckDraft(draft); len(findings) > 0 {
		t.Fatalf("ложные срабатывания: %+v", findings)
	}
}

func TestCheckDraftReportsLineNumbers(t *testing.T) {
	findings := CheckDraft("Ок\nГотово — выкачено\n\nНашлось три расхождения")
	var lines []int
	for _, f := range findings {
		lines = append(lines, f.Line)
	}
	if !slices.Equal(lines, []int{2, 4}) {
		t.Fatalf("строки нарушений %v, нужно [2 4]", lines)
	}
}
