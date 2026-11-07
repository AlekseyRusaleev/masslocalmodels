package worker

import "testing"

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		remote string
		local  string
		want   bool
	}{
		{"0.2.1", "0.2.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.1.9", "0.2.0", false},
		{"1.0.0", "0.9.9", true},
		{"v0.3.0", "0.2.0", true},
		{"", "0.2.0", false},
		{"beta", "0.2.0", false},
	}
	for _, tc := range cases {
		if got := NewerVersion(tc.remote, tc.local); got != tc.want {
			t.Fatalf("NewerVersion(%q, %q) = %v, want %v", tc.remote, tc.local, got, tc.want)
		}
	}
}

func TestReleaseURLAllowed(t *testing.T) {
	control := "https://api.nshno.tech"
	if !releaseURLAllowed(control, "https://api.nshno.tech/node/compute-node.exe") {
		t.Fatal("свой адрес отклонён")
	}
	for _, raw := range []string{
		"http://api.nshno.tech/node/compute-node.exe",
		"https://evil.example/compute-node.exe",
		"https://user:pass@api.nshno.tech/node/compute-node.exe",
		"https://api.nshno.tech.evil/node/compute-node.exe",
	} {
		if releaseURLAllowed(control, raw) {
			t.Fatalf("принят чужой адрес %s", raw)
		}
	}
}
