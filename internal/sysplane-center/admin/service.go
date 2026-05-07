package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/jimyag/sysplane/internal/pkg/stream"
	"github.com/jimyag/sysplane/internal/pkg/tokenauth"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
	"github.com/jimyag/sysplane/internal/sysplane-center/router"
)

var (
	errNodeNotFound = errors.New("node not found")
	errNodeOffline  = errors.New("node offline")
)

const (
	// risk levels
	riskReadonly  = "readonly"
	riskMutating  = "mutating"
	riskDangerous = "dangerous"

	// action types
	actionTypeBuiltin         = "builtin"
	actionTypeCommandTemplate = "command_template"

	// invocation/audit statuses
	statusPending   = "pending"
	statusRunning   = "running"
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCanceled  = "canceled"
	statusPartial   = "partial"
)

type RemoteForwarder interface {
	ForwardIfNeeded(ctx context.Context, requestID, targetHost, toolName, argsJSON string) (string, bool, error)
}

type CallLogger interface {
	InsertToolCallLog(ctx context.Context, requestID, centerID, targetHost, toolName, argsJSON string) error
	CompleteToolCallLog(ctx context.Context, requestID, resultJSON, errorMsg string) error
}

type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	return e.Message
}

type RequestMeta struct {
	RequestID string
	Source    string
	Identity  tokenauth.Identity
}

type CommandTemplate struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	RiskLevel         string         `json:"risk_level"`
	TargetOS          []string       `json:"target_os"`
	Executor          TemplateExec   `json:"executor"`
	ParamsSchema      map[string]any `json:"params_schema"`
	DefaultTimeoutSec int            `json:"default_timeout_sec"`
	MaxTimeoutSec     int            `json:"max_timeout_sec"`
	MaxOutputBytes    int            `json:"max_output_bytes"`
	Enabled           bool           `json:"enabled"`
	CreatedBy         string         `json:"created_by"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type TemplateExec struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type TemplateListFilter struct {
	Limit     int
	Cursor    string
	Name      string
	Enabled   *bool
	RiskLevel string
	TargetOS  string
}

type CreateTemplateRequest struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	RiskLevel         string         `json:"risk_level"`
	TargetOS          []string       `json:"target_os"`
	Executor          TemplateExec   `json:"executor"`
	ParamsSchema      map[string]any `json:"params_schema"`
	DefaultTimeoutSec int            `json:"default_timeout_sec"`
	MaxTimeoutSec     int            `json:"max_timeout_sec"`
	MaxOutputBytes    int            `json:"max_output_bytes"`
}

type UpdateTemplateRequest struct {
	Description       *string        `json:"description,omitempty"`
	RiskLevel         *string        `json:"risk_level,omitempty"`
	TargetOS          []string       `json:"target_os,omitempty"`
	ParamsSchema      map[string]any `json:"params_schema,omitempty"`
	DefaultTimeoutSec *int           `json:"default_timeout_sec,omitempty"`
	MaxTimeoutSec     *int           `json:"max_timeout_sec,omitempty"`
	MaxOutputBytes    *int           `json:"max_output_bytes,omitempty"`
	Enabled           *bool          `json:"enabled,omitempty"`
}

type Invocation struct {
	ID          string      `json:"id"`
	Action      string      `json:"action"`
	ActionType  string      `json:"action_type"`
	Status      string      `json:"status"`
	Async       bool        `json:"async"`
	Targets     Targets     `json:"targets"`
	Params      any         `json:"params"`
	RequestedBy RequestedBy `json:"requested_by"`
	TimeoutSec  int         `json:"timeout_sec"`
	CreatedAt   time.Time   `json:"created_at"`
	StartedAt   *time.Time  `json:"started_at"`
	FinishedAt  *time.Time  `json:"finished_at"`
}

type Targets struct {
	NodeIDs []string `json:"node_ids"`
}

type RequestedBy struct {
	SubjectID string `json:"subject_id"`
	Source    string `json:"source"`
}

type InvocationResult struct {
	NodeID     string           `json:"node_id"`
	Hostname   string           `json:"hostname"`
	Status     string           `json:"status"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Data       any              `json:"data"`
	Error      *InvocationError `json:"error"`
}

type InvocationError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type CreateInvocationRequest struct {
	Action     string  `json:"action"`
	ActionType string  `json:"action_type"`
	Targets    Targets `json:"targets"`
	Params     any     `json:"params"`
	TimeoutSec int     `json:"timeout_sec"`
	Async      bool    `json:"async"`
}

type InvocationListFilter struct {
	Limit      int
	Cursor     string
	Status     string
	Action     string
	ActionType string
}

