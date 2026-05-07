// Package router routes tool requests from center to agents and delivers responses.
package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jimyag/sysplane/api/tunnel"
	"github.com/jimyag/sysplane/internal/sysplane-center/metrics"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
)

type pendingSlot struct {
	ch chan *tunnel.TunnelMessage
}

// Router forwards tool requests to agents and correlates responses.
type Router struct {
	timeoutSec int
	pending    sync.Map // requestID -> *pendingSlot
}

// New creates a Router with the given timeout.
func New(timeoutSec int) *Router {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	return &Router{timeoutSec: timeoutSec}
}

// Send sends a tool request to the agent described by rec and waits for the response.
// Returns the result JSON string or an error.
func (r *Router) Send(ctx context.Context, rec *registry.AgentRecord, requestID, toolName, argsJSON string) (string, error) {
	start := time.Now()
	status := "success"
	defer func() {
		metrics.ToolRequestsTotal.WithLabelValues(toolName, status).Inc()
		metrics.ToolRequestDuration.WithLabelValues(toolName).Observe(time.Since(start).Seconds())
	}()

	slot := &pendingSlot{ch: make(chan *tunnel.TunnelMessage, 1)}
	r.pending.Store(requestID, slot)
	defer r.pending.Delete(requestID)

	if err := rec.RouteStream.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_ToolRequest{
			ToolRequest: &tunnel.ToolRequest{
				RequestId:  requestID,
				ToolName:   toolName,
				ArgsJson:   argsJSON,
				TargetHost: rec.Hostname,
			},
		},
	}); err != nil {
		status = "error"
		return "", fmt.Errorf("router: send to agent %s: %w", rec.Hostname, err)
	}

	timeout := time.Duration(r.timeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		status = "timeout"
		// 通知 agent 取消正在执行的请求，避免 agent goroutine 持续消耗资源。
		_ = rec.RouteStream.Send(&tunnel.TunnelMessage{
			Payload: &tunnel.TunnelMessage_CancelRequest{
				CancelRequest: &tunnel.CancelRequest{RequestId: requestID},
			},
		})
		if errors.Is(ctx.Err(), context.Canceled) {
			status = "canceled"
			return "", fmt.Errorf("router: canceled waiting for response from %s (request %s)", rec.Hostname, requestID)
		}
		return "", fmt.Errorf("router: timeout waiting for response from %s (request %s)", rec.Hostname, requestID)
	case msg := <-slot.ch:
		switch p := msg.Payload.(type) {
		case *tunnel.TunnelMessage_ToolResponse:
			return p.ToolResponse.ResultJson, nil
		case *tunnel.TunnelMessage_ErrorResponse:
			status = "error"
			return "", fmt.Errorf("agent error [%s]: %s", p.ErrorResponse.Code, p.ErrorResponse.Message)
		default:
			status = "error"
			return "", fmt.Errorf("router: unexpected response type %T", msg.Payload)
		}
	}
}

// MultiResult 保存单台 agent 的工具执行结果。
type MultiResult struct {
	Hostname string
	Result   string
	Err      error
}

// SendMulti 并发向多台 agent 发送同一工具请求，聚合结果。
// 每台 agent 使用独立的 requestID（requestIDBase + "_" + hostname）。
func (r *Router) SendMulti(ctx context.Context, records []*registry.AgentRecord, requestIDBase, toolName, argsJSON string) []MultiResult {
	results := make([]MultiResult, len(records))
	var wg sync.WaitGroup
	for i, rec := range records {
		wg.Add(1)
		go func(idx int, rec *registry.AgentRecord) {
			defer wg.Done()
			reqID := requestIDBase + "_" + rec.Hostname
			res, err := r.Send(ctx, rec, reqID, toolName, argsJSON)
			results[idx] = MultiResult{Hostname: rec.Hostname, Result: res, Err: err}
		}(i, rec)
	}
	wg.Wait()
	return results
}

// Deliver delivers a response to a waiting Send call.
// Safe to call even if the pending slot has already expired.
func (r *Router) Deliver(requestID string, msg *tunnel.TunnelMessage) {
	val, ok := r.pending.Load(requestID)
	if !ok {
		return
	}
	slot := val.(*pendingSlot)
	select {
	case slot.ch <- msg:
	default:
		// Already delivered or expired.
	}
}

// DeliverStream is a helper that wraps a TunnelStream's messages into Deliver calls.
// It is used by TunnelService to route all incoming responses.
func (r *Router) DeliverFromMessage(msg *tunnel.TunnelMessage) {
	var requestID string
	switch p := msg.Payload.(type) {
	case *tunnel.TunnelMessage_ToolResponse:
		requestID = p.ToolResponse.RequestId
	case *tunnel.TunnelMessage_ErrorResponse:
		requestID = p.ErrorResponse.RequestId
	default:
		return
	}
	r.Deliver(requestID, msg)
}
