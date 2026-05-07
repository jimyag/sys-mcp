// Package metrics 提供 sysplane-center 的 Prometheus 指标定义。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// AgentsOnline 记录当前在线 agent 数量。
	AgentsOnline = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "sysmcp",
		Subsystem: "center",
		Name:      "agents_online",
		Help:      "当前在线 agent 数量",
	})

	// ToolRequestsTotal 记录工具请求总数，按 tool 名称和状态（success/error/timeout）分类。
	ToolRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "sysmcp",
		Subsystem: "center",
		Name:      "tool_requests_total",
		Help:      "工具请求总数",
	}, []string{"tool", "status"})

	// ToolRequestDuration 记录工具请求耗时（秒）。
	ToolRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "sysmcp",
		Subsystem: "center",
		Name:      "tool_request_duration_seconds",
		Help:      "工具请求耗时（秒）",
		Buckets:   prometheus.DefBuckets,
	}, []string{"tool"})
)
