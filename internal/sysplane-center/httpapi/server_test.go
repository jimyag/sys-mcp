package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jimyag/sysplane/api/tunnel"
	"github.com/jimyag/sysplane/internal/pkg/tokenauth"
	"github.com/jimyag/sysplane/internal/sysplane-center/admin"
	"github.com/jimyag/sysplane/internal/sysplane-center/httpapi"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
	"github.com/jimyag/sysplane/internal/sysplane-center/router"
)

type fakeStream struct {
	reply func(msg *tunnel.TunnelMessage)
}

func (f *fakeStream) Send(msg *tunnel.TunnelMessage) error {
	if f.reply != nil {
		f.reply(msg)
	}
	return nil
}
func (f *fakeStream) Recv() (*tunnel.TunnelMessage, error) { return nil, nil }
func (f *fakeStream) Context() context.Context             { return context.Background() }
func (f *fakeStream) ID() string                           { return "fake-stream" }
func (f *fakeStream) RemoteAddr() string                   { return "127.0.0.1:0" }

func newHandler(t *testing.T) http.Handler {
	t.Helper()

	reg := registry.New()
	rtr := router.New(1)
	catalog, err := tokenauth.NewCatalog([]string{"client-token"}, []string{"admin-token"}, []string{"agent-token"}, []string{"proxy-token"})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	stream := &fakeStream{}
	stream.reply = func(msg *tunnel.TunnelMessage) {
		req := msg.GetToolRequest()
		if req == nil {
			return
		}
		switch req.ToolName {
		case "stat_file":
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: `{"path":"/etc/hostname","type":"file"}`,
					},
				},
			})
		case "list_directory":
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: `{"path":"/tmp","items":[{"name":"a"},{"name":"b"}],"total":2}`,
					},
				},
			})
		case "get_hardware_info":
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: `{"system":{"hostname":"node-01","os":"linux","kernel_version":"6.6.0","uptime_seconds":7200}}`,
					},
				},
			})
		case "run_process":
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: `{"command":"/bin/echo","args":["hello"],"stdout":"hello\n","stderr":"","exit_code":0,"success":true,"truncated":false,"duration_ms":1}`,
					},
				},
			})
		case "write_file":
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: `{"path":"/tmp/demo.txt","bytes_written":5,"created":true}`,
					},
				},
			})
		case "read_file":
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: `{"path":"/etc/hostname","content":"node-01\n","encoding":"utf-8","truncated":false}`,
					},
				},
			})
		}
	}

	now := time.Now().UTC()
	reg.Register(&registry.AgentRecord{
		Hostname:      "node-01",
		OS:            "linux/amd64",
		AgentVersion:  "0.1.0",
		NodeType:      "agent",
		ProxyPath:     []string{"proxy-sh-01"},
		RegisteredAt:  now,
		LastHeartbeat: now,
		Status:        registry.StatusOnline,
		RouteStream:   stream,
	})
	reg.Register(&registry.AgentRecord{
		Hostname:      "proxy-01",
		NodeType:      "proxy",
		RegisteredAt:  now,
		LastHeartbeat: now,
		Status:        registry.StatusOnline,
	})
	reg.Register(&registry.AgentRecord{
		Hostname:      "node-offline",
		NodeType:      "agent",
		RegisteredAt:  now,
		LastHeartbeat: now,
		Status:        registry.StatusOffline,
	})

	adminSvc := admin.NewService(reg, rtr, nil, nil, "center-a")
	return httpapi.NewHandler(reg, catalog, adminSvc)
}

func TestListNodesFiltersProxyAndReturnsRequestID(t *testing.T) {
	handler := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes?limit=1", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id header")
	}

	var body struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID == "proxy-01" {
		t.Fatalf("expected one agent node without proxy, got %+v", body.Items)
	}
}

