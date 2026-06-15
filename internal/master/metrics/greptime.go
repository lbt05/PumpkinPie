package metrics

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	greptime "github.com/GreptimeTeam/greptimedb-ingester-go"
	"github.com/GreptimeTeam/greptimedb-ingester-go/table"
	gtypes "github.com/GreptimeTeam/greptimedb-ingester-go/table/types"
)

// Default destination table when the user leaves Config.Table empty.
const defaultTable = "node_metrics"

// greptimeSink writes each Metric into a single GreptimeDB table
// ("node_metrics" by default). The schema is fixed and created on
// first insert via GreptimeDB's auto-schema-on-insert behavior, so no
// bootstrap migration is required.
//
// Connection model: a single gRPC client per sink, shared across
// every Write call. The SDK is documented as safe for concurrent use;
// we serialize Write calls anyway because the Table object we build
// per-row is not (it's appended to in AddRow).
type greptimeSink struct {
	client *greptime.Client
	cfg    Config

	// writeMu serializes Table construction. Cheaper than per-call
	// allocations of a fresh *table.Table because AddRow mutates
	// the table in place.
	writeMu sync.Mutex
}

// newGreptimeSink builds a sink over an Ingester gRPC client. The
// client is constructed lazily — a failed dial is logged and the
// caller still gets a working sink that returns errors from Write
// until either the client recovers or the process restarts.
//
// Per the design agreement: "fail soft". We never return an error
// from NewSink that would prevent the master from starting; instead
// we log a warning and return a sink that records its first Write
// failure so the operator sees the issue in the journal.
func newGreptimeSink(cfg Config) (Sink, error) {
	db := cfg.Database
	if db == "" {
		db = "public"
	}
	tbl := cfg.Table
	if tbl == "" {
		tbl = defaultTable
	}
	cfg.Database = db
	cfg.Table = tbl

	// Normalize the URL — the ingester SDK's Config.build() does
	// not accept "http://" or "https://" prefixes, only "host:port".
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.URL, "http://"), "https://")

	gc := greptime.NewConfig(endpoint).
		WithDatabase(cfg.Database).
		WithAuth(cfg.Username, cfg.Password).
		WithInsecure(true)

	client, err := greptime.NewClient(gc)
	if err != nil {
		// Don't fail the master on a bad config — log and degrade
		// to a sink that returns the error from every Write.
		log.Printf("[metrics] greptimedb client init: %v (sink will return errors until restart)", err)
		return &degradedSink{err: err}, nil
	}
	log.Printf("[metrics] greptimedb sink enabled: endpoint=%s db=%s table=%s", endpoint, cfg.Database, cfg.Table)
	return &greptimeSink{client: client, cfg: cfg}, nil
}

// buildTable returns a fresh table object with the schema for one
// MetricReport. Schema definition is cheap (a slice of ColumnSchema
// entries), so we recreate it per write rather than caching — that
// way we never accidentally append a stale row to a table built for
// a different schema version.
func buildTable(name string) (*table.Table, error) {
	tbl, err := table.New(name)
	if err != nil {
		return nil, err
	}
	// Tags — identifying fields, indexed by GreptimeDB.
	if err := tbl.AddTagColumn("node_id", gtypes.STRING); err != nil {
		return nil, err
	}
	if err := tbl.AddTagColumn("node_name", gtypes.STRING); err != nil {
		return nil, err
	}
	// Scalar fields.
	if err := tbl.AddFieldColumn("cpu_percent", gtypes.FLOAT64); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("cpu_cores", gtypes.INT32); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("mem_used_bytes", gtypes.INT64); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("mem_total_bytes", gtypes.INT64); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("load1", gtypes.FLOAT64); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("gpu_count", gtypes.INT32); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("gpu_usage_percent", gtypes.FLOAT64); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("gpu_mem_used_bytes", gtypes.INT64); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("gpu_mem_total_bytes", gtypes.INT64); err != nil {
		return nil, err
	}
	// Variable-length lists — stored as JSON for one-row-per-report
	// ergonomics. The string values are still valid in column-family
	// scans; users that want normalized per-disk / per-GPU rows can
	// write a SQL view over the JSON column.
	if err := tbl.AddFieldColumn("disks", gtypes.JSON); err != nil {
		return nil, err
	}
	if err := tbl.AddFieldColumn("gpus", gtypes.JSON); err != nil {
		return nil, err
	}
	// Timestamp — single column per table, millisecond precision
	// matches MetricReport.TsUnixMs on the wire.
	if err := tbl.AddTimestampColumn("ts", gtypes.TIMESTAMP_MILLISECOND); err != nil {
		return nil, err
	}
	return tbl, nil
}

func (s *greptimeSink) Write(ctx context.Context, m Metric) error {
	if m.NodeID == "" {
		// Without an ID we have no tag value and the row would be
		// useless on the GreptimeDB side. Skip silently — the SQLite
		// path still recorded the snapshot so the UI isn't affected.
		return nil
	}
	ts := m.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tbl, err := buildTable(s.cfg.Table)
	if err != nil {
		return err
	}
	// disks / gpus: the JSON column type accepts the slice directly
	// (BuildJSON marshals it). Pass nil for "no devices" so we write
	// an explicit JSON null rather than "[]", which is friendlier in
	// Grafana queries.
	var disksVal, gpusVal any
	if len(m.Disks) > 0 {
		disksVal = m.Disks
	}
	if len(m.GpuDevices) > 0 {
		gpusVal = m.GpuDevices
	}
	if err := tbl.AddRow(
		m.NodeID,
		m.NodeName,
		m.CPUPercent,
		int32(m.CPUCores),
		int64(m.MemUsedBytes),
		int64(m.MemTotalBytes),
		m.Load1,
		int32(m.GpuCount),
		m.GpuUsageAvg,
		int64(m.GpuMemUsed),
		int64(m.GpuMemTotal),
		disksVal,
		gpusVal,
		ts,
	); err != nil {
		return err
	}
	if _, err := s.client.Write(ctx, tbl); err != nil {
		return err
	}
	return nil
}

func (s *greptimeSink) Close() error {
	if s.client == nil {
		return nil
	}
	return s.client.Close()
}

// degradedSink is the fallback returned by newGreptimeSink when the
// gRPC client fails to construct. It satisfies Sink so the rest of
// the master never has to nil-check, and surfaces the original error
// from every Write so the operator notices in the journal.
type degradedSink struct {
	err error
}

func (d *degradedSink) Write(_ context.Context, _ Metric) error { return d.err }
func (d *degradedSink) Close() error                             { return nil }

// IsDegraded reports whether the given sink is a degradedSink. Useful
// for tests and for emitting a single warning at startup without
// having to type-assert the field directly.
func IsDegraded(s Sink) bool {
	_, ok := s.(*degradedSink)
	return ok
}