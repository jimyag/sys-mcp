package fileops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ReadFileParams holds the input parameters for read_file.
type ReadFileParams struct {
	Path     string `json:"path"`
	Encoding string `json:"encoding"` // "text" (default) or "base64"
	// Head reads the first N lines. 0 = disabled.
	Head int `json:"head"`
	// Tail reads the last N lines. 0 = disabled.
	Tail int `json:"tail"`
	// MaxLines limits total lines returned. 0 = no limit.
	MaxLines int `json:"max_lines"`
}

// ReadFileResult is the result of read_file.
type ReadFileResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
	IsBinary  bool   `json:"is_binary"`
	SizeBytes int64  `json:"size_bytes"`
}

const binaryCheckBytes = 512

// ReadFile reads a file and returns its content as text or base64.
func ReadFile(ctx context.Context, guard *PathGuard, maxFileSizeMB int64, argsJSON string) (string, error) {
	var p ReadFileParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return "", fmt.Errorf("read_file: invalid args: %w", err)
	}
	if err := guard.Check(p.Path); err != nil {
		return "", err
	}

	fi, err := os.Stat(p.Path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("read_file: %q is a directory", p.Path)
	}
	sizeBytes := fi.Size()

	f, err := os.Open(p.Path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer f.Close()

	// Binary detection: read first binaryCheckBytes.
	probe := make([]byte, binaryCheckBytes)
	n, _ := f.Read(probe)
	if bytes.IndexByte(probe[:n], 0) >= 0 {
		res := ReadFileResult{
			Path:      p.Path,
			IsBinary:  true,
			SizeBytes: sizeBytes,
			Encoding:  "text",
		}
		out, _ := json.Marshal(res)
		return string(out), nil
	}

	// Size limit check.
	maxBytes := maxFileSizeMB * 1024 * 1024
	if sizeBytes > maxBytes {
		return "", fmt.Errorf("read_file: file size %d bytes exceeds limit %d bytes", sizeBytes, maxBytes)
	}

	// Re-seek to start.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read_file: seek: %w", err)
	}

	if p.Tail > 0 {
		return readTail(ctx, p.Path, f, p.Tail, sizeBytes)
	}

	// Read all lines.
	var lines []string
	scanner := bufio.NewScanner(f)
	truncated := false
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		lines = append(lines, scanner.Text())
		if p.Head > 0 && len(lines) >= p.Head {
			truncated = true
			break
		}
		if p.MaxLines > 0 && len(lines) >= p.MaxLines {
			truncated = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read_file: scan: %w", err)
	}

	content := joinLines(lines)
	res := ReadFileResult{
		Path:      p.Path,
		Content:   content,
		Encoding:  "text",
		Lines:     len(lines),
		Truncated: truncated,
		SizeBytes: sizeBytes,
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

// readTail returns the last n lines using a ring buffer.
func readTail(ctx context.Context, path string, f *os.File, n int, sizeBytes int64) (string, error) {
	ring := make([]string, n)
	idx := 0
	total := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		ring[idx%n] = scanner.Text()
		idx++
		total++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read_file tail: %w", err)
	}

	var lines []string
	if total <= n {
		lines = ring[:total]
	} else {
		start := idx % n
		lines = append(ring[start:], ring[:start]...)
	}

	res := ReadFileResult{
		Path:      path,
		Content:   joinLines(lines),
		Encoding:  "text",
		Lines:     len(lines),
		Truncated: total > n,
		SizeBytes: sizeBytes,
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	buf := make([]byte, 0, total)
	for i, l := range lines {
		buf = append(buf, l...)
		if i < len(lines)-1 {
			buf = append(buf, '\n')
		}
	}
	return string(buf)
}
