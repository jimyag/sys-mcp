package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jimyag/sysplane/internal/sysplane-center/metrics"
)

func TestMetricsExist(t *testing.T) {
	metrics.ToolRequestsTotal.WithLabelValues("list_directory", "success").Inc()
	metrics.AgentsOnline.Set(3)

	count, err := testutil.GatherAndCount(
		prometheus.DefaultGatherer,
		"sysmcp_center_tool_requests_total",
		"sysmcp_center_agents_online",
	)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("期望至少 1 个 metric 样本")
	}
}
