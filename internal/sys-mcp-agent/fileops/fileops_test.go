package fileops_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimyag/sys-mcp/internal/sys-mcp-agent/fileops"
)

var allowAllGuard = fileops.NewPathGuard(nil, nil)

func TestListDirectory_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644)

	argsJSON := fmt.Sprintf(`{"path":%q,"show_hidden":false}`, dir)
	out, err := fileops.ListDirectory(context.Background(), allowAllGuard, argsJSON)
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.ListDirectoryResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("expected 2 non-hidden items, got %d", res.Total)
	}
}

func TestListDirectory_ShowHidden(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644)

	argsJSON := fmt.Sprintf(`{"path":%q,"show_hidden":true}`, dir)
	out, err := fileops.ListDirectory(context.Background(), allowAllGuard, argsJSON)
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.ListDirectoryResult
	json.Unmarshal([]byte(out), &res)
	if res.Total != 1 {
		t.Fatalf("expected 1 item with hidden, got %d", res.Total)
	}
}

func TestStatFile_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	out, err := fileops.StatFile(context.Background(), allowAllGuard, fmt.Sprintf(`{"path":%q}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.StatResult
	json.Unmarshal([]byte(out), &res)
	if res.Type != "file" {
		t.Fatalf("expected type=file, got %s", res.Type)
	}
	if res.Size != 5 {
		t.Fatalf("expected size=5, got %d", res.Size)
	}
}

func TestCheckPathExists_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, nil, 0o644)

	out, err := fileops.CheckPathExists(context.Background(), allowAllGuard, fmt.Sprintf(`{"path":%q}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.CheckPathExistsResult
	json.Unmarshal([]byte(out), &res)
	if !res.Exists {
		t.Fatal("expected exists=true")
	}
}

func TestCheckPathExists_NotExists(t *testing.T) {
	out, err := fileops.CheckPathExists(context.Background(), allowAllGuard, `{"path":"/nonexistent/xyz123"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.CheckPathExistsResult
	json.Unmarshal([]byte(out), &res)
	if res.Exists {
		t.Fatal("expected exists=false")
	}
}

func TestReadFile_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3"), 0o644)

	out, err := fileops.ReadFile(context.Background(), allowAllGuard, 100, fmt.Sprintf(`{"path":%q}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.ReadFileResult
	json.Unmarshal([]byte(out), &res)
	if res.Lines != 3 {
		t.Fatalf("expected 3 lines, got %d", res.Lines)
	}
}

func TestReadFile_Binary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.dat")
	os.WriteFile(path, []byte{0x00, 0x01, 0x02}, 0o644)

	out, err := fileops.ReadFile(context.Background(), allowAllGuard, 100, fmt.Sprintf(`{"path":%q}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.ReadFileResult
	json.Unmarshal([]byte(out), &res)
	if !res.IsBinary {
		t.Fatal("expected is_binary=true")
	}
}

func TestReadFile_Tail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	lines := "a\nb\nc\nd\ne"
	os.WriteFile(path, []byte(lines), 0o644)

	out, err := fileops.ReadFile(context.Background(), allowAllGuard, 100, fmt.Sprintf(`{"path":%q,"tail":2}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.ReadFileResult
	json.Unmarshal([]byte(out), &res)
	if res.Lines != 2 {
		t.Fatalf("expected 2 tail lines, got %d", res.Lines)
	}
	if !strings.Contains(res.Content, "d") || !strings.Contains(res.Content, "e") {
		t.Fatalf("expected last 2 lines d,e in content, got: %s", res.Content)
	}
}

func TestReadFile_SizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	// Write 2MB of data
	data := make([]byte, 2*1024*1024)
	for i := range data {
		data[i] = 'a'
	}
	os.WriteFile(path, data, 0o644)

	_, err := fileops.ReadFile(context.Background(), allowAllGuard, 1, fmt.Sprintf(`{"path":%q}`, path))
	if err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestSearchFileContent_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	os.WriteFile(path, []byte("error: something\ninfo: ok\nerror: again"), 0o644)

	out, err := fileops.SearchFileContent(context.Background(), allowAllGuard,
		fmt.Sprintf(`{"path":%q,"pattern":"error"}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.SearchFileContentResult
	json.Unmarshal([]byte(out), &res)
	if res.Count != 2 {
		t.Fatalf("expected 2 matches, got %d", res.Count)
	}
}

func TestSearchFileContent_IgnoreCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	os.WriteFile(path, []byte("Error: something\nerror: again"), 0o644)

	out, err := fileops.SearchFileContent(context.Background(), allowAllGuard,
		fmt.Sprintf(`{"path":%q,"pattern":"error","ignore_case":true}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.SearchFileContentResult
	json.Unmarshal([]byte(out), &res)
	if res.Count != 2 {
		t.Fatalf("expected 2 case-insensitive matches, got %d", res.Count)
	}
}

func TestSearchFileContent_InvertMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	os.WriteFile(path, []byte("error: x\ninfo: ok\nwarn: y"), 0o644)

	out, err := fileops.SearchFileContent(context.Background(), allowAllGuard,
		fmt.Sprintf(`{"path":%q,"pattern":"error","invert_match":true}`, path))
	if err != nil {
		t.Fatal(err)
	}
	var res fileops.SearchFileContentResult
	json.Unmarshal([]byte(out), &res)
	if res.Count != 2 {
		t.Fatalf("expected 2 non-error matches, got %d", res.Count)
	}
}
