package git

import "testing"

func TestPorcelainPathSubmoduleLine(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{" M vendor/lib", "vendor/lib"},
		{"M vendor/lib", "vendor/lib"},
	}
	for _, tc := range tests {
		got := PorcelainPath(tc.line)
		if got != tc.want {
			t.Fatalf("PorcelainPath(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}
