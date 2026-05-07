package fileops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StatFileParams holds the input parameters for stat_file.
type StatFileParams struct {
	Path string `json:"path"`
}

// StatResult holds the metadata for a file or directory.
type StatResult struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Size        int64     `json:"size"`
	Permissions string    `json:"permissions"`
	ModifiedAt  time.Time `json:"modified_at"`
	IsSymlink   bool      `json:"is_symlink"`
	LinkTarget  string    `json:"link_target,omitempty"`
}

// StatFile returns detailed metadata for a file or directory.
func StatFile(ctx context.Context, guard *PathGuard, argsJSON string) (string, error) {
	var p StatFileParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return "", fmt.Errorf("stat_file: invalid args: %w", err)
	}
	if err := guard.Check(p.Path); err != nil {
		return "", err
	}

	info, err := os.Lstat(p.Path)
	if err != nil {
		return "", fmt.Errorf("stat_file: %w", err)
	}

	res := StatResult{
		Path:        filepath.Clean(p.Path),
		Name:        info.Name(),
		Size:        info.Size(),
		Permissions: info.Mode().String(),
		ModifiedAt:  info.ModTime(),
		IsSymlink:   info.Mode()&os.ModeSymlink != 0,
	}
	switch {
	case info.IsDir():
		res.Type = "directory"
	case info.Mode()&os.ModeSymlink != 0:
		res.Type = "symlink"
		if target, err := os.Readlink(p.Path); err == nil {
			res.LinkTarget = target
		}
	case info.Mode().IsRegular():
		res.Type = "file"
	default:
		res.Type = "other"
	}

	out, _ := json.Marshal(res)
	return string(out), nil
}

// CheckPathExistsParams holds the input for check_path_exists.
type CheckPathExistsParams struct {
	Path string `json:"path"`
}

// CheckPathExistsResult is the result of check_path_exists.
type CheckPathExistsResult struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Type   string `json:"type,omitempty"`
}

// CheckPathExists checks whether a path exists without reading its contents.
func CheckPathExists(ctx context.Context, guard *PathGuard, argsJSON string) (string, error) {
	var p CheckPathExistsParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return "", fmt.Errorf("check_path_exists: invalid args: %w", err)
	}
	if err := guard.Check(p.Path); err != nil {
		return "", err
	}

	res := CheckPathExistsResult{Path: filepath.Clean(p.Path)}
	info, err := os.Lstat(p.Path)
	if err != nil {
		if os.IsNotExist(err) {
			res.Exists = false
		} else {
			return "", fmt.Errorf("check_path_exists: %w", err)
		}
	} else {
		res.Exists = true
		switch {
		case info.IsDir():
			res.Type = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			res.Type = "symlink"
		case info.Mode().IsRegular():
			res.Type = "file"
		default:
			res.Type = "other"
		}
	}

	out, _ := json.Marshal(res)
	return string(out), nil
}