func TestNodeActionRequiresAuth(t *testing.T) {
	handler := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/node-01/actions/fs:stat", strings.NewReader(`{"path":"/etc/hostname"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestNodeActionStatReturnsStructuredData(t *testing.T) {
	handler := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/node-01/actions/fs:stat", strings.NewReader(`{"path":"/etc/hostname"}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if body.Data["type"] != "file" {
		t.Fatalf("expected file result, got %+v", body.Data)
	}
}

func TestOfflineNodeActionReturnsNodeOffline(t *testing.T) {
	handler := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/node-offline/actions/fs:stat", strings.NewReader(`{"path":"/etc/hostname"}`))
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if body.Error.Code != "NODE_OFFLINE" {
		t.Fatalf("expected NODE_OFFLINE, got %s", body.Error.Code)
	}
}

func TestCreateAndInvokeCommandTemplate(t *testing.T) {
	handler := newHandler(t)
	createReq := httptest.NewRequest(http.MethodPost, "/v1/command-templates", strings.NewReader(`{
		"name":"echo.hello",
		"description":"echo hello",
		"risk_level":"readonly",
		"target_os":["linux"],
		"executor":{"type":"process","command":"/bin/echo","args":["hello"]},
		"params_schema":{"type":"object","properties":{},"additionalProperties":false},
		"default_timeout_sec":10,
		"max_timeout_sec":30,
		"max_output_bytes":4096
	}`))
	createReq.Header.Set("Authorization", "Bearer admin-token")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var tpl struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &tpl); err != nil {
		t.Fatalf("Unmarshal template: %v", err)
	}

	invokeReq := httptest.NewRequest(http.MethodPost, "/v1/command-templates/"+tpl.ID+":invoke", strings.NewReader(`{
		"targets":{"node_ids":["node-01"]},
		"params":{},
		"async":false
	}`))
	invokeReq.Header.Set("Authorization", "Bearer admin-token")
	invokeRec := httptest.NewRecorder()
	handler.ServeHTTP(invokeRec, invokeReq)
	if invokeRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", invokeRec.Code, invokeRec.Body.String())
	}

	var body struct {
		Invocation struct {
			ID string `json:"id"`
		} `json:"invocation"`
		Results []struct {
			Status string `json:"status"`
			Data   struct {
				Stdout string `json:"stdout"`
			} `json:"data"`
		} `json:"results"`
	}
	if err := json.Unmarshal(invokeRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal invoke response: %v", err)
	}
	if body.Invocation.ID == "" || len(body.Results) != 1 || body.Results[0].Status != "succeeded" || body.Results[0].Data.Stdout != "hello\n" {
		t.Fatalf("unexpected invoke response: %s", invokeRec.Body.String())
	}
}

func TestListInvocationsAndAuditEvents(t *testing.T) {
	handler := newHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/invocations", strings.NewReader(`{
		"action":"fs.read",
		"action_type":"builtin",
		"targets":{"node_ids":["node-01"]},
		"params":{"path":"/etc/hostname"},
		"async":false
	}`))
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/invocations", nil)
	listReq.Header.Set("Authorization", "Bearer client-token")
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}

	var invList struct {
		Items []struct {
			Action string `json:"action"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &invList); err != nil {
		t.Fatalf("Unmarshal invocations: %v", err)
	}
	if len(invList.Items) == 0 || invList.Items[0].Action != "fs.read" {
		t.Fatalf("unexpected invocations list: %s", listRec.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/v1/audit/events", nil)
	auditReq.Header.Set("Authorization", "Bearer admin-token")
	auditRec := httptest.NewRecorder()
	handler.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", auditRec.Code, auditRec.Body.String())
	}

	var auditList struct {
		Items []struct {
			Action   string `json:"action"`
			Decision string `json:"decision"`
		} `json:"items"`
	}
	if err := json.Unmarshal(auditRec.Body.Bytes(), &auditList); err != nil {
		t.Fatalf("Unmarshal audit events: %v", err)
	}
	if len(auditList.Items) == 0 || auditList.Items[0].Action == "" {
		t.Fatalf("unexpected audit list: %s", auditRec.Body.String())
	}
}
