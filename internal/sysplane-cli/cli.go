package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type App struct {
	stdout  io.Writer
	stderr  io.Writer
	client  *http.Client
	getenv  func(string) string
	now     func() time.Time
	baseURL string
	token   string
}

type apiErrorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

type listPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type templateRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type actionEnvelope struct {
	Data map[string]any `json:"data"`
}

func New(stdout, stderr io.Writer) *App {
	return &App{
		stdout: stdout,
		stderr: stderr,
		client: &http.Client{Timeout: 30 * time.Second},
		getenv: os.Getenv,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (a *App) Run(args []string) error {
	root := flag.NewFlagSet("sysplane", flag.ContinueOnError)
	root.SetOutput(a.stderr)
	root.Usage = func() {
		fmt.Fprintln(a.stderr, `Usage:
  sysplane [--server URL] [--token TOKEN] <command> [args]

Commands:
  nodes        list/get/capabilities
  fs           list/read/stat/write
  sys          info/hardware
  templates    list/get/create/update/invoke
  commands     templates 的别名
  invocations  list/get/results/cancel/create
  audit        list/get

Environment:
  SYSPLANE_SERVER / SYSPLANE_URL
  SYSPLANE_TOKEN`)
	}

	server := strings.TrimSpace(firstNonEmpty(a.baseURL, a.getenv("SYSPLANE_SERVER"), a.getenv("SYSPLANE_URL"), "http://127.0.0.1:18880"))
	token := strings.TrimSpace(firstNonEmpty(a.token, a.getenv("SYSPLANE_TOKEN")))
	root.StringVar(&server, "server", server, "sysplane center base URL")
	root.StringVar(&token, "token", token, "bearer token")
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rest := root.Args()
	if len(rest) == 0 {
		root.Usage()
		return errors.New("missing command")
	}
	if token == "" {
		return errors.New("missing token: use --token or SYSPLANE_TOKEN")
	}

	a.baseURL = strings.TrimRight(server, "/")
	a.token = token

	switch rest[0] {
	case "nodes":
		return a.runNodes(rest[1:])
	case "fs":
		return a.runFS(rest[1:])
	case "sys":
		return a.runSys(rest[1:])
	case "templates", "commands":
		return a.runTemplates(rest[1:])
	case "invocations":
		return a.runInvocations(rest[1:])
	case "audit":
		return a.runAudit(rest[1:])
	case "help", "-h", "--help":
		root.Usage()
		return nil
	default:
		root.Usage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func (a *App) runNodes(args []string) error {
	if len(args) == 0 {
		return errors.New("nodes requires a subcommand")
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("sysplane nodes list", a.stderr)
		limit := fs.Int("limit", 50, "page size")
		cursor := fs.String("cursor", "", "pagination cursor")
		hostname := fs.String("hostname", "", "filter by hostname")
		status := fs.String("status", "", "filter by status")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		q := url.Values{}
		if *limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", *limit))
		}
		if *cursor != "" {
			q.Set("cursor", *cursor)
		}
		if *hostname != "" {
			q.Set("hostname", *hostname)
		}
		if *status != "" {
			q.Set("status", *status)
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/nodes", q, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "get":
		if len(args) != 2 {
			return errors.New("usage: sysplane nodes get <node-id>")
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/nodes/"+url.PathEscape(args[1]), nil, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "capabilities":
		if len(args) != 2 {
			return errors.New("usage: sysplane nodes capabilities <node-id>")
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/nodes/"+url.PathEscape(args[1])+"/capabilities", nil, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	default:
		return fmt.Errorf("unknown nodes subcommand %q", args[0])
	}
}

func (a *App) runFS(args []string) error {
	if len(args) == 0 {
		return errors.New("fs requires a subcommand")
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("sysplane fs list", a.stderr)
		nodeID := fs.String("node", "", "target node ID")
		path := fs.String("path", "", "target directory path")
		showHidden := fs.Bool("show-hidden", false, "include hidden files")
		limit := fs.Int("limit", 0, "maximum entries")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		payload := map[string]any{"path": *path, "show_hidden": *showHidden}
		if *limit > 0 {
			payload["limit"] = *limit
		}
		return a.runNodeAction(*nodeID, "fs:list", payload, false)
	case "read":
		fs := newFlagSet("sysplane fs read", a.stderr)
		nodeID := fs.String("node", "", "target node ID")
		path := fs.String("path", "", "target file path")
		offset := fs.Int64("offset", 0, "read offset")
		length := fs.Int64("length", 0, "read length")
		jsonOut := fs.Bool("json", false, "print JSON envelope instead of content only")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		payload := map[string]any{"path": *path}
		if *offset > 0 {
			payload["offset"] = *offset
		}
		if *length > 0 {
			payload["length"] = *length
		}
		return a.runNodeAction(*nodeID, "fs:read", payload, !*jsonOut)
	case "stat":
		fs := newFlagSet("sysplane fs stat", a.stderr)
		nodeID := fs.String("node", "", "target node ID")
		path := fs.String("path", "", "target path")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		return a.runNodeAction(*nodeID, "fs:stat", map[string]any{"path": *path}, false)
	case "write":
		fs := newFlagSet("sysplane fs write", a.stderr)
		nodeID := fs.String("node", "", "target node ID")
		path := fs.String("path", "", "target file path")
		content := fs.String("content", "", "inline file content")
		contentFile := fs.String("content-file", "", "read file content from local path")
		overwrite := fs.Bool("overwrite", false, "overwrite existing file")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		body, err := resolveContent(*content, *contentFile)
		if err != nil {
			return err
		}
		return a.runNodeAction(*nodeID, "fs:write", map[string]any{
			"path":      *path,
			"content":   body,
			"overwrite": *overwrite,
		}, false)
	default:
		return fmt.Errorf("unknown fs subcommand %q", args[0])
	}
}

func (a *App) runSys(args []string) error {
	if len(args) == 0 {
		return errors.New("sys requires a subcommand")
	}
	switch args[0] {
	case "info", "hardware":
		fs := newFlagSet("sysplane sys "+args[0], a.stderr)
		nodeID := fs.String("node", "", "target node ID")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		return a.runNodeAction(*nodeID, "sys:"+args[0], map[string]any{}, false)
	default:
		return fmt.Errorf("unknown sys subcommand %q", args[0])
	}
}

func (a *App) runTemplates(args []string) error {
	if len(args) == 0 {
		return errors.New("templates requires a subcommand")
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("sysplane templates list", a.stderr)
		limit := fs.Int("limit", 50, "page size")
		name := fs.String("name", "", "filter by name")
		riskLevel := fs.String("risk-level", "", "filter by risk level")
		targetOS := fs.String("target-os", "", "filter by target OS")
		enabled := fs.String("enabled", "", "filter by enabled=true|false")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		q := url.Values{}
		if *limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", *limit))
		}
		if *name != "" {
			q.Set("name", *name)
		}
		if *riskLevel != "" {
			q.Set("risk_level", *riskLevel)
		}
		if *targetOS != "" {
			q.Set("target_os", *targetOS)
		}
		if *enabled != "" {
			q.Set("enabled", *enabled)
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/command-templates", q, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "get":
		if len(args) != 2 {
			return errors.New("usage: sysplane templates get <template-id-or-name>")
		}
		id, err := a.resolveTemplateRef(args[1])
		if err != nil {
			return err
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/command-templates/"+url.PathEscape(id), nil, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "create":
		fs := newFlagSet("sysplane templates create", a.stderr)
		file := fs.String("file", "", "template request JSON file")
		data := fs.String("data", "", "template request JSON string")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		body, err := resolveJSONBody(*data, *file)
		if err != nil {
			return err
		}
		var out any
		if err := a.request(context.Background(), http.MethodPost, "/v1/command-templates", nil, body, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "update":
		if len(args) < 2 {
			return errors.New("usage: sysplane templates update <template-id-or-name> --file patch.json")
		}
		id, err := a.resolveTemplateRef(args[1])
		if err != nil {
			return err
		}
		fs := newFlagSet("sysplane templates update", a.stderr)
		file := fs.String("file", "", "template patch JSON file")
		data := fs.String("data", "", "template patch JSON string")
		if stop, err := parseFlagSet(fs, args[2:]); err != nil || stop {
			return err
		}
		body, err := resolveJSONBody(*data, *file)
		if err != nil {
			return err
		}
		var out any
		if err := a.request(context.Background(), http.MethodPatch, "/v1/command-templates/"+url.PathEscape(id), nil, body, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "invoke":
		if len(args) < 2 {
			return errors.New("usage: sysplane templates invoke <template-id-or-name> --nodes n1,n2 [--params '{}']")
		}
		id, err := a.resolveTemplateRef(args[1])
		if err != nil {
			return err
		}
		fs := newFlagSet("sysplane templates invoke", a.stderr)
		node := fs.String("node", "", "single node ID")
		nodes := fs.String("nodes", "", "comma-separated node IDs")
		params := fs.String("params", "{}", "params JSON object")
		paramsFile := fs.String("params-file", "", "params JSON file")
		timeoutSec := fs.Int("timeout", 0, "override timeout in seconds")
		async := fs.Bool("async", false, "run asynchronously")
		if stop, err := parseFlagSet(fs, args[2:]); err != nil || stop {
			return err
		}
		body, err := resolveJSONObject(*params, *paramsFile)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"targets": map[string]any{"node_ids": targetNodeList(*node, *nodes)},
			"params":  body,
			"async":   *async,
		}
		if *timeoutSec > 0 {
			payload["timeout_sec"] = *timeoutSec
		}
		var out any
		if err := a.request(context.Background(), http.MethodPost, "/v1/command-templates/"+url.PathEscape(id)+":invoke", nil, payload, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	default:
		return fmt.Errorf("unknown templates subcommand %q", args[0])
	}
}

func (a *App) runInvocations(args []string) error {
	if len(args) == 0 {
		return errors.New("invocations requires a subcommand")
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("sysplane invocations list", a.stderr)
		limit := fs.Int("limit", 50, "page size")
		status := fs.String("status", "", "filter by status")
		action := fs.String("action", "", "filter by action")
		actionType := fs.String("action-type", "", "filter by action type")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		q := url.Values{}
		if *limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", *limit))
		}
		if *status != "" {
			q.Set("status", *status)
		}
		if *action != "" {
			q.Set("action", *action)
		}
		if *actionType != "" {
			q.Set("action_type", *actionType)
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/invocations", q, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "get":
		if len(args) != 2 {
			return errors.New("usage: sysplane invocations get <invocation-id>")
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/invocations/"+url.PathEscape(args[1]), nil, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "results":
		if len(args) != 2 {
			return errors.New("usage: sysplane invocations results <invocation-id>")
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/invocations/"+url.PathEscape(args[1])+"/results", nil, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "cancel":
		if len(args) != 2 {
			return errors.New("usage: sysplane invocations cancel <invocation-id>")
		}
		var out any
		if err := a.request(context.Background(), http.MethodPost, "/v1/invocations/"+url.PathEscape(args[1])+":cancel", nil, map[string]any{}, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "create":
		fs := newFlagSet("sysplane invocations create", a.stderr)
		action := fs.String("action", "", "builtin action or template name/id")
		actionType := fs.String("action-type", "builtin", "builtin or command_template")
		node := fs.String("node", "", "single node ID")
		nodes := fs.String("nodes", "", "comma-separated node IDs")
		params := fs.String("params", "{}", "params JSON object")
		paramsFile := fs.String("params-file", "", "params JSON file")
		timeoutSec := fs.Int("timeout", 0, "override timeout in seconds")
		async := fs.Bool("async", false, "run asynchronously")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		if *action == "" {
			return errors.New("action is required")
		}
		body, err := resolveJSONObject(*params, *paramsFile)
		if err != nil {
			return err
		}
		resolvedAction := *action
		if *actionType == "command_template" {
			resolvedAction, err = a.resolveTemplateRef(*action)
			if err != nil {
				return err
			}
		}
		payload := map[string]any{
			"action":      resolvedAction,
			"action_type": *actionType,
			"targets":     map[string]any{"node_ids": targetNodeList(*node, *nodes)},
			"params":      body,
			"async":       *async,
		}
		if *timeoutSec > 0 {
			payload["timeout_sec"] = *timeoutSec
		}
		var out any
		if err := a.request(context.Background(), http.MethodPost, "/v1/invocations", nil, payload, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	default:
		return fmt.Errorf("unknown invocations subcommand %q", args[0])
	}
}

func (a *App) runAudit(args []string) error {
	if len(args) == 0 {
		return errors.New("audit requires a subcommand")
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("sysplane audit list", a.stderr)
		limit := fs.Int("limit", 50, "page size")
		action := fs.String("action", "", "filter by action")
		decision := fs.String("decision", "", "filter by decision")
		nodeID := fs.String("node", "", "filter by node ID")
		subjectID := fs.String("subject", "", "filter by subject ID")
		if stop, err := parseFlagSet(fs, args[1:]); err != nil || stop {
			return err
		}
		q := url.Values{}
		if *limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", *limit))
		}
		if *action != "" {
			q.Set("action", *action)
		}
		if *decision != "" {
			q.Set("decision", *decision)
		}
		if *nodeID != "" {
			q.Set("node_id", *nodeID)
		}
		if *subjectID != "" {
			q.Set("subject_id", *subjectID)
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/audit/events", q, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	case "get":
		if len(args) != 2 {
			return errors.New("usage: sysplane audit get <event-id>")
		}
		var out any
		if err := a.request(context.Background(), http.MethodGet, "/v1/audit/events/"+url.PathEscape(args[1]), nil, nil, &out); err != nil {
			return err
		}
		return a.printJSON(out)
	default:
		return fmt.Errorf("unknown audit subcommand %q", args[0])
	}
}

func (a *App) runNodeAction(nodeID, action string, payload map[string]any, contentOnly bool) error {
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("node is required")
	}
	var out actionEnvelope
	if err := a.request(context.Background(), http.MethodPost, "/v1/nodes/"+url.PathEscape(nodeID)+"/actions/"+url.PathEscape(action), nil, payload, &out); err != nil {
		return err
	}
	if contentOnly {
		if content, ok := out.Data["content"].(string); ok {
			_, err := fmt.Fprintln(a.stdout, content)
			return err
		}
	}
	return a.printJSON(out.Data)
}

func (a *App) request(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	fullURL := a.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Request-Id", "cli-"+a.now().Format("20060102150405"))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiErrorEnvelope
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error.Code != "" {
			return fmt.Errorf("%s: %s (request_id=%s)", apiErr.Error.Code, apiErr.Error.Message, apiErr.Error.RequestID)
		}
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *App) resolveTemplateRef(ref string) (string, error) {
	var page listPage[templateRef]
	if err := a.request(context.Background(), http.MethodGet, "/v1/command-templates", url.Values{
		"name":  []string{ref},
		"limit": []string{"200"},
	}, nil, &page); err == nil {
		for _, item := range page.Items {
			if item.Name == ref {
				return item.ID, nil
			}
			if item.ID == ref {
				return item.ID, nil
			}
		}
	}
	return ref, nil
}

func (a *App) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parseFlagSet(fs *flag.FlagSet, args []string) (bool, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func targetNodeList(single, multi string) []string {
	out := splitCSV(multi)
	if strings.TrimSpace(single) != "" {
		out = append([]string{strings.TrimSpace(single)}, out...)
	}
	return out
}

func resolveContent(content, file string) (string, error) {
	if content != "" && file != "" {
		return "", errors.New("use either --content or --content-file")
	}
	if file == "" {
		return content, nil
	}
	raw, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func resolveJSONBody(data, file string) (any, error) {
	raw, err := readTextSource(data, file)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("JSON body is required")
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func resolveJSONObject(data, file string) (map[string]any, error) {
	raw, err := readTextSource(data, file)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func readTextSource(data, file string) (string, error) {
	if data != "" && file != "" {
		return "", errors.New("use either inline JSON or --file/--params-file")
	}
	if file == "" {
		return data, nil
	}
	raw, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
