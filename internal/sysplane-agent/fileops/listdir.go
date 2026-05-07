package fileops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ListDirectoryParams holds the input parameters for list_directory.
type ListDirectoryParams struct {
	Path       string `json:"path"`
	ShowHidden bool   `json:"show_hidden"`
}

// FileEntry represents a single item returned by list_directory.
type FileEntry struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"` // "file", "directory", "symlink", "other"
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modified_at"`
	Permissions string    `json:"permissions"` // e.g. "-rw-r--r--"
}

// ListDirectoryResult is the JSON result of list_directory.
type ListDirectoryResult struct {
	Path  string      `json:"path"`
	Items []FileEntry `json:"items"`
	Total int         `json:"total"`
}

// ListDirectory lists the contents of a directory.
func ListDirectory(ctx context.Context, guard *PathGuard, argsJSON string) (string, error) {
	var p ListDirectoryParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return "", fmt.Errorf("list_directory: invalid args: %w", err)
	}
	if err := guard.Check(p.Path); err != nil {
		return "", err
	}

	entries, err := os.ReadDir(p.Path)
	if err != nil {
		return "", fmt.Errorf("list_directory: %w", err)
	}

	items := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		if !p.ShowHidden && len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, FileEntry{
			Name:        e.Name(),
			Type:        entryType(e),
			Size:        info.Size(),
			ModifiedAt:  info.ModTime(),
			Permissions: info.Mode().String(),
		})
	}

	result := ListDirectoryResult{
		Path:  filepath.Clean(p.Path),
		Items: items,
		Total: len(items),
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

func entryType(e os.DirEntry) string {
	switch {
	case e.IsDir():
		return "directory"
	case e.Type()&os.ModeSymlink != 0:
		return "symlink"
	case e.Type().IsRegular():
		return "file"
	default:
		return "other"
	}
}
