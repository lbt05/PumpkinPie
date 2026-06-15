package metrics

import "context"

// NoopSink discards every metric. Returned by NewSink when cfg.URL is
// empty so the rest of the master code can call sink.Write without
// caring whether a backend is configured.
type NoopSink struct{}

func (NoopSink) Write(_ context.Context, _ Metric) error { return nil }
func (NoopSink) Close() error                             { return nil }