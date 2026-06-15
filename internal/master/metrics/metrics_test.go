package metrics

import (
	"context"
	"testing"
)

func TestNoopSink(t *testing.T) {
	var s Sink = NoopSink{}
	if err := s.Write(context.Background(), Metric{}); err != nil {
		t.Fatalf("noop Write should not error, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("noop Close should not error, got %v", err)
	}
}

func TestNewSinkEmptyURLOff(t *testing.T) {
	s, err := NewSink(Config{})
	if err != nil {
		t.Fatalf("NewSink with empty URL should be a no-op, got %v", err)
	}
	if _, ok := s.(NoopSink); !ok {
		t.Fatalf("expected NoopSink, got %T", s)
	}
}

func TestDecodeDisks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"invalid", "not-json", 0},
		{
			"one",
			`[{"path":"/","total_bytes":100,"used_bytes":40,"usage_percent":40.0}]`,
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeDisks(tt.in)
			if len(got) != tt.want {
				t.Fatalf("got %d disks, want %d", len(got), tt.want)
			}
		})
	}
}

func TestDecodeDisksFields(t *testing.T) {
	in := `[{"path":"/data","total_bytes":1000,"used_bytes":250,"usage_percent":25.5}]`
	got := DecodeDisks(in)
	if len(got) != 1 {
		t.Fatalf("want 1 disk, got %d", len(got))
	}
	if got[0].Path != "/data" || got[0].TotalBytes != 1000 || got[0].UsedBytes != 250 || got[0].UsagePercent != 25.5 {
		t.Fatalf("unexpected disk: %+v", got[0])
	}
}