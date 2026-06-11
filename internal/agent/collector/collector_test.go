package collector

import "testing"

func TestIsIgnoredMount(t *testing.T) {
	cases := []struct {
		mp   string
		want bool
	}{
		{"/", false},
		{"/data", false},
		{"/var/lib", false},
		{"/var/lib/docker", true},
		{"/var/lib/docker/overlay2/abc", true},
		{"/var/lib/kubelet", true},
		{"/var/lib/kubelet/pods/xyz/volumes/kubernetes.io~projected/kube-api-access", true},
		{"/boot", true},
		{"/boot/efi", true},
		{"/run", true},
		{"/run/user/1000", true},
		{"/runtime", false},
		{"/booted", false},
		{"/var/lib/dockerfoo", false},
	}
	for _, tc := range cases {
		if got := isIgnoredMount(tc.mp); got != tc.want {
			t.Errorf("isIgnoredMount(%q) = %v, want %v", tc.mp, got, tc.want)
		}
	}
}
