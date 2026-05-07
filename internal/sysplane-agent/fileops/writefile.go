package fileops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type WriteFileParams struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

type WriteFileResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
	Created      bool   `json:"created"`
}

func WriteFile(ctx context.Context, guard *PathGuard, argsJSON string) (string, error) {
	var p WriteFileParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return "", fmt.Errorf("write_file: invalid args: %w", err)
	}
	if err := guard.Check(p.Path); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	flags := os.O_WRONLY | os.O_CREATE
	if p.Overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}

	_, statErr := os.Stat(p.Path)
	created := os.IsNotExist(statErr)
	f, err := os.OpenFile(p.Path, flags, 0o644)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	defer f.Close()

	n, err := f.WriteString(p.Content)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}

	out, _ := json.Marshal(WriteFileResult{
		Path:         filepath.Clean(p.Path),
		BytesWritten: n,
		Created:      created,
	})
	return string(out), nil
}
