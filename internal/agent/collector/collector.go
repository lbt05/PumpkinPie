package collector

import (
	"context"
	"log"
	"os/exec"
	"sort"
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

// HostInfo returns static host info for the RegisterRequest (hostname,
// OS, kernel arch). The build version is owned by the caller — see
// buildinfo.Version — and is forwarded separately in the Register
// message. Keeping HostInfo version-free avoids dragging the global
// into the collector package.
func HostInfo() (hostname, os, arch string, err error) {
	h, err := host.Info()
	if err != nil {
		return "", "", "", err
	}
	return h.Hostname, h.OS, h.KernelArch, nil
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
	kept := filterPartitions(parts)
	out := make([]*pb.DiskStats, 0, len(kept))
	for _, p := range kept {
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

// filterPartitions drops mounts we never want to report (container
// runtime overlays, snap squashfs, network shares, kernel pseudo-FS,
// etc.) and deduplicates the survivors by underlying device so a
// single physical disk reachable via multiple bind mounts / APFS
// firmlinks reports exactly once. Pulled out as a pure function so
// the platform-specific gopsutil call site stays small and the
// filtering is unit-testable.
func filterPartitions(parts []disk.PartitionStat) []disk.PartitionStat {
	// 1. drop by mountpoint prefix and by fstype.
	keep := make([]disk.PartitionStat, 0, len(parts))
	for _, p := range parts {
		if isIgnoredMount(p.Mountpoint) || isIgnoredFstype(p.Fstype) {
			continue
		}
		keep = append(keep, p)
	}
	// 2. dedup by device, preferring the shortest mountpoint
	//    (canonical mount usually wins over bind targets / firmlinks).
	//    Devices reported as empty string get a per-mount synthetic key
	//    so we don't accidentally collapse unrelated FUSE-style mounts.
	best := make(map[string]disk.PartitionStat, len(keep))
	for _, p := range keep {
		key := p.Device
		if key == "" {
			key = "\x00mp:" + p.Mountpoint
		}
		if cur, ok := best[key]; !ok || len(p.Mountpoint) < len(cur.Mountpoint) {
			best[key] = p
		}
	}
	// 3. stable output by mountpoint so reports / UI rows don't flap.
	out := make([]disk.PartitionStat, 0, len(best))
	for _, p := range best {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

// ignoredMountPrefixes are container/runtime/transient mounts whose disk
// usage just mirrors something we already report (or is noise), so we
// drop them to keep the per-node disk list small and meaningful.
//
// Each entry is matched as a path prefix with a "/" boundary
// (so "/var/lib/docker" also catches "/var/lib/docker/overlay2/abc").
var ignoredMountPrefixes = []string{
	// container / orchestrator state — these live on the same disk as /
	// and just inflate the partition list with one entry per container
	// layer / pod volume.
	"/var/lib/kubelet",
	"/var/lib/docker",
	"/var/lib/containerd",
	"/var/lib/containers",
	"/var/lib/rancher",

	// snap packages and their per-snap data dirs.
	"/snap",
	"/var/snap",
	"/var/lib/snapd",

	// boot / runtime / kernel pseudo-FS (most of these are filtered by
	// disk.Partitions(false), but keep them as defense-in-depth).
	"/boot",
	"/run",
	"/proc",
	"/sys",
	"/dev",

	// macOS APFS sub-volumes that share the sealed-system container
	// with /. On Big Sur+ these all report the same capacity because
	// they live in one APFS container, but gopsutil emits distinct
	// device strings for each (`/dev/disk3s1s1`, `/dev/disk3s5`, …)
	// so our dedup-by-device can't merge them. Drop the firmlinks
	// and let the canonical "/" entry represent the whole container.
	// `/System/Volumes/Update/SFR/mnt1` is the sealed firmware
	// partition on macOS 14+ (small read-only volume, ~5 GB) — kept
	// here so the disk list collapses to just the user-data root.
	"/System/Volumes/Preboot",
	"/System/Volumes/Update",
	"/System/Volumes/Update/SFR/mnt1",
	"/System/Volumes/VM",
	"/System/Volumes/Data",         // writable data layer (same container as /)
	"/System/Volumes/xarts",
	"/System/Volumes/Hardware",
	"/System/Volumes/iSCPreboot",

	// Nix package store. Multi-user Nix installs mount /nix on a
	// dedicated partition on Linux, but the contents are mostly
	// read-only package blobs that mirror what's already on the root
	// disk. macOS users typically keep it on the system APFS
	// container where it would otherwise double-count against /.
	"/nix",
}

func isIgnoredMount(mp string) bool {
	for _, p := range ignoredMountPrefixes {
		if mp == p || strings.HasPrefix(mp, p+"/") {
			return true
		}
	}
	return false
}

// ignoredFstypes covers filesystem types that gopsutil's Partitions(false)
// still returns even though they're never useful to report:
//   - squashfs / iso9660 / udf: read-only image filesystems (snap packages,
//     loop-mounted ISOs). All show 100% used.
//   - overlay / overlay2 / aufs: container layer FS — same data as the
//     underlying disk, double-counted.
//   - fuse.snapfuse / fuse.portal / fuse.gvfsd-fuse: desktop FUSE shims.
//   - nfs / nfs4 / cifs / smbfs / 9p: network shares. disk.Usage() can
//     block on stale mounts, and most operators don't want remote storage
//     conflated with local capacity. Re-enable per-mount via prefix if
//     you really care about a specific share.
var ignoredFstypes = map[string]bool{
	"squashfs":        true,
	"iso9660":         true,
	"udf":             true,
	"overlay":         true,
	"overlay2":        true,
	"aufs":            true,
	"fuse.snapfuse":   true,
	"fuse.portal":     true,
	"fuse.gvfsd-fuse": true,
	"nfs":             true,
	"nfs4":            true,
	"cifs":            true,
	"smbfs":           true,
	"9p":              true,
}

func isIgnoredFstype(fs string) bool {
	return ignoredFstypes[strings.ToLower(fs)]
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
