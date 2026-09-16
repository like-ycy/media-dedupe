package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.0.0", "v1.0.0", 0},
		{"1.2.3", "v1.2.3", 0},
		{"v1.10.0", "v1.9.0", 1},
		{"v1.0.0", "v1.0", 0},
		{"v1.0.0", "v1.0.0-beta", 1},
		{"v1.0.0-beta", "v1.0.0", -1},
		{"v1.0.0-beta", "v1.0.0-alpha", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestGetVersion(t *testing.T) {
	v := GetVersion()
	if v == "" || v[0] != 'v' {
		t.Fatalf("GetVersion() = %q, want non-empty with v prefix", v)
	}
}
