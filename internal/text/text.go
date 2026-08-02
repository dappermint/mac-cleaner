package text

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func Count(value int) string {
	switch {
	case value >= 1000000:
		return fmt.Sprintf("%.1fM", float64(value)/1000000)
	case value >= 1000:
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	default:
		return strconv.Itoa(value)
	}
}

func Clean(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == 27 || r < 32 || r == 127 {
			return ' '
		}
		return r
	}, value)
}

func Truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= width {
		return value
	}
	runes := []rune(value)
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func PadRight(value string, width int) string {
	count := utf8.RuneCountInString(value)
	if count >= width {
		return Truncate(value, width)
	}
	return value + strings.Repeat(" ", width-count)
}

func PadLeft(value string, width int) string {
	count := utf8.RuneCountInString(value)
	if count >= width {
		return Truncate(value, width)
	}
	return strings.Repeat(" ", width-count) + value
}

func JoinEdges(left, right string, width int) string {
	left = Clean(left)
	right = Clean(right)
	if right == "" {
		return Truncate(left, width)
	}
	if utf8.RuneCountInString(left)+utf8.RuneCountInString(right)+2 > width {
		leftWidth := width - utf8.RuneCountInString(right) - 2
		if leftWidth < 1 {
			return Truncate(right, width)
		}
		left = Truncate(left, leftWidth)
	}
	spaces := width - utf8.RuneCountInString(left) - utf8.RuneCountInString(right)
	if spaces < 1 {
		spaces = 1
	}
	return left + strings.Repeat(" ", spaces) + right
}

func Duration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	minutes := int(duration / time.Minute)
	seconds := int(duration/time.Second) % 60
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func Wrap(value string, width int) []string {
	if width < 10 {
		width = 10
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil
	}
	lines := []string{words[0]}
	for _, word := range words[1:] {
		last := len(lines) - 1
		if utf8.RuneCountInString(lines[last])+1+utf8.RuneCountInString(word) <= width {
			lines[last] += " " + word
		} else {
			lines = append(lines, Truncate(word, width))
		}
	}
	return lines
}
