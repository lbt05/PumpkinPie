package api

import (
	"reflect"
	"regexp"
	"testing"

	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

func TestAutoName(t *testing.T) {
	cases := []struct {
		image  string
		prefix string // expected prefix WITHOUT trailing '-' (suffix is -<6hex>)
	}{
		{"nginx:alpine", "pp-nginx-alpine"},
		{"nginx", "pp-nginx"},
		{"myregistry.io/app:1.0", "pp-app-1.0"}, // registry host stripped, '.' kept
		{"library/redis:7", "pp-redis-7"},
		{"", "pp-container"}, // empty -> fallback "container"
	}
	randRe := regexp.MustCompile(`-[0-9a-f]{6}$`)
	for _, c := range cases {
		got := autoName(c.image)
		if !randRe.MatchString(got) {
			t.Errorf("autoName(%q) = %q, missing -<6hex> suffix", c.image, got)
		}
		prefix := got[:len(got)-7] // strip "-xxxxxx"
		if prefix != c.prefix {
			t.Errorf("autoName(%q) prefix = %q, want %q", c.image, prefix, c.prefix)
		}
	}
}

func TestAutoName_NoCollisions(t *testing.T) {
	// Repeated calls in the same second should all be unique.
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		n := autoName("nginx:alpine")
		if seen[n] {
			t.Fatalf("collision: %q generated twice", n)
		}
		seen[n] = true
	}
}

func TestSanitizeContainerName(t *testing.T) {
	fallbackRe := regexp.MustCompile(`^pp-[0-9a-f]{6}$`)
	cases := []struct {
		in, want      string
		isFallback    bool // true means expect "pp-<rand6>" (empty / all-symbol input)
	}{
		{"My Cool/App #1!", "my-cool-app-1", false},
		{"  /leading-and-trailing/  ", "leading-and-trailing", false},
		// docker requires name to start with [a-zA-Z0-9]; '_' or '.' at
		// the start are technically allowed by docker but we normalize to
		// a leading 'x' to keep things tidy.
		{"___only__under__scores___", "x___only__under__scores___", false},
		{"...dots...", "x...dots...", false}, // '.' is legal, so kept verbatim
		{"", "", true},                        // empty -> "pp-<rand6>"
		{"UPPERCASE", "uppercase", false},
		{"a-b-c", "a-b-c", false},
		{"a@b#c$d", "a-b-c-d", false},
		{"---", "", true}, // all symbols -> "pp-<rand6>"
		{"123start-num", "123start-num", false},
		{"9leading-digit", "9leading-digit", false}, // digits at start are allowed
	}
	for _, c := range cases {
		got := sanitizeContainerName(c.in)
		if c.isFallback {
			if !fallbackRe.MatchString(got) {
				t.Errorf("sanitizeContainerName(%q) = %q, want pp-<rand6>", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("sanitizeContainerName(%q) = %q, want %q", c.in, got, c.want)
		}
		// docker name constraints: [a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}
		if len(got) > 64 {
			t.Errorf("sanitizeContainerName(%q) length %d > 64", c.in, len(got))
		}
		if first := got[0]; !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
			t.Errorf("sanitizeContainerName(%q) first char %q is not [a-z0-9]", c.in, first)
		}
		for _, r := range got {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				t.Errorf("sanitizeContainerName(%q) contains illegal char %q in %q", c.in, r, got)
			}
		}
	}
}

func TestSanitizeContainerName_Truncates(t *testing.T) {
	long := "a"
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := sanitizeContainerName(long)
	if len(got) > 64 {
		t.Errorf("len=%d, want <= 64: %q", len(got), got)
	}
	if !regexp.MustCompile(`-[0-9a-f]{6}$`).MatchString(got) {
		t.Errorf("truncated name should end in -<6hex>: %q", got)
	}
}

func TestPortMappingsToProto_DefaultsHostPortToContainerPort(t *testing.T) {
	// Mirrors `docker run -p X:X` shorthand: when the user doesn't pin
	// a host port, the agent should bind the same number as the
	// container port. The API enforces this default server-side so the
	// frontend doesn't have to send the field at all.
	in := []portMappingJSON{
		{ContainerPort: 8888, Protocol: "tcp", HostPort: 0},
		{ContainerPort: 53, Protocol: "udp", HostPort: 5353}, // user pinned a different one
		{ContainerPort: 9000, Protocol: "", HostPort: 9001},   // empty protocol -> tcp
	}
	got := portMappingsToProto(in)
	want := []*pb.PortMapping{
		{ContainerPort: 8888, Protocol: "tcp", HostPort: 8888},
		{ContainerPort: 53, Protocol: "udp", HostPort: 5353},
		{ContainerPort: 9000, Protocol: "tcp", HostPort: 9001},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("portMappingsToProto = %+v, want %+v", got, want)
	}
}
