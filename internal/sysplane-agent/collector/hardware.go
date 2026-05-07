// Package collector provides hardware/OS information collection for the agent.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sync/errgroup"
)

// HardwareInfo is the JSON shape returned by get_hardware_info.
type HardwareInfo struct {
	CPU     CPUInfo    `json:"cpu"`
	Memory  MemInfo    `json:"memory"`
	Disks   []DiskInfo `json:"disks"`
	Network []NetInfo  `json:"network"`
	System  SystemInfo `json:"system"`
}

// CPUInfo contains CPU details.
type CPUInfo struct {
	ModelName     string    `json:"model_name"`
	PhysicalCores int       `json:"physical_cores"`
	LogicalCores  int       `json:"logical_cores"`
	UsagePercent  []float64 `json:"usage_percent"` // per-core
}

// MemInfo contains memory details.
type MemInfo struct {
	TotalBytes     uint64  `json:"total_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

// DiskInfo contains disk partition + usage details.
type DiskInfo struct {
	Device      string  `json:"device"`
	Mountpoint  string  `json:"mountpoint"`
	Fstype      string  `json:"fstype"`
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

// NetInfo contains network interface details.
type NetInfo struct {
	Name         string   `json:"name"`
	HardwareAddr string   `json:"hardware_addr"`
	Addrs        []string `json:"addrs"`
	BytesSent    uint64   `json:"bytes_sent"`
	BytesRecv    uint64   `json:"bytes_recv"`
}

// SystemInfo contains OS/host details.
type SystemInfo struct {
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	KernelVersion   string `json:"kernel_version"`
	UptimeSeconds   uint64 `json:"uptime_seconds"`
	BootTime        int64  `json:"boot_time"`
}

// GetHardwareInfo collects hardware and OS information concurrently.
func GetHardwareInfo(ctx context.Context, _ string) (string, error) {
	var (
		cpuResult CPUInfo
		memInfo   MemInfo
		diskInfos []DiskInfo
		netInfos  []NetInfo
		sysInfo   SystemInfo
	)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		infos, err := cpu.InfoWithContext(gctx)
		if err != nil || len(infos) == 0 {
			return nil
		}
		cpuResult.ModelName = infos[0].ModelName
		physCores, _ := cpu.CountsWithContext(gctx, false)
		logCores, _ := cpu.CountsWithContext(gctx, true)
		cpuResult.PhysicalCores = physCores
		cpuResult.LogicalCores = logCores

		usage, err := cpu.PercentWithContext(gctx, 200*time.Millisecond, true)
		if err == nil {
			cpuResult.UsagePercent = usage
		}
		return nil
	})

	g.Go(func() error {
		v, err := mem.VirtualMemoryWithContext(gctx)
		if err != nil {
			return nil
		}
		memInfo = MemInfo{
			TotalBytes:     v.Total,
			AvailableBytes: v.Available,
			UsedBytes:      v.Used,
			UsedPercent:    v.UsedPercent,
		}
		return nil
	})

	g.Go(func() error {
		parts, err := disk.PartitionsWithContext(gctx, false)
		if err != nil {
			return nil
		}
		for _, p := range parts {
			usage, err := disk.UsageWithContext(gctx, p.Mountpoint)
			if err != nil {
				continue
			}
			diskInfos = append(diskInfos, DiskInfo{
				Device:      p.Device,
				Mountpoint:  p.Mountpoint,
				Fstype:      p.Fstype,
				TotalBytes:  usage.Total,
				FreeBytes:   usage.Free,
				UsedPercent: usage.UsedPercent,
			})
		}
		return nil
	})

	g.Go(func() error {
		ifaces, err := net.InterfacesWithContext(gctx)
		if err != nil {
			return nil
		}
		counters, _ := net.IOCountersWithContext(gctx, true)
		counterMap := make(map[string]net.IOCountersStat)
		for _, c := range counters {
			counterMap[c.Name] = c
		}
		for _, iface := range ifaces {
			addrs := make([]string, 0, len(iface.Addrs))
			for _, a := range iface.Addrs {
				addrs = append(addrs, a.Addr)
			}
			ni := NetInfo{
				Name:         iface.Name,
				HardwareAddr: iface.HardwareAddr,
				Addrs:        addrs,
			}
			if c, ok := counterMap[iface.Name]; ok {
				ni.BytesSent = c.BytesSent
				ni.BytesRecv = c.BytesRecv
			}
			netInfos = append(netInfos, ni)
		}
		return nil
	})

	g.Go(func() error {
		info, err := host.InfoWithContext(gctx)
		if err != nil {
			return nil
		}
		sysInfo = SystemInfo{
			Hostname:        info.Hostname,
			OS:              info.OS,
			Platform:        info.Platform,
			PlatformVersion: info.PlatformVersion,
			KernelVersion:   info.KernelVersion,
			UptimeSeconds:   info.Uptime,
			BootTime:        int64(info.BootTime),
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("collector: %w", err)
	}

	hw := HardwareInfo{
		CPU:     cpuResult,
		Memory:  memInfo,
		Disks:   diskInfos,
		Network: netInfos,
		System:  sysInfo,
	}
	out, _ := json.Marshal(hw)
	return string(out), nil
}
