package collector_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jimyag/sysplane/internal/sysplane-agent/collector"
)

func TestGetHardwareInfo_ReturnsValidJSON(t *testing.T) {
	out, err := collector.GetHardwareInfo(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	var hw collector.HardwareInfo
	if err := json.Unmarshal([]byte(out), &hw); err != nil {
		t.Fatalf("expected valid JSON, got: %v\noutput: %s", err, out)
	}
	if hw.System.Hostname == "" {
		t.Fatal("expected non-empty hostname")
	}
	if hw.CPU.LogicalCores <= 0 {
		t.Fatal("expected at least 1 logical CPU core")
	}
	if hw.Memory.TotalBytes == 0 {
		t.Fatal("expected non-zero total memory")
	}
}
