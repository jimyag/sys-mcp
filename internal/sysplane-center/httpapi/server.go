package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jimyag/sysplane/internal/pkg/stream"
	"github.com/jimyag/sysplane/internal/pkg/tokenauth"
	"github.com/jimyag/sysplane/internal/sysplane-center/admin"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
)

type Server struct {
	reg    *registry.Registry
	tokens *tokenauth.Catalog
	admin  *admin.Service
}

type contextKey string

const requestIDKey contextKey = "requestID"

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type nodeResponse struct {
	ID           string            `json:"id"`
	Hostname     string            `json:"hostname"`
	Labels       map[string]string `json:"labels"`
	Status       string            `json:"status"`
	Platform     nodePlatform      `json:"platform"`
	Agent        nodeAgent         `json:"agent"`
	LastSeenAt   time.Time         `json:"last_seen_at"`
	RegisteredAt time.Time         `json:"registered_at"`
}

type nodePlatform struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kernel string `json:"kernel"`
}

type nodeAgent struct {
	Version      string   `json:"version"`
	ConnectedVia []string `json:"connected_via"`
}

type capabilityResponse struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	RiskLevel string `json:"risk_level"`
	Enabled   bool   `json:"enabled"`
}

type successEnvelope struct {
	Data any `json:"data"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}

func NewHandler(reg *registry.Registry, tokens *tokenauth.Catalog, adminSvc *admin.Service) http.Handler {
	s := &Server{reg: reg, tokens: tokens, admin: adminSvc}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/nodes", s.requireHTTP(http.HandlerFunc(s.handleListNodes), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("/v1/nodes/", s.requireHTTP(http.HandlerFunc(s.handleNodeRoutes), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("GET /v1/command-templates", s.requireHTTP(http.HandlerFunc(s.handleListTemplates), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("POST /v1/command-templates", s.requireHTTP(http.HandlerFunc(s.handleCreateTemplate), tokenauth.DomainAdmin))
	mux.Handle("/v1/command-templates/", s.requireHTTP(http.HandlerFunc(s.handleTemplateRoutes), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("GET /v1/invocations", s.requireHTTP(http.HandlerFunc(s.handleListInvocations), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("POST /v1/invocations", s.requireHTTP(http.HandlerFunc(s.handleCreateInvocation), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("/v1/invocations/", s.requireHTTP(http.HandlerFunc(s.handleInvocationRoutes), tokenauth.DomainClient, tokenauth.DomainAdmin))
	mux.Handle("GET /v1/audit/events", s.requireHTTP(http.HandlerFunc(s.handleListAuditEvents), tokenauth.DomainAdmin))
	mux.Handle("/v1/audit/events/", s.requireHTTP(http.HandlerFunc(s.handleAuditEventRoutes), tokenauth.DomainAdmin))
	return s.withRequestID(mux)
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = stream.NewRequestID("http")
		}
		w.Header().Set("X-Request-Id", requestID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireHTTP(next http.Handler, allowed ...tokenauth.Domain) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := s.tokens.AuthenticateHTTP(r.Header.Get("Authorization"), allowed...)
		if err != nil {
			switch err.Error() {
			case "forbidden":
				s.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "token is not allowed to access this endpoint", nil)
			default:
				s.writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid bearer token", nil)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(tokenauth.WithIdentity(r.Context(), identity)))
	})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	selector := r.URL.Query().Get("label_selector")
	limit, ok := parsePositiveIntQuery(r, "limit", 50, 200)
	if !ok {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "limit must be a positive integer", map[string]any{"field": "limit"})
		return
	}
	cursor := r.URL.Query().Get("cursor")
	hostnameFilter := r.URL.Query().Get("hostname")
	statusFilter := r.URL.Query().Get("status")

	records := s.reg.All()
	nodes := make([]nodeResponse, 0, len(records))
	for _, rec := range records {
		if rec.NodeType != "agent" {
			continue
		}
		if hostnameFilter != "" && rec.Hostname != hostnameFilter {
			continue
		}
		if statusFilter != "" && string(rec.Status) != statusFilter {
			continue
		}
		if selector != "" {
			continue
		}
		nodes = append(nodes, buildNode(rec))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	page := paginate(nodes, cursor, limit, func(item nodeResponse) string { return item.ID })
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleNodeRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/nodes/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		s.writeError(w, r, http.StatusNotFound, "NODE_NOT_FOUND", "node not found", nil)
		return
	}
	parts := strings.Split(path, "/")
	nodeID, err := url.PathUnescape(parts[0])
	if err != nil || nodeID == "" {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid node id", map[string]any{"field": "node_id"})
		return
	}

	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.handleGetNode(w, r, nodeID)
	case len(parts) == 2 && parts[1] == "capabilities" && r.Method == http.MethodGet:
		s.handleCapabilities(w, r, nodeID)
	case len(parts) == 3 && parts[1] == "actions" && r.Method == http.MethodPost:
		s.handleAction(w, r, nodeID, parts[2])
	default:
		s.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "endpoint not found", nil)
	}
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request, nodeID string) {
	rec := s.reg.Lookup(nodeID)
	if rec == nil || rec.NodeType != "agent" {
		s.writeError(w, r, http.StatusNotFound, "NODE_NOT_FOUND", "target node was not found", map[string]any{"node_id": nodeID})
		return
	}
	s.writeJSON(w, http.StatusOK, buildNode(rec))
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request, nodeID string) {
	rec := s.reg.Lookup(nodeID)
	if rec == nil || rec.NodeType != "agent" {
		s.writeError(w, r, http.StatusNotFound, "NODE_NOT_FOUND", "target node was not found", map[string]any{"node_id": nodeID})
		return
	}
	enabled := rec.Status == registry.StatusOnline
	items := []capabilityResponse{
		{Name: "fs.list", Type: "builtin", RiskLevel: "readonly", Enabled: enabled},
		{Name: "fs.read", Type: "builtin", RiskLevel: "readonly", Enabled: enabled},
		{Name: "fs.stat", Type: "builtin", RiskLevel: "readonly", Enabled: enabled},
		{Name: "fs.write", Type: "builtin", RiskLevel: "mutating", Enabled: enabled},
		{Name: "sys.info", Type: "builtin", RiskLevel: "readonly", Enabled: enabled},
		{Name: "sys.hardware", Type: "builtin", RiskLevel: "readonly", Enabled: enabled},
	}
	templates := s.admin.ListTemplates(admin.TemplateListFilter{Limit: 200}).Items
	for _, tpl := range templates {
		items = append(items, capabilityResponse{
			Name:      tpl.Name,
			Type:      "command_template",
			RiskLevel: tpl.RiskLevel,
			Enabled:   enabled && tpl.Enabled,
		})
	}
	s.writeJSON(w, http.StatusOK, listResponse[capabilityResponse]{Items: items, NextCursor: ""})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request, nodeID, action string) {
	params, err := decodeJSONMap(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	meta := requestMetaFromContext(r.Context())
	invocation, result, svcErr := s.admin.DirectInvokeBuiltin(r.Context(), meta, nodeID, action, params)
	if svcErr != nil {
		s.writeServiceError(w, r, svcErr)
		return
	}
	if result != nil && result.Error != nil {
		s.writeInvocationError(w, r, result.Error)
		return
	}
	_ = invocation
	if result == nil {
		s.writeJSON(w, http.StatusOK, successEnvelope{Data: map[string]any{}})
		return
	}
	s.writeJSON(w, http.StatusOK, successEnvelope{Data: result.Data})
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	filter := admin.TemplateListFilter{
		Limit:     mustPositiveQuery(r, "limit", 50),
		Cursor:    r.URL.Query().Get("cursor"),
		Name:      r.URL.Query().Get("name"),
		RiskLevel: r.URL.Query().Get("risk_level"),
		TargetOS:  r.URL.Query().Get("target_os"),
	}
	if raw := r.URL.Query().Get("enabled"); raw != "" {
		val := raw == "true"
		filter.Enabled = &val
	}
	page := s.admin.ListTemplates(filter)
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req admin.CreateTemplateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	tpl, err := s.admin.CreateTemplate(requestMetaFromContext(r.Context()), req)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, tpl)
}

func (s *Server) handleTemplateRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/command-templates/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		s.writeError(w, r, http.StatusNotFound, "COMMAND_TEMPLATE_NOT_FOUND", "command template not found", nil)
		return
	}
	if strings.HasSuffix(path, ":invoke") {
		templateID := strings.TrimSuffix(path, ":invoke")
		s.handleInvokeTemplate(w, r, templateID)
		return
	}
	templateID, err := url.PathUnescape(path)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid template id", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		tpl, svcErr := s.admin.GetTemplate(templateID)
		if svcErr != nil {
			s.writeServiceError(w, r, svcErr)
			return
		}
		s.writeJSON(w, http.StatusOK, tpl)
	case http.MethodPatch:
		var req admin.UpdateTemplateRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		tpl, svcErr := s.admin.UpdateTemplate(requestMetaFromContext(r.Context()), templateID, req)
		if svcErr != nil {
			s.writeServiceError(w, r, svcErr)
			return
		}
		s.writeJSON(w, http.StatusOK, tpl)
	default:
		s.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "endpoint not found", nil)
	}
}

func (s *Server) handleInvokeTemplate(w http.ResponseWriter, r *http.Request, templateID string) {
	var req admin.CreateInvocationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	resp, err := s.admin.InvokeTemplate(r.Context(), requestMetaFromContext(r.Context()), templateID, req)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	status := http.StatusOK
	if resp.Invocation.Async {
		status = http.StatusAccepted
	}
	s.writeJSON(w, status, resp)
}

func (s *Server) handleCreateInvocation(w http.ResponseWriter, r *http.Request) {
	var req admin.CreateInvocationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	resp, err := s.admin.CreateInvocation(r.Context(), requestMetaFromContext(r.Context()), req)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	status := http.StatusOK
	if resp.Invocation.Async {
		status = http.StatusAccepted
	}
	s.writeJSON(w, status, resp)
}

func (s *Server) handleListInvocations(w http.ResponseWriter, r *http.Request) {
	page := s.admin.ListInvocations(admin.InvocationListFilter{
		Limit:      mustPositiveQuery(r, "limit", 50),
		Cursor:     r.URL.Query().Get("cursor"),
		Status:     r.URL.Query().Get("status"),
		Action:     r.URL.Query().Get("action"),
		ActionType: r.URL.Query().Get("action_type"),
	})
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleInvocationRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/invocations/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		s.writeError(w, r, http.StatusNotFound, "INVOCATION_NOT_FOUND", "invocation not found", nil)
		return
	}
	switch {
	case strings.HasSuffix(path, "/results"):
		id := strings.TrimSuffix(path, "/results")
		results, err := s.admin.GetInvocationResults(id)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		s.writeJSON(w, http.StatusOK, listResponse[admin.InvocationResult]{Items: results, NextCursor: ""})
	case strings.HasSuffix(path, ":cancel") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(path, ":cancel")
		inv, err := s.admin.CancelInvocation(id)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"invocation": inv})
	case r.Method == http.MethodGet:
		inv, err := s.admin.GetInvocation(path)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		s.writeJSON(w, http.StatusOK, inv)
	default:
		s.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "endpoint not found", nil)
	}
}

func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	filter := admin.AuditFilter{
		Limit:     mustPositiveQuery(r, "limit", 50),
		Cursor:    r.URL.Query().Get("cursor"),
		Action:    r.URL.Query().Get("action"),
		Decision:  r.URL.Query().Get("decision"),
		NodeID:    r.URL.Query().Get("node_id"),
		SubjectID: r.URL.Query().Get("subject_id"),
	}
	page := s.admin.ListAuditEvents(filter)
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleAuditEventRoutes(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/v1/audit/events/")
	evt, err := s.admin.GetAuditEvent(id)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, evt)
}

func requestMetaFromContext(ctx context.Context) admin.RequestMeta {
	identity, _ := tokenauth.IdentityFromContext(ctx)
	return admin.RequestMeta{
		RequestID: requestIDFromContext(ctx),
		Source:    "api",
		Identity:  identity,
	}
}

func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var svcErr *admin.Error
	if errors.As(err, &svcErr) {
		s.writeError(w, r, svcErr.Status, svcErr.Code, svcErr.Message, svcErr.Details)
		return
	}
	s.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
}

func (s *Server) writeInvocationError(w http.ResponseWriter, r *http.Request, invErr *admin.InvocationError) {
	status := http.StatusInternalServerError
	switch invErr.Code {
	case "NODE_NOT_FOUND":
		status = http.StatusNotFound
	case "NODE_OFFLINE", "TIMEOUT":
		status = http.StatusServiceUnavailable
	case "CANCELED":
		status = http.StatusConflict
	case "POLICY_DENIED", "FORBIDDEN", "CAPABILITY_DISABLED", "COMMAND_FAILED":
		status = http.StatusForbidden
	}
	s.writeError(w, r, status, invErr.Code, invErr.Message, invErr.Details)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	s.writeJSON(w, status, errorEnvelope{
		Error: apiError{
			Code:      code,
			Message:   message,
			RequestID: requestIDFromContext(r.Context()),
			Details:   details,
		},
	})
}

func buildNode(rec *registry.AgentRecord) nodeResponse {
	platform := nodePlatform{}
	if rec.OS != "" {
		parts := strings.Split(rec.OS, "/")
		platform.OS = parts[0]
		if len(parts) > 1 {
			platform.Arch = parts[1]
		}
	}
	return nodeResponse{
		ID:       rec.Hostname,
		Hostname: rec.Hostname,
		Labels:   map[string]string{},
		Status:   string(rec.Status),
		Platform: platform,
		Agent: nodeAgent{
			Version:      rec.AgentVersion,
			ConnectedVia: append([]string(nil), rec.ProxyPath...),
		},
		LastSeenAt:   rec.LastHeartbeat,
		RegisteredAt: rec.RegisteredAt,
	}
}

func requestIDFromContext(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDKey).(string); ok {
		return requestID
	}
	return ""
}

func decodeJSONBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func decodeJSONMap(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	if r.ContentLength == 0 {
		return map[string]any{}, nil
	}
	var dst map[string]any
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dst); err != nil {
		return nil, err
	}
	if dst == nil {
		dst = map[string]any{}
	}
	return dst, nil
}

func parsePositiveIntQuery(r *http.Request, key string, defaultValue, maxValue int) (int, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return defaultValue, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	if maxValue > 0 && n > maxValue {
		n = maxValue
	}
	return n, true
}

func mustPositiveQuery(r *http.Request, key string, defaultValue int) int {
	value, ok := parsePositiveIntQuery(r, key, defaultValue, 200)
	if !ok {
		return defaultValue
	}
	return value
}

func paginate[T any](items []T, cursor string, limit int, idFn func(T) string) listResponse[T] {
	start := 0
	if cursor != "" {
		start = len(items)
		for i, item := range items {
			if idFn(item) == cursor {
				start = i + 1
				break
			}
		}
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = idFn(items[end-1])
	}
	return listResponse[T]{Items: items[start:end], NextCursor: next}
}
