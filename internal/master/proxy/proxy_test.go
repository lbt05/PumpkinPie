package proxy

import (
	"net/http"
	"testing"
)

func TestIsUpgradeRequest(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"plain GET", map[string]string{}, false},
		{
			"websocket upgrade (lowercase)",
			map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"},
			true,
		},
		{
			"websocket upgrade (canonical case)",
			map[string]string{"Connection": "upgrade", "Upgrade": "websocket"},
			true,
		},
		{
			"keep-alive only",
			map[string]string{"Connection": "keep-alive"},
			false,
		},
		{
			"keep-alive + upgrade mixed",
			map[string]string{"Connection": "keep-alive, Upgrade", "Upgrade": "websocket"},
			true,
		},
		{
			"upgrade word not in connection header",
			map[string]string{"Upgrade": "websocket"},
			false, // bare Upgrade without Connection: Upgrade isn't a real upgrade
		},
		{
			"h2c upgrade",
			map[string]string{"Connection": "Upgrade, HTTP2-Settings", "Upgrade": "h2c"},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "/", nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := isUpgradeRequest(r); got != c.want {
				t.Errorf("isUpgradeRequest(%v) = %v, want %v", c.headers, got, c.want)
			}
		})
	}
}
