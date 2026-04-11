package fileops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const maxLineLength = 1024 * 1024 // 1 MB per line max

// SearchFileContentParams holds the input for search_file_content.
type SearchFileContentParams struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	// Flags
	IgnoreCase    bool `json:"ignore_case"`    // -i
	InvertMatch   bool `json:"invert_match"`   // -v
	ShowLineNums  bool `json:"show_line_nums"` // -n
	CountOnly     bool `json:"count_only"`     // -c
	ContextBefore int  `json:"context_before"` // -B
	ContextAfter  int  `json:"context_after"`  // -A
	// MaxMatches limits the number of matches returned. 0 = unlimited.
	MaxMatches int `json:"max_matches"`
	// FixedString treats pattern as literal string, not regex. -F
	FixedString bool `json:"fixed_string"`
}

// MatchLine represents one matched line.
type MatchLine struct {
	LineNum int    `json:"line_num,omitempty"`
	Content string `json:"content"`
	Context string `json:"context,omitempty"` // "before" or "after"
}

// SearchFileContentResult is the JSON result for search_file_content.
type SearchFileContentResult struct {
	Path    string      `json:"path"`
	Pattern string      `json:"pattern"`
	Matches []MatchLine `json:"matches"`
	Count   int         `json:"count"`
}

// SearchFileContent searches a file for lines matching the pattern.
func SearchFileContent(ctx context.Context, guard *PathGuard, argsJSON string) (string, error) {
	var p SearchFileContentParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return "", fmt.Errorf("search_file_content: invalid args: %w", err)
	}
	if err := guard.Check(p.Path); err != nil {
		return "", err
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("search_file_content: pattern is required")
	}

	var re *regexp.Regexp
	if !p.FixedString {
		patStr := p.Pattern
		if p.IgnoreCase {
			patStr = "(?i)" + patStr
		}
		var err error
		re, err = regexp.Compile(patStr)
		if err != nil {
			return "", fmt.Errorf("search_file_content: invalid pattern: %w", err)
		}
	}

	f, err := os.Open(p.Path)
	if err != nil {
		return "", fmt.Errorf("search_file_content: %w", err)
	}
	defer f.Close()

	// Stream-read all lines with ctx cancellation check each iteration.
	// We still collect all lines when context_before/after is requested (need look-back/look-ahead).
	// For large files without context the full-scan is bounded by file size and ctx timeout.
	var allLines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, maxLineLength), maxLineLength)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("search_file_content: scan: %w", err)
	}

	matchLine := func(line string) bool {
		if p.FixedString {
			if p.IgnoreCase {
				return strings.Contains(strings.ToLower(line), strings.ToLower(p.Pattern))
			}
			return strings.Contains(line, p.Pattern)
		}
		matched := re.MatchString(line)
		return matched
	}

	var matches []MatchLine
	count := 0
	addedLines := make(map[int]bool)

	for i, line := range allLines {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		matched := matchLine(line)
		if p.InvertMatch {
			matched = !matched
		}
		if !matched {
			continue
		}
		count++
		if p.CountOnly {
			continue
		}
		if p.MaxMatches > 0 && count > p.MaxMatches {
			break
		}

		// Context before.
		for b := max2(0, i-p.ContextBefore); b < i; b++ {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
			if !addedLines[b] {
				matches = append(matches, makeMatchLine(p, b, allLines[b], "before"))
				addedLines[b] = true
			}
		}
		// Match line itself.
		if !addedLines[i] {
			matches = append(matches, makeMatchLine(p, i, line, ""))
			addedLines[i] = true
		}
		// Context after.
		for a := i + 1; a <= min2(len(allLines)-1, i+p.ContextAfter); a++ {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
			if !addedLines[a] {
				matches = append(matches, makeMatchLine(p, a, allLines[a], "after"))
				addedLines[a] = true
			}
		}
	}

	res := SearchFileContentResult{
		Path:    p.Path,
		Pattern: p.Pattern,
		Matches: matches,
		Count:   count,
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

func makeMatchLine(p SearchFileContentParams, idx int, content, ctx string) MatchLine {
	m := MatchLine{Content: content, Context: ctx}
	if p.ShowLineNums {
		m.LineNum = idx + 1
	}
	return m
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
