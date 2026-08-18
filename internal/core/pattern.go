package core

import (
	"regexp"
	"strings"
)

// PatternMatch представляет найденное совпадение с паттерном
type PatternMatch struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Text    string `json:"text"`
	Pattern string `json:"pattern"`
}

// FindPatternMatches ищет совпадения с паттернами в тексте
func FindPatternMatches(content string, patterns []*regexp.Regexp) []PatternMatch {
	var matches []PatternMatch

	lines := strings.Split(content, "\n")
	for lineNum, line := range lines {
		for _, pattern := range patterns {
			for _, loc := range pattern.FindAllStringIndex(line, -1) {
				matches = append(matches, PatternMatch{
					Line:    lineNum + 1,
					Column:  loc[0] + 1,
					Text:    line[loc[0]:loc[1]],
					Pattern: pattern.String(),
				})
			}
		}
	}

	return matches
}

// CreateViolation создает нарушение из совпадения с паттерном
func CreateViolation(match PatternMatch, violationType, message, suggestion string, severity Level) Violation {
	return NewViolation(violationType, message, suggestion, severity, match.Line, match.Column)
}
