package router_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jimyag/sysplane/api/tunnel"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
	"github.com/jimyag/sysplane/internal/sysplane-center/router"
)

// fakeStream 实现 pkgstream.TunnelStream 接口用于测试。
type fakeStream struct {
	mu       sync.Mutex
	received []*tunnel.TunnelMessage
	reply    func(msg *tunnel.TunnelMessage) // 收到消息后的回调
}

func (f *fakeStream) Send(msg *tunnel.TunnelMessage) error {
	f.mu.Lock()
	f.received = append(f.received, msg)
	cb := f.reply
	f.mu.Unlock()
	if cb != nil {
		cb(msg)
	}
	return nil
}

func (f *fakeStream) Recv() (*tunnel.TunnelMessage, error) {
	return nil, nil
}

func (f *fakeStream) Context() context.Context {
	return context.Background()
}

func (f *fakeStream) ID() string {
	return "fake-stream"
}

func (f *fakeStream) RemoteAddr() string {
	return "127.0.0.1:0"
}

func TestSendMulti_AllSuccess(t *testing.T) {
	rtr := router.New(5)

	makeRecord := func(hostname string, fs *fakeStream) *registry.AgentRecord {
		return &registry.AgentRecord{
			Hostname:    hostname,
			Status:      registry.StatusOnline,
			RouteStream: fs,
		}
	}

	var records []*registry.AgentRecord
	for i, name := range []string{"host-a", "host-b", "host-c"} {
		idx := i
		hostname := name
		fs := &fakeStream{}
		fs.reply = func(msg *tunnel.TunnelMessage) {
			// 模拟 agent 立刻响应
			req := msg.GetToolRequest()
			if req == nil {
				return
			}
			rtr.Deliver(req.RequestId, &tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_ToolResponse{
					ToolResponse: &tunnel.ToolResponse{
						RequestId:  req.RequestId,
						ResultJson: fmt.Sprintf(`{"host":"%s","idx":%d}`, hostname, idx),
					},
				},
			})
		}
		records = append(records, makeRecord(hostname, fs))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	results := rtr.SendMulti(ctx, records, "req-base", "get_hardware_info", "{}")
	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("host %s 出错: %v", r.Hostname, r.Err)
		}
		if r.Result == "" {
			t.Errorf("host %s 结果为空", r.Hostname)
		}
	}
}

// TestSend_TimeoutSendsCancelRequest 验证超时时 router 向 agent stream 发送 CancelRequest。
func TestSend_TimeoutSendsCancelRequest(t *testing.T) {
	rtr := router.New(1) // 1 秒超时

	fs := &fakeStream{}
	rec := &registry.AgentRecord{
		Hostname:    "slow-host",
		Status:      registry.StatusOnline,
		RouteStream: fs,
	}

	// 不回复任何消息，让请求超时
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := rtr.Send(ctx, rec, "req-cancel-test", "get_cpu_info", `{"target_host":"slow-host"}`)
	if err == nil {
		t.Fatal("期望超时错误，但没有报错")
	}

	// 等待一小段时间让 CancelRequest 被发送
	time.Sleep(50 * time.Millisecond)

	fs.mu.Lock()
	msgs := make([]*tunnel.TunnelMessage, len(fs.received))
	copy(msgs, fs.received)
	fs.mu.Unlock()

	// 应该收到两条消息：ToolRequest（第一条）和 CancelRequest（超时后）
	var hasCancelRequest bool
	for _, m := range msgs {
		if c := m.GetCancelRequest(); c != nil && c.RequestId == "req-cancel-test" {
			hasCancelRequest = true
		}
	}
	if !hasCancelRequest {
		t.Errorf("超时后应发送 CancelRequest，实际收到 %d 条消息: %v", len(msgs), msgs)
	}
}
