// Package metrics forwards per-node metrics reports to a downstream
// time-series store. The default NoopSink lets the master run unchanged
// when no sink is configured; the GreptimeDB sink writes each report
// to a GreptimeDB table for long-term analytics.
//
// Wiring:
//
//   sink, err := metrics.NewSink(cfg)
//   defer sink.Close()
//   mgr := agentmgr.NewManager(st, sink)
//
// The sink never blocks the master: Write returns an error but does not
// retry or backoff internally — the master's own SQLite snapshot
// (UpdateNodeMetrics) remains authoritative for the UI and is
// unaffected by sink failures.
package metrics

import (
	"context"
	"time"
)

// Config selects the sink implementation. An empty URL means
// "no sink" — NewSink returns a NoopSink and the master behaves
// exactly as before the metrics feature was added.
type Config struct {
	// URL is the GreptimeDB gRPC endpoint, e.g. "127.0.0.1:4001".
	// "http://" / "https://" prefixes are stripped automatically.
	// Empty disables the sink.
	URL string
	// Database is the GreptimeDB logical database to write into.
	// Defaults to "public" when empty.
	Database string
	// Username / Password are optional Basic Auth credentials
	// (GreptimeCloud sets them, standalone does not).
	Username string
	Password string
	// Table overrides the destination table name. Defaults to
	// "node_metrics" when empty.
	Table string
}

// Metric is the per-report snapshot sent to a Sink. It is a flat
// projection of pb.MetricReport + the node's identifying fields so
// sinks don't have to know about protobuf or gRPC.
type Metric struct {
	NodeID   string
	NodeName string

	CPUPercent float64
	CPUCores   uint32

	MemUsedBytes  uint64
	MemTotalBytes uint64

	Load1 float64

	GpuCount    uint32
	GpuUsageAvg float64
	GpuMemUsed  uint64
	GpuMemTotal uint64
	GpuDevices  []GPUSample

	Disks []DiskSample

	TS time.Time
}

// GPUSample is one row per GPU device; the aggregate fields on Metric
// are pre-computed by the agent.
type GPUSample struct {
	Index         uint32
	Name          string
	UUID          string
	UsagePercent  float64
	MemUsedBytes  uint64
	MemTotalBytes uint64
}

// DiskSample is one row per mounted filesystem.
type DiskSample struct {
	Path         string
	TotalBytes   uint64
	UsedBytes    uint64
	UsagePercent float64
}

// Sink is the contract every backend implements. Write must be
// non-blocking and return quickly; failures are returned to the caller
// but never panic. Close releases any underlying resources.
type Sink interface {
	Write(ctx context.Context, m Metric) error
	Close() error
}

// NewSink constructs the sink named by cfg. An empty cfg.URL yields a
// NoopSink and a nil error — callers can then ignore the sink without
// having to special-case "is this configured?".
func NewSink(cfg Config) (Sink, error) {
	if cfg.URL == "" {
		return NoopSink{}, nil
	}
	return newGreptimeSink(cfg)
}