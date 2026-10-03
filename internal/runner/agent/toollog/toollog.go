// Package toollog renders decoded tool-call inputs into compact, log-friendly
// summaries. It is a pure, dependency-free utility imported by the agent
// stream parsers (claude.go, codex.go, cursor.go); it knows nothing about
// specific tools or field names.
package toollog

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	// maxValueLen caps the length (in runes) of an individual rendered value.
	maxValueLen = 120
	// maxSummaryLen caps the length (in runes) of the whole summary line.
	maxSummaryLen = 200
	// truncatedPlaceholder replaces bulky field values that a provider redacts
	// at its call site before handing the input to Summarize.
	truncatedPlaceholder = "<truncated>"
)

// Summarize renders a tool call's decoded input object as a short,
// single-line, human-readable summary. It knows nothing about specific tools
// or field names: it walks the map in sorted key order, renders key=value per
// field with type-aware formatting, collapses whitespace/newlines to a single
// space, and applies per-value and overall length limits. It returns "" when
// there is nothing useful to render, so the log line falls back to just the
// tool name.
func Summarize(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		v := input[k]
		if v == nil {
			continue
		}
		pairs = append(pairs, k+"="+formatValue(v))
	}
	if len(pairs) == 0 {
		return ""
	}
	return truncateRunes(strings.Join(pairs, " "), maxSummaryLen)
}

// Redact replaces the given keys, when present, with the bulky-field
// placeholder. Callers pass the field names they know are bulky, which keeps
// all tool-specific field knowledge at the provider call site rather than in
// the name-agnostic summarizer.
func Redact(input map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if _, ok := input[k]; ok {
			input[k] = truncatedPlaceholder
		}
	}
	return input
}

// formatValue renders a single decoded JSON value by type.
func formatValue(v any) string {
	switch t := v.(type) {
	case string:
		return formatScalarString(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return formatNumber(t)
	case []any:
		return formatArray(t)
	case map[string]any:
		// Objects are never expanded, to keep the summary one line.
		return "{…}"
	default:
		return formatScalarString(fmt.Sprintf("%v", t))
	}
}

// formatScalarString collapses whitespace, truncates to the per-value limit,
// and quotes the result if it contains a space.
func formatScalarString(s string) string {
	s = collapseWhitespace(s)
	s = truncateRunes(s, maxValueLen)
	if strings.Contains(s, " ") {
		return strconv.Quote(s)
	}
	return s
}

// formatNumber renders integral floats without a decimal point and other
// numbers with the shortest representation that round-trips.
func formatNumber(f float64) string {
	if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// formatArray joins scalar arrays with spaces (so a shell command array reads
// as a command line) and renders any array containing a non-scalar element as
// "[n items]".
func formatArray(arr []any) string {
	parts := make([]string, 0, len(arr))
	for _, el := range arr {
		switch e := el.(type) {
		case string:
			parts = append(parts, e)
		case bool:
			parts = append(parts, strconv.FormatBool(e))
		case float64:
			parts = append(parts, formatNumber(e))
		default:
			return fmt.Sprintf("[%d items]", len(arr))
		}
	}
	return formatScalarString(strings.Join(parts, " "))
}

// collapseWhitespace replaces every run of whitespace (including newlines) with
// a single space and trims the ends.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes truncates s to at most max runes, appending an ellipsis when
// truncation occurs. Truncation happens on rune boundaries, not bytes.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