type AuditEvent struct {
	ID             string     `json:"id"`
	RequestID      string     `json:"request_id"`
	InvocationID   string     `json:"invocation_id,omitempty"`
	SubjectID      string     `json:"subject_id"`
	TokenType      string     `json:"token_type"`
	Source         string     `json:"source"`
	Action         string     `json:"action"`
	ActionType     string     `json:"action_type"`
	TargetNodeIDs  []string   `json:"target_node_ids"`
	RiskLevel      string     `json:"risk_level"`
	Decision       string     `json:"decision"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

type AuditFilter struct {
	Limit     int
	Cursor    string
	Action    string
	Decision  string
	NodeID    string
	SubjectID string
}

type ListPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type InvocationResponse struct {
	Invocation *Invocation        `json:"invocation"`
	Results    []InvocationResult `json:"results,omitempty"`
}

// Service manages invocations, templates, and audit events entirely in memory.
// All state is lost on center restart; there is no persistence for these maps.
// tool_call_logs are persisted to PostgreSQL via the CallLogger, but templates,
// invocations, results, and audit events are not. Extend the store schema to
// persist these if durability across restarts is required.
type Service struct {
	reg        *registry.Registry
	rtr        *router.Router
	fwd        RemoteForwarder
	log        CallLogger
	instanceID string

	mu              sync.RWMutex
	templates       map[string]*CommandTemplate
	templateNameIDs map[string]string
	invocations     map[string]*Invocation
	results         map[string][]InvocationResult
	audits          map[string]*AuditEvent
	auditByInvoke   map[string]string
	cancelFns       sync.Map
}

func NewService(reg *registry.Registry, rtr *router.Router, fwd RemoteForwarder, log CallLogger, instanceID string) *Service {
	return &Service{
		reg:             reg,
		rtr:             rtr,
		fwd:             fwd,
		log:             log,
		instanceID:      instanceID,
		templates:       map[string]*CommandTemplate{},
		templateNameIDs: map[string]string{},
		invocations:     map[string]*Invocation{},
		results:         map[string][]InvocationResult{},
		audits:          map[string]*AuditEvent{},
		auditByInvoke:   map[string]string{},
	}
}

func (s *Service) DirectInvokeBuiltin(ctx context.Context, meta RequestMeta, nodeID, action string, params map[string]any) (*Invocation, *InvocationResult, error) {
	resp, err := s.CreateInvocation(ctx, meta, CreateInvocationRequest{
		Action:     normalizeAction(action),
		ActionType: actionTypeBuiltin,
		Targets:    Targets{NodeIDs: []string{nodeID}},
		Params:     params,
		Async:      false,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(resp.Results) == 0 {
		return resp.Invocation, nil, nil
	}
	result := resp.Results[0]
	return resp.Invocation, &result, nil
}

func (s *Service) CreateInvocation(ctx context.Context, meta RequestMeta, req CreateInvocationRequest) (*InvocationResponse, error) {
	meta = normalizeMeta(meta)
	plan, err := s.preparePlan(meta, req)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	invocation := &Invocation{
		ID:         stream.NewRequestID("inv"),
		Action:     plan.Action,
		ActionType: plan.ActionType,
		Status:     statusPending,
		Async:      req.Async,
		Targets:    Targets{NodeIDs: append([]string(nil), plan.Targets...)},
		Params:     cloneAny(plan.Params),
		RequestedBy: RequestedBy{
			SubjectID: meta.Identity.SubjectID,
			Source:    meta.Source,
		},
		TimeoutSec: plan.TimeoutSec,
		CreatedAt:  now,
	}

	s.mu.Lock()
	s.invocations[invocation.ID] = cloneInvocation(invocation)
	s.results[invocation.ID] = nil
	s.mu.Unlock()
	s.recordAuditStart(meta, invocation, plan.RiskLevel)

	if req.Async {
		runCtx, cancel := context.WithCancel(context.Background())
		s.cancelFns.Store(invocation.ID, cancel)
		go s.executeInvocation(runCtx, meta, invocation.ID, plan)
		return &InvocationResponse{Invocation: cloneInvocation(invocation)}, nil
	}

	results := s.executeInvocation(ctx, meta, invocation.ID, plan)
	inv, _ := s.GetInvocation(invocation.ID)
	return &InvocationResponse{Invocation: inv, Results: results}, nil
}

func (s *Service) InvokeTemplate(ctx context.Context, meta RequestMeta, templateID string, body CreateInvocationRequest) (*InvocationResponse, error) {
	body.ActionType = actionTypeCommandTemplate
	body.Action = templateID
	return s.CreateInvocation(ctx, meta, body)
}

func (s *Service) GetInvocation(id string) (*Invocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.invocations[id]
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: "INVOCATION_NOT_FOUND", Message: "invocation not found"}
	}
	return cloneInvocation(inv), nil
}

func (s *Service) ListInvocations(filter InvocationListFilter) ListPage[Invocation] {
	s.mu.RLock()
	items := make([]Invocation, 0, len(s.invocations))
	for _, inv := range s.invocations {
		if filter.Status != "" && inv.Status != filter.Status {
			continue
		}
		if filter.Action != "" && inv.Action != filter.Action {
			continue
		}
		if filter.ActionType != "" && inv.ActionType != filter.ActionType {
			continue
		}
		items = append(items, *cloneInvocation(inv))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return paginate(items, filter.Cursor, normalizeLimit(filter.Limit), func(item Invocation) string { return item.ID })
}

func (s *Service) GetInvocationResults(id string) ([]InvocationResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	results, ok := s.results[id]
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: "INVOCATION_NOT_FOUND", Message: "invocation not found"}
	}
	out := make([]InvocationResult, len(results))
	for i := range results {
		out[i] = cloneResult(results[i])
	}
	return out, nil
}

func (s *Service) CancelInvocation(id string) (*Invocation, error) {
	s.mu.RLock()
	inv, ok := s.invocations[id]
	s.mu.RUnlock()
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: "INVOCATION_NOT_FOUND", Message: "invocation not found"}
	}
	if inv.Status == statusSucceeded || inv.Status == statusFailed || inv.Status == statusPartial || inv.Status == statusCanceled {
		return cloneInvocation(inv), nil
	}
	if cancel, ok := s.cancelFns.Load(id); ok {
		cancel.(context.CancelFunc)()
	}
	return s.GetInvocation(id)
}

func (s *Service) ListTemplates(filter TemplateListFilter) ListPage[CommandTemplate] {
	s.mu.RLock()
	items := make([]CommandTemplate, 0, len(s.templates))
	for _, tpl := range s.templates {
		if filter.Name != "" && !strings.Contains(tpl.Name, filter.Name) {
			continue
		}
		if filter.Enabled != nil && tpl.Enabled != *filter.Enabled {
			continue
		}
		if filter.RiskLevel != "" && tpl.RiskLevel != filter.RiskLevel {
			continue
		}
		if filter.TargetOS != "" && !slices.Contains(tpl.TargetOS, filter.TargetOS) {
			continue
		}
		items = append(items, *cloneTemplate(tpl))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return paginate(items, filter.Cursor, normalizeLimit(filter.Limit), func(item CommandTemplate) string { return item.ID })
}

func (s *Service) CreateTemplate(meta RequestMeta, req CreateTemplateRequest) (*CommandTemplate, error) {
	meta = normalizeMeta(meta)
	if meta.Identity.Domain != tokenauth.DomainAdmin {
		return nil, &Error{Status: http.StatusForbidden, Code: "FORBIDDEN", Message: "admin token is required"}
	}
	tpl, err := buildTemplate(meta, req)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.templateNameIDs[tpl.Name]; ok {
		return nil, &Error{Status: http.StatusConflict, Code: "CONFLICT", Message: "template name already exists"}
	}
	s.templates[tpl.ID] = cloneTemplate(tpl)
	s.templateNameIDs[tpl.Name] = tpl.ID
	eventID := stream.NewRequestID("evt")
	s.audits[eventID] = &AuditEvent{
		ID:            eventID,
		RequestID:     meta.RequestID,
		SubjectID:     meta.Identity.SubjectID,
		TokenType:     string(meta.Identity.Domain),
		Source:        meta.Source,
		Action:        tpl.Name,
		ActionType:    actionTypeCommandTemplate,
		TargetNodeIDs: nil,
		RiskLevel:     tpl.RiskLevel,
		Decision:      "created",
		CreatedAt:     time.Now().UTC(),
	}
	return cloneTemplate(tpl), nil
}

func (s *Service) GetTemplate(id string) (*CommandTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tpl, ok := s.templates[id]
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: "COMMAND_TEMPLATE_NOT_FOUND", Message: "command template not found"}
	}
	return cloneTemplate(tpl), nil
}

func (s *Service) UpdateTemplate(meta RequestMeta, id string, patch UpdateTemplateRequest) (*CommandTemplate, error) {
	meta = normalizeMeta(meta)
	if meta.Identity.Domain != tokenauth.DomainAdmin {
		return nil, &Error{Status: http.StatusForbidden, Code: "FORBIDDEN", Message: "admin token is required"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tpl, ok := s.templates[id]
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: "COMMAND_TEMPLATE_NOT_FOUND", Message: "command template not found"}
	}

	if patch.Description != nil {
		tpl.Description = *patch.Description
	}
	if patch.RiskLevel != nil {
		if err := validateRisk(*patch.RiskLevel); err != nil {
			return nil, err
		}
		tpl.RiskLevel = *patch.RiskLevel
	}
	if patch.TargetOS != nil {
		tpl.TargetOS = append([]string(nil), patch.TargetOS...)
	}
	if patch.ParamsSchema != nil {
		if err := validateSchema(patch.ParamsSchema); err != nil {
			return nil, err
		}
		tpl.ParamsSchema = cloneMap(patch.ParamsSchema)
	}
	if patch.DefaultTimeoutSec != nil {
		tpl.DefaultTimeoutSec = *patch.DefaultTimeoutSec
	}
	if patch.MaxTimeoutSec != nil {
		tpl.MaxTimeoutSec = *patch.MaxTimeoutSec
	}
	if patch.MaxOutputBytes != nil {
		tpl.MaxOutputBytes = *patch.MaxOutputBytes
	}
	if patch.Enabled != nil {
		tpl.Enabled = *patch.Enabled
	}
	if tpl.DefaultTimeoutSec <= 0 || tpl.MaxTimeoutSec <= 0 || tpl.DefaultTimeoutSec > tpl.MaxTimeoutSec {
		return nil, &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: "default_timeout_sec must be > 0 and <= max_timeout_sec"}
	}
	tpl.UpdatedAt = time.Now().UTC()
	eventID := stream.NewRequestID("evt")
	s.audits[eventID] = &AuditEvent{
		ID:         eventID,
		RequestID:  meta.RequestID,
		SubjectID:  meta.Identity.SubjectID,
		TokenType:  string(meta.Identity.Domain),
		Source:     meta.Source,
		Action:     tpl.Name,
		ActionType: actionTypeCommandTemplate,
		RiskLevel:  tpl.RiskLevel,
		Decision:   "updated",
		CreatedAt:  time.Now().UTC(),
	}
	return cloneTemplate(tpl), nil
}

func (s *Service) ListAuditEvents(filter AuditFilter) ListPage[AuditEvent] {
	s.mu.RLock()
	items := make([]AuditEvent, 0, len(s.audits))
	for _, evt := range s.audits {
		if filter.Action != "" && evt.Action != filter.Action {
			continue
		}
		if filter.Decision != "" && evt.Decision != filter.Decision {
			continue
		}
		if filter.SubjectID != "" && evt.SubjectID != filter.SubjectID {
			continue
		}
		if filter.NodeID != "" && !slices.Contains(evt.TargetNodeIDs, filter.NodeID) {
			continue
		}
		items = append(items, *cloneAudit(evt))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return paginate(items, filter.Cursor, normalizeLimit(filter.Limit), func(item AuditEvent) string { return item.ID })
}

func (s *Service) GetAuditEvent(id string) (*AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	evt, ok := s.audits[id]
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: "AUDIT_EVENT_NOT_FOUND", Message: "audit event not found"}
	}
	return cloneAudit(evt), nil
}

type executionPlan struct {
	Action     string
	ActionType string
	Targets    []string
	Params     map[string]any
	TimeoutSec int
	RiskLevel  string
	Template   *CommandTemplate
}

func (s *Service) preparePlan(meta RequestMeta, req CreateInvocationRequest) (*executionPlan, error) {
	actionType := strings.TrimSpace(req.ActionType)
	action := normalizeAction(req.Action)
	if action == "" {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "action is required"}
	}
	if actionType != actionTypeBuiltin && actionType != actionTypeCommandTemplate {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "action_type must be builtin or command_template"}
	}
	if len(req.Targets.NodeIDs) == 0 {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "targets.node_ids must not be empty"}
	}

	params, err := toMap(req.Params)
	if err != nil {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "params must be an object"}
	}

	plan := &executionPlan{
		Action:     action,
		ActionType: actionType,
		Targets:    dedupe(req.Targets.NodeIDs),
		Params:     params,
	}
	if actionType == actionTypeBuiltin {
		spec, err := builtinSpecFor(action)
		if err != nil {
			return nil, err
		}
		if err := allowRisk(meta.Identity.Domain, spec.RiskLevel); err != nil {
			return nil, err
		}
		plan.TimeoutSec = normalizeTimeout(req.TimeoutSec, 10, 30)
		plan.RiskLevel = spec.RiskLevel
		return plan, nil
	}

	tpl, err := s.resolveTemplate(action)
	if err != nil {
		return nil, err
	}
	if !tpl.Enabled {
		return nil, &Error{Status: http.StatusUnprocessableEntity, Code: "COMMAND_TEMPLATE_DISABLED", Message: "command template is disabled"}
	}
	if err := allowRisk(meta.Identity.Domain, tpl.RiskLevel); err != nil {
		return nil, err
	}
	if err := validateParams(tpl.ParamsSchema, params); err != nil {
		return nil, err
	}
	plan.Action = tpl.Name
	plan.Template = tpl
	plan.TimeoutSec = normalizeTimeout(req.TimeoutSec, tpl.DefaultTimeoutSec, tpl.MaxTimeoutSec)
	plan.RiskLevel = tpl.RiskLevel
	return plan, nil
}

func (s *Service) executeInvocation(ctx context.Context, _ RequestMeta, invocationID string, plan *executionPlan) []InvocationResult {
	started := time.Now().UTC()
	s.mu.Lock()
	if inv, ok := s.invocations[invocationID]; ok {
		inv.Status = statusRunning
		inv.StartedAt = &started
	}
	s.mu.Unlock()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make([]InvocationResult, 0, len(plan.Targets))
	)
	for _, nodeID := range plan.Targets {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			result := s.executeTarget(ctx, invocationID, nodeID, plan)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(nodeID)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].NodeID < results[j].NodeID })
	finished := time.Now().UTC()
	finalStatus := summarizeInvocationStatus(results, ctx.Err())

	s.mu.Lock()
	if inv, ok := s.invocations[invocationID]; ok {
		inv.Status = finalStatus
		if inv.StartedAt == nil {
			inv.StartedAt = &started
		}
		inv.FinishedAt = &finished
	}
	s.results[invocationID] = cloneResults(results)
	s.mu.Unlock()
	s.finishAudit(invocationID, finalStatus, finished)
	s.cancelFns.Delete(invocationID)
	return cloneResults(results)
}

func (s *Service) executeTarget(ctx context.Context, requestID, nodeID string, plan *executionPlan) InvocationResult {
	started := time.Now().UTC()
	result := InvocationResult{
		NodeID:    nodeID,
		Hostname:  nodeID,
		Status:    statusFailed,
		StartedAt: started,
	}

	var data any
	var err *InvocationError
	if plan.ActionType == actionTypeBuiltin {
		data, err = s.executeBuiltin(ctx, requestID, nodeID, plan)
	} else {
		data, err = s.executeTemplate(ctx, requestID, nodeID, plan)
	}

	finished := time.Now().UTC()
	result.FinishedAt = finished
	if err != nil {
		result.Status = statusFromErrorCode(err.Code)
		result.Error = err
		return result
	}
	result.Status = statusSucceeded
	result.Data = data
	return result
}

func (s *Service) executeBuiltin(ctx context.Context, requestID, nodeID string, plan *executionPlan) (any, *InvocationError) {
	spec, err := builtinSpecFor(plan.Action)
	if err != nil {
		return nil, &InvocationError{Code: "INVALID_ARGUMENT", Message: err.Message}
	}
	toolName, argsJSON, err2 := spec.Marshal(plan.Params)
	if err2 != nil {
		return nil, &InvocationError{Code: "INVALID_ARGUMENT", Message: err2.Error()}
	}
	data, invokeErr := s.invokeToolWithRequest(ctx, requestID, nodeID, toolName, argsJSON)
	if invokeErr != nil {
		return nil, classifyInvokeError(nodeID, invokeErr)
	}
	return spec.Transform(data)
}

func (s *Service) executeTemplate(ctx context.Context, requestID, nodeID string, plan *executionPlan) (any, *InvocationError) {
	if !matchesTargetOS(plan.Template.TargetOS, nodeOS(s.reg.Lookup(nodeID))) {
		return nil, &InvocationError{Code: "CAPABILITY_DISABLED", Message: "template is not enabled for target node platform"}
	}
	command, args, err := renderTemplateCommand(plan.Template, plan.Params)
	if err != nil {
		return nil, &InvocationError{Code: "INVALID_ARGUMENT", Message: err.Error()}
	}
	payload := map[string]any{
		"command":          command,
		"args":             args,
		"timeout_sec":      plan.TimeoutSec,
		"max_output_bytes": plan.Template.MaxOutputBytes,
	}
	raw, _ := json.Marshal(payload)
	data, invokeErr := s.invokeToolWithRequest(ctx, requestID+"-"+nodeID, nodeID, "run_process", string(raw))
	if invokeErr != nil {
		return nil, classifyInvokeError(nodeID, invokeErr)
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return data, nil
	}
	if success, _ := obj["success"].(bool); !success {
		exitCode, _ := obj["exit_code"].(float64)
		return nil, &InvocationError{
			Code:    "COMMAND_FAILED",
			Message: fmt.Sprintf("process exited with code %.0f", exitCode),
			Details: map[string]any{
				"stdout":    obj["stdout"],
				"stderr":    obj["stderr"],
				"exit_code": obj["exit_code"],
			},
		}
	}
	return obj, nil
}

func (s *Service) invokeToolWithRequest(ctx context.Context, requestID, nodeID, toolName, argsJSON string) (any, error) {
	rec := s.reg.Lookup(nodeID)
	if rec == nil {
		if s.fwd != nil {
			if s.log != nil {
				_ = s.log.InsertToolCallLog(ctx, requestID, s.instanceID, nodeID, toolName, argsJSON)
			}
			resultJSON, forwarded, err := s.fwd.ForwardIfNeeded(ctx, requestID, nodeID, toolName, argsJSON)
			if s.log != nil {
				errMsg := ""
				if err != nil {
					errMsg = err.Error()
				}
				_ = s.log.CompleteToolCallLog(ctx, requestID, resultJSON, errMsg)
			}
			if forwarded {
				if err != nil {
					return nil, err
				}
				return decodeResult(resultJSON), nil
			}
		}
		return nil, errNodeNotFound
	}
	if rec.NodeType != "agent" {
		return nil, errNodeNotFound
	}
	if rec.Status != registry.StatusOnline {
		return nil, errNodeOffline
	}
	if s.log != nil {
		_ = s.log.InsertToolCallLog(ctx, requestID, s.instanceID, nodeID, toolName, argsJSON)
	}
	resultJSON, err := s.rtr.Send(ctx, rec, requestID, toolName, argsJSON)
	if s.log != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		_ = s.log.CompleteToolCallLog(ctx, requestID, resultJSON, errMsg)
	}
	if err != nil {
		return nil, err
	}
	return decodeResult(resultJSON), nil
}

func (s *Service) resolveTemplate(idOrName string) (*CommandTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tpl, ok := s.templates[idOrName]; ok {
		return cloneTemplate(tpl), nil
	}
	if id, ok := s.templateNameIDs[idOrName]; ok {
		if tpl, ok := s.templates[id]; ok {
			return cloneTemplate(tpl), nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Code: "COMMAND_TEMPLATE_NOT_FOUND", Message: "command template not found"}
}

func (s *Service) recordAuditStart(meta RequestMeta, inv *Invocation, riskLevel string) {
	evt := &AuditEvent{
		ID:            stream.NewRequestID("evt"),
		RequestID:     meta.RequestID,
		InvocationID:  inv.ID,
		SubjectID:     meta.Identity.SubjectID,
		TokenType:     string(meta.Identity.Domain),
		Source:        meta.Source,
		Action:        inv.Action,
		ActionType:    inv.ActionType,
		TargetNodeIDs: append([]string(nil), inv.Targets.NodeIDs...),
		RiskLevel:     riskLevel,
		Decision:      "accepted",
		CreatedAt:     time.Now().UTC(),
	}
	s.mu.Lock()
	s.audits[evt.ID] = evt
	s.auditByInvoke[inv.ID] = evt.ID
	s.mu.Unlock()
}

func (s *Service) finishAudit(invocationID, decision string, finished time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	eventID := s.auditByInvoke[invocationID]
	if eventID == "" {
		return
	}
	if evt, ok := s.audits[eventID]; ok {
		evt.Decision = decision
		evt.FinishedAt = &finished
	}
}

type builtinSpec struct {
	RiskLevel string
	Marshal   func(map[string]any) (string, string, error)
	Transform func(any) (any, *InvocationError)
}

func builtinSpecFor(action string) (*builtinSpec, *Error) {
	switch normalizeAction(action) {
	case "fs.list":
		return &builtinSpec{
			RiskLevel: riskReadonly,
			Marshal: func(params map[string]any) (string, string, error) {
				return marshalToolArgs("list_directory", map[string]any{
					"path":        asString(params["path"]),
					"show_hidden": asBool(params["show_hidden"]),
					"limit":       asInt(params["limit"]),
				})
			},
			Transform: func(data any) (any, *InvocationError) {
				obj, ok := data.(map[string]any)
				if !ok {
					return data, nil
				}
				return map[string]any{"entries": obj["items"]}, nil
			},
		}, nil
	case "fs.read":
		return &builtinSpec{
			RiskLevel: riskReadonly,
			Marshal: func(params map[string]any) (string, string, error) {
				return marshalToolArgs("read_file", map[string]any{
					"path":   asString(params["path"]),
					"offset": asInt64(params["offset"]),
					"length": asInt64(params["length"]),
				})
			},
			Transform: func(data any) (any, *InvocationError) {
				obj, ok := data.(map[string]any)
				if !ok {
					return data, nil
				}
				return map[string]any{
					"content":   obj["content"],
					"encoding":  valueOr(obj["encoding"], "utf-8"),
					"truncated": obj["truncated"],
				}, nil
			},
		}, nil
	case "fs.stat":
		return &builtinSpec{
			RiskLevel: riskReadonly,
			Marshal: func(params map[string]any) (string, string, error) {
				return marshalToolArgs("stat_file", map[string]any{"path": asString(params["path"])})
			},
			Transform: func(data any) (any, *InvocationError) {
				obj, ok := data.(map[string]any)
				if !ok {
					return data, nil
				}
				return map[string]any{
					"name":        obj["name"],
					"type":        obj["type"],
					"size":        obj["size"],
					"mode":        firstNonNil(obj["mode"], obj["permissions"]),
					"modified_at": obj["modified_at"],
				}, nil
			},
		}, nil
	case "fs.write":
		return &builtinSpec{
			RiskLevel: riskMutating,
			Marshal: func(params map[string]any) (string, string, error) {
				return marshalToolArgs("write_file", map[string]any{
					"path":      asString(params["path"]),
					"content":   asString(params["content"]),
					"overwrite": asBool(params["overwrite"]),
				})
			},
			Transform: func(data any) (any, *InvocationError) { return data, nil },
		}, nil
	case "sys.hardware":
		return &builtinSpec{
			RiskLevel: riskReadonly,
			Marshal: func(_ map[string]any) (string, string, error) {
				return "get_hardware_info", "{}", nil
			},
			Transform: func(data any) (any, *InvocationError) { return data, nil },
		}, nil
	case "sys.info":
		return &builtinSpec{
			RiskLevel: riskReadonly,
			Marshal: func(_ map[string]any) (string, string, error) {
				return "get_hardware_info", "{}", nil
			},
			Transform: func(data any) (any, *InvocationError) {
				obj, ok := data.(map[string]any)
				if !ok {
					return data, nil
				}
				sys, _ := obj["system"].(map[string]any)
				return map[string]any{
					"hostname":   sys["hostname"],
					"os":         sys["os"],
					"arch":       guessArch(sys, obj),
					"kernel":     sys["kernel_version"],
					"uptime_sec": sys["uptime_seconds"],
				}, nil
			},
		}, nil
	default:
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("unsupported action %q", action)}
	}
}

func buildTemplate(meta RequestMeta, req CreateTemplateRequest) (*CommandTemplate, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "name is required"}
	}
	if err := validateRisk(req.RiskLevel); err != nil {
		return nil, err
	}
	if req.Executor.Type != "process" {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "executor.type must be process"}
	}
	if !filepath.IsAbs(req.Executor.Command) {
		return nil, &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "executor.command must be an absolute path"}
	}
	if err := validateSchema(req.ParamsSchema); err != nil {
		return nil, err
	}
	if req.DefaultTimeoutSec <= 0 || req.MaxTimeoutSec <= 0 || req.DefaultTimeoutSec > req.MaxTimeoutSec {
		return nil, &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: "default_timeout_sec must be > 0 and <= max_timeout_sec"}
	}
	if req.MaxOutputBytes <= 0 {
		req.MaxOutputBytes = 256 * 1024
	}
	now := time.Now().UTC()
	return &CommandTemplate{
		ID:                stream.NewRequestID("cmdtpl"),
		Name:              req.Name,
		Description:       req.Description,
		RiskLevel:         req.RiskLevel,
		TargetOS:          append([]string(nil), req.TargetOS...),
		Executor:          req.Executor,
		ParamsSchema:      cloneMap(req.ParamsSchema),
		DefaultTimeoutSec: req.DefaultTimeoutSec,
		MaxTimeoutSec:     req.MaxTimeoutSec,
		MaxOutputBytes:    req.MaxOutputBytes,
		Enabled:           true,
		CreatedBy:         meta.Identity.SubjectID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

func validateRisk(risk string) error {
	switch risk {
	case riskReadonly, riskMutating, riskDangerous:
		return nil
	default:
		return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "risk_level must be readonly, mutating, or dangerous"}
	}
}

func allowRisk(domain tokenauth.Domain, risk string) error {
	if domain == tokenauth.DomainAdmin {
		return nil
	}
	if risk != riskReadonly {
		return &Error{Status: http.StatusForbidden, Code: "FORBIDDEN", Message: "admin token is required for high-risk actions"}
	}
	return nil
}

func validateSchema(schema map[string]any) error {
	if len(schema) == 0 {
		return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "params_schema is required"}
	}
	if schema["type"] != "object" {
		return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: "params_schema.type must be object"}
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("params_schema.properties.%s must be an object", name)}
		}
		switch prop["type"] {
		case "string", "boolean", "integer", "number":
		default:
			return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("unsupported schema type for %s", name)}
		}
	}
	return nil
}

func validateParams(schema map[string]any, params map[string]any) error {
	if err := validateSchema(schema); err != nil {
		return err
	}
	props, _ := schema["properties"].(map[string]any)
	requiredRaw, _ := schema["required"].([]any)
	required := map[string]struct{}{}
	for _, raw := range requiredRaw {
		if name, ok := raw.(string); ok {
			required[name] = struct{}{}
		}
	}
	for name := range required {
		if _, ok := params[name]; !ok {
			return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("missing required param %q", name)}
		}
	}
	additional := true
	if raw, ok := schema["additionalProperties"].(bool); ok {
		additional = raw
	}
	for key, val := range params {
		propRaw, ok := props[key]
		if !ok {
			if !additional {
				return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("unexpected param %q", key)}
			}
			continue
		}
		prop := propRaw.(map[string]any)
		switch prop["type"] {
		case "string":
			if _, ok := val.(string); !ok {
				return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("param %q must be a string", key)}
			}
		case "boolean":
			if _, ok := val.(bool); !ok {
				return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("param %q must be a boolean", key)}
			}
		case "integer":
			if !isInteger(val) {
				return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("param %q must be an integer", key)}
			}
		case "number":
			if _, ok := val.(float64); !ok && !isInteger(val) {
				return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("param %q must be a number", key)}
			}
		}
	}
	return nil
}

func renderTemplateCommand(tpl *CommandTemplate, params map[string]any) (string, []string, error) {
	command := tpl.Executor.Command
	args := make([]string, 0, len(tpl.Executor.Args))
	for _, arg := range tpl.Executor.Args {
		rendered, err := renderString(arg, params)
		if err != nil {
			return "", nil, err
		}
		args = append(args, rendered)
	}
	return command, args, nil
}

func renderString(src string, params map[string]any) (string, error) {
	tpl, err := template.New("arg").Option("missingkey=error").Parse(src)
	if err != nil {
		return "", fmt.Errorf("invalid executor arg template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("render executor arg: %w", err)
	}
	return buf.String(), nil
}

func normalizeMeta(meta RequestMeta) RequestMeta {
	if meta.RequestID == "" {
		meta.RequestID = stream.NewRequestID("api")
	}
	if meta.Source == "" {
		meta.Source = "api"
	}
	return meta
}

func normalizeAction(action string) string {
	return strings.ReplaceAll(strings.TrimSpace(action), ":", ".")
}

func normalizeTimeout(requested, defaultSec, maxSec int) int {
	if requested <= 0 {
		requested = defaultSec
	}
	if maxSec > 0 && requested > maxSec {
		requested = maxSec
	}
	if requested <= 0 {
		return 10
	}
	return requested
}

func summarizeInvocationStatus(results []InvocationResult, ctxErr error) string {
	if errors.Is(ctxErr, context.Canceled) {
		return statusCanceled
	}
	success := 0
	canceled := 0
	for _, result := range results {
		switch result.Status {
		case statusSucceeded:
			success++
		case statusCanceled:
			canceled++
		}
	}
	switch {
	case canceled == len(results) && len(results) > 0:
		return statusCanceled
	case success == len(results) && len(results) > 0:
		return statusSucceeded
	case success == 0:
		return statusFailed
	default:
		return statusPartial
	}
}

func statusFromErrorCode(code string) string {
	switch code {
	case "CANCELED":
		return statusCanceled
	default:
		return statusFailed
	}
}

func classifyInvokeError(nodeID string, err error) *InvocationError {
	details := map[string]any{"node_id": nodeID}
	switch {
	case errors.Is(err, errNodeNotFound):
		return &InvocationError{Code: "NODE_NOT_FOUND", Message: "target node was not found", Details: details}
	case errors.Is(err, errNodeOffline):
		return &InvocationError{Code: "NODE_OFFLINE", Message: "target node is offline", Details: details}
	case errors.Is(err, router.ErrCanceled), errors.Is(err, context.Canceled):
		return &InvocationError{Code: "CANCELED", Message: "invocation was canceled", Details: details}
	case errors.Is(err, router.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return &InvocationError{Code: "TIMEOUT", Message: "upstream execution timed out", Details: details}
	default:
		msg := err.Error()
		if strings.Contains(msg, "HOST_NOT_FOUND") || strings.Contains(msg, "agent not found") {
			return &InvocationError{Code: "NODE_NOT_FOUND", Message: "target node was not found", Details: details}
		}
		if strings.Contains(strings.ToLower(msg), "denied") || strings.Contains(msg, "exceeds limit") {
			return &InvocationError{Code: "POLICY_DENIED", Message: msg, Details: details}
		}
		return &InvocationError{Code: "INTERNAL_ERROR", Message: msg, Details: details}
	}
}

func marshalToolArgs(toolName string, payload any) (string, string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("marshal %s request: %w", toolName, err)
	}
	return toolName, string(raw), nil
}

func decodeResult(resultJSON string) any {
	var out any
	if err := json.Unmarshal([]byte(resultJSON), &out); err == nil {
		return out
	}
	return resultJSON
}

func toMap(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	if m, ok := v.(map[string]any); ok {
		return cloneMap(m), nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func paginate[T any](items []T, cursor string, limit int, idFn func(T) string) ListPage[T] {
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
	end := min(start+limit, len(items))
	next := ""
	if end < len(items) {
		next = idFn(items[end-1])
	}
	return ListPage[T]{Items: items[start:end], NextCursor: next}
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func cloneInvocation(inv *Invocation) *Invocation {
	if inv == nil {
		return nil
	}
	cp := *inv
	cp.Targets.NodeIDs = append([]string(nil), inv.Targets.NodeIDs...)
	cp.Params = cloneAny(inv.Params)
	if inv.StartedAt != nil {
		t := *inv.StartedAt
		cp.StartedAt = &t
	}
	if inv.FinishedAt != nil {
		t := *inv.FinishedAt
		cp.FinishedAt = &t
	}
	return &cp
}

func cloneTemplate(tpl *CommandTemplate) *CommandTemplate {
	if tpl == nil {
		return nil
	}
	cp := *tpl
	cp.TargetOS = append([]string(nil), tpl.TargetOS...)
	cp.Executor.Args = append([]string(nil), tpl.Executor.Args...)
	cp.ParamsSchema = cloneMap(tpl.ParamsSchema)
	return &cp
}

func cloneAudit(evt *AuditEvent) *AuditEvent {
	if evt == nil {
		return nil
	}
	cp := *evt
	cp.TargetNodeIDs = append([]string(nil), evt.TargetNodeIDs...)
	if evt.FinishedAt != nil {
		t := *evt.FinishedAt
		cp.FinishedAt = &t
	}
	return &cp
}

func cloneResults(in []InvocationResult) []InvocationResult {
	out := make([]InvocationResult, len(in))
	for i := range in {
		out[i] = cloneResult(in[i])
	}
	return out
}

func cloneResult(in InvocationResult) InvocationResult {
	out := in
	if in.Error != nil {
		errCopy := *in.Error
		errCopy.Details = cloneMap(in.Error.Details)
		out.Error = &errCopy
	}
	out.Data = cloneAny(in.Data)
	return out
}

func cloneAny(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func dedupe(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}


func matchesTargetOS(targets []string, osName string) bool {
	if len(targets) == 0 || osName == "" {
		return true
	}
	return slices.Contains(targets, osName)
}

func nodeOS(rec *registry.AgentRecord) string {
	if rec == nil || rec.OS == "" {
		return ""
	}
	parts := strings.Split(rec.OS, "/")
	return parts[0]
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	default:
		return 0
	}
}

func isInteger(v any) bool {
	switch x := v.(type) {
	case int, int64:
		return true
	case float64:
		return x == float64(int64(x))
	default:
		return false
	}
}

func guessArch(sys map[string]any, payload map[string]any) any {
	if arch := sys["arch"]; arch != nil {
		return arch
	}
	if cpu, ok := payload["cpu"].(map[string]any); ok {
		return cpu["arch"]
	}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func valueOr(value, fallback any) any {
	if value == nil || value == "" {
		return fallback
	}
	return value
}
