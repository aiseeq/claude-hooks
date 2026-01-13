package shared

import (
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// PatternMatch представляет найденное совпадение с паттерном
// CANONICAL VERSION - заменяет дубликаты в BaseValidator, BaseAdvisor, BaseTool
type PatternMatch struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Text    string `json:"text"`
	Pattern string `json:"pattern"`
}

// FindPatternMatches ищет совпадения с паттернами в тексте
// CANONICAL VERSION - заменяет идентичные функции в BaseValidator, BaseAdvisor, BaseTool
func FindPatternMatches(content string, patterns []*regexp.Regexp) []PatternMatch {
	var matches []PatternMatch

	lines := strings.Split(content, "\n")
	for lineNum, line := range lines {
		for _, pattern := range patterns {
			if locations := pattern.FindAllStringIndex(line, -1); len(locations) > 0 {
				for _, loc := range locations {
					match := PatternMatch{
						Line:    lineNum + 1,
						Column:  loc[0] + 1,
						Text:    line[loc[0]:loc[1]],
						Pattern: pattern.String(),
					}
					matches = append(matches, match)
				}
			}
		}
	}

	return matches
}

// CreateViolation создает нарушение из совпадения
// CANONICAL VERSION - заменяет идентичные функции в BaseValidator, BaseAdvisor, BaseTool
func CreateViolation(match PatternMatch, violationType, message, suggestion string, severity core.Level) core.Violation {
	return core.Violation{
		Type:       violationType,
		Message:    message,
		Suggestion: suggestion,
		Line:       match.Line,
		Column:     match.Column,
		Severity:   severity,
	}
}
