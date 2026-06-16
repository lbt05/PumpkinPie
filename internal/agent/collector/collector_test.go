package collector

import (
	"reflect"
	"testing"

	"github.com/shirou/gopsutil/v3/disk"
)

func TestIsIgnoredMount(t *testing.T) {
	cases := []struct {
		mp   string
		want bool
	}{
		// keep real disks
		{"/", false},
		{"/data", false},
		{"/var/lib", false},

		// container / runtime state
		{"/var/lib/docker", true},
		{"/var/lib/docker/overlay2/abc", true},
		{"/var/lib/kubelet", true},
		{"/var/lib/kubelet/pods/xyz/volumes/kubernetes.io~projected/kube-api-access", true},
		{"/var/lib/containerd", true},
		{"/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/123", true},
		{"/var/lib/containers/storage/overlay/x", true},
		{"/var/lib/rancher/k3s/agent", true},

		// snap
		{"/snap", true},
		{"/snap/core/1234", true},
		{"/snap/lxd/current", true},
		{"/var/snap/microk8s/common", true},
		{"/var/lib/snapd/snaps/core_xxx.snap", true},

		// boot / kernel pseudo-FS
		{"/boot", true},
		{"/boot/efi", true},
		{"/run", true},
		{"/run/user/1000", true},
		{"/proc", true},
		{"/sys/fs/cgroup", true},
		{"/dev/shm", true},

		// macOS APFS sub-volumes — all share the same container as /
		// so the canonical "/" entry stands in for the whole disk.
		{"/System/Volumes/Preboot", true},
		{"/System/Volumes/VM/swapfile0", true},
		{"/System/Volumes/Data", true}, // firmlink to / (same container)
		{"/System/Volumes/Update/mnt1", true},
		{"/System/Volumes/Update/SFR/mnt1", true}, // sealed firmware partition (macOS 14+)

		// Nix package store
		{"/nix", true},
		{"/nix/store", true},
		{"/nix/store/abc123-foo", true},

		// false-positive guards (prefix without trailing slash must not match)
		{"/runtime", false},
		{"/booted", false},
		{"/var/lib/dockerfoo", false},
		{"/var/lib/containerdfoo", false},
		{"/var/lib/containersfoo", false},
		{"/snapshot", false},
		{"/devops", false},
		{"/system", false},
	}
	for _, tc := range cases {
		if got := isIgnoredMount(tc.mp); got != tc.want {
			t.Errorf("isIgnoredMount(%q) = %v, want %v", tc.mp, got, tc.want)
		}
	}
}

func TestIsIgnoredFstype(t *testing.T) {
	cases := []struct {
		fs   string
		want bool
	}{
		// real on-disk FS — keep
		{"ext4", false},
		{"xfs", false},
		{"btrfs", false},
		{"zfs", false},
		{"apfs", false},
		{"vfat", false},
		{"ntfs", false},
		{"", false},

		// noise
		{"squashfs", true},
		{"SQUASHFS", true}, // case-insensitive
		{"iso9660", true},
		{"udf", true},
		{"overlay", true},
		{"overlay2", true},
		{"aufs", true},
		{"fuse.snapfuse", true},
		{"fuse.portal", true},
		{"fuse.gvfsd-fuse", true},

		// network FS — filtered by default (see ignoredFstypes comment)
		{"nfs", true},
		{"nfs4", true},
		{"cifs", true},
		{"smbfs", true},
		{"9p", true},
	}
	for _, tc := range cases {
		if got := isIgnoredFstype(tc.fs); got != tc.want {
			t.Errorf("isIgnoredFstype(%q) = %v, want %v", tc.fs, got, tc.want)
		}
	}
}

func TestFilterPartitions_LinuxBox(t *testing.T) {
	parts := []disk.PartitionStat{
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/mnt/bind", Fstype: "ext4"}, // bind dup of root
		{Device: "/dev/sda2", Mountpoint: "/data", Fstype: "ext4"},
		{Device: "/dev/loop0", Mountpoint: "/snap/core/1234", Fstype: "squashfs"},                           // path + fstype
		{Device: "/dev/loop1", Mountpoint: "/var/lib/docker/overlay2/abc/merged", Fstype: "overlay"},        // path + fstype
		{Device: "tmpfs", Mountpoint: "/dev/shm", Fstype: "tmpfs"},                                          // path noise
		{Device: "nas:/share", Mountpoint: "/mnt/nas", Fstype: "nfs4"},                                      // network FS
		{Device: "/dev/nvme0n1p1", Mountpoint: "/var/lib/kubelet/pods/abc/volumes/x", Fstype: "ext4"},       // path noise on a real disk
	}
	got := filterPartitions(parts)
	var mps []string
	for _, p := range got {
		mps = append(mps, p.Mountpoint)
	}
	// sda1 bind dup → "/" wins (shorter than "/mnt/bind")
	// sda2 → "/data"
	// loop0, loop1, tmpfs, nas, nvme0n1p1 (kubelet path) all dropped
	want := []string{"/", "/data"}
	if !reflect.DeepEqual(mps, want) {
		t.Errorf("filterPartitions mountpoints = %v, want %v", mps, want)
	}
}

func TestFilterPartitions_APFSFirmlinks(t *testing.T) {
	// macOS shows the same APFS data volume under several firmlinked
	// mountpoints. They all share the same device and must collapse.
	parts := []disk.PartitionStat{
		{Device: "/dev/disk1s1", Mountpoint: "/", Fstype: "apfs"},
		{Device: "/dev/disk1s1", Mountpoint: "/System/Volumes/Data", Fstype: "apfs"},
		{Device: "/dev/disk1s1", Mountpoint: "/private/var", Fstype: "apfs"},
		{Device: "/dev/disk1s2", Mountpoint: "/System/Volumes/Preboot", Fstype: "apfs"}, // dropped by prefix
	}
	got := filterPartitions(parts)
	if len(got) != 1 {
		t.Fatalf("expected 1 partition (firmlinks deduped, preboot dropped), got %d: %+v", len(got), got)
	}
	if got[0].Mountpoint != "/" {
		t.Errorf("expected canonical mountpoint '/', got %q", got[0].Mountpoint)
	}
}

func TestFilterPartitions_EmptyDeviceNotMerged(t *testing.T) {
	// Two distinct mounts with empty device must NOT be deduped together.
	parts := []disk.PartitionStat{
		{Device: "", Mountpoint: "/data/a", Fstype: "fuse"},
		{Device: "", Mountpoint: "/data/b", Fstype: "fuse"},
	}
	got := filterPartitions(parts)
	if len(got) != 2 {
		t.Fatalf("expected 2 partitions (distinct empty-device mounts), got %d", len(got))
	}
}

func TestFilterPartitions_StableSortedOutput(t *testing.T) {
	parts := []disk.PartitionStat{
		{Device: "/dev/sdc1", Mountpoint: "/zzz", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/sdb1", Mountpoint: "/data", Fstype: "ext4"},
	}
	got := filterPartitions(parts)
	var mps []string
	for _, p := range got {
		mps = append(mps, p.Mountpoint)
	}
	want := []string{"/", "/data", "/zzz"}
	if !reflect.DeepEqual(mps, want) {
		t.Errorf("filterPartitions = %v, want sorted %v", mps, want)
	}
}
