package collector

import (
	"context"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"

	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type Collector struct {
	nodeName string
}

func New(nodeName string) *Collector {
	return &Collector{nodeName: nodeName}
}

// HostInfo returns static host info for the RegisterRequest.
func HostInfo() (hostname, os, arch, version string, err error) {
	h, err := host.Info()
	if err != nil {
		return "", "", "", "", err
	}
	return h.Hostname, h.OS, h.KernelArch, "0.1.0", nil
}

func (c *Collector) Collect(ctx context.Context) *pb.MetricReport {
	r := &pb.MetricReport{TsUnixMs: time.Now().UnixMilli()}
	r.Cpu = c.cpuStats(ctx)
	r.Memory = c.memStats()
	r.Load = c.loadStats()
	r.Disks = c.diskStats()
	r.Gpu = c.gpuStats()
	return r
}

func (c *Collector) cpuStats(ctx context.Context) *pb.CpuStats {
	out := &pb.CpuStats{}
	// overall percent
	pcts, err := cpu.PercentWithContext(ctx, 0, false)
	if err == nil && len(pcts) > 0 {
		out.UsagePercent = pcts[0]
	}
	// per-core
	perCore, err := cpu.PercentWithContext(ctx, 0, true)
	if err == nil {
		out.PerCorePercent = perCore
	}
	// cores
	n, err := cpu.CountsWithContext(ctx, true)
	if err == nil {
		out.Cores = uint32(n)
	}
	return out
}

func (c *Collector) memStats() *pb.MemoryStats {
	v, err := mem.VirtualMemory()
	out := &pb.MemoryStats{}
	if err != nil {
		return out
	}
	out.TotalBytes = v.Total
	out.UsedBytes = v.Used
	out.UsagePercent = v.UsedPercent
	return out
}

func (c *Collector) loadStats() *pb.LoadAvg {
	avg, err := load.Avg()
	out := &pb.LoadAvg{}
	if err != nil {
		return out
	}
	out.Load1 = avg.Load1
	out.Load5 = avg.Load5
	out.Load15 = avg.Load15
	return out
}

func (c *Collector) diskStats() []*pb.DiskStats {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]*pb.DiskStats, 0, len(parts))
	for _, p := range parts {
		if seen[p.Mountpoint] {
			continue
		}
		seen[p.Mountpoint] = true
		if isIgnoredMount(p.Mountpoint) {
			continue
		}
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil || usage == nil {
			continue
		}
		out = append(out, &pb.DiskStats{
			Path:         p.Mountpoint,
			TotalBytes:   usage.Total,
			UsedBytes:    usage.Used,
			UsagePercent: usage.UsedPercent,
		})
	}
	return out
}

// ignoredMountPrefixes are container/runtime/transient mounts whose disk
// usage just mirrors something we already report (or is noise), so we
// drop them to keep the per-node disk list small and meaningful.
var ignoredMountPrefixes = []string{
	"/var/lib/kubelet",
	"/var/lib/docker",
	"/boot",
	"/run",
}

func isIgnoredMount(mp string) bool {
	for _, p := range ignoredMountPrefixes {
		if mp == p || strings.HasPrefix(mp, p+"/") {
			return true
		}
	}
	return false
}

// gpuStats uses nvidia-smi for simplicity. Returns zeros if no nvidia-smi.
func (c *Collector) gpuStats() *pb.GpuStats {
	out := &pb.GpuStats{}
	cmd := exec.Command("nvidia-smi",
		"--query-gpu=index,name,uuid,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits")
	outBytes, err := cmd.Output()
	if err != nil {
		// no GPUs or nvidia-smi missing
		return out
	}
	lines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
	var totalUsage, n int
	var memUsed, memTotal uint64
	for _, line := range lines {
		fields := strings.Split(line, ", ")
		if len(fields) < 6 {
			continue
		}
		idx, _ := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 32)
		name := strings.TrimSpace(fields[1])
		uuid := strings.TrimSpace(fields[2])
		util, _ := strconv.ParseFloat(strings.TrimSpace(fields[3]), 64)
		mu, _ := strconv.ParseUint(strings.TrimSpace(fields[4]), 10, 64)
		mt, _ := strconv.ParseUint(strings.TrimSpace(fields[5]), 10, 64)
		// nvidia-smi memory is in MiB
		muB := mu * 1024 * 1024
		mtB := mt * 1024 * 1024
		out.Devices = append(out.Devices, &pb.GpuDevice{
			Index:         uint32(idx),
			Name:          name,
			Uuid:          uuid,
			UsagePercent:  util,
			MemUsedBytes:  muB,
			MemTotalBytes: mtB,
		})
		totalUsage += int(util)
		memUsed += muB
		memTotal += mtB
		n++
	}
	out.Count = uint32(n)
	if n > 0 {
		out.UsagePercent = float64(totalUsage) / float64(n)
	}
	out.MemUsedBytes = memUsed
	out.MemTotalBytes = memTotal
	return out
}

// LogSample prints a one-line sample for debug runs.
func (c *Collector) LogSample(r *pb.MetricReport) {
	if r.Cpu != nil {
		log.Printf("cpu=%.1f%% mem=%d/%d gpu=%d", r.Cpu.UsagePercent,
			safeU64(r.Memory.UsedBytes), safeU64(r.Memory.TotalBytes), safeU32(r.Gpu.Count))
	}
}

func safeU64(v uint64) uint64 { return v }
func safeU32(v uint32) uint32 { return v }
