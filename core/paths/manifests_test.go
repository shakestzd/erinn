package paths

import "testing"

func TestIsDependencyManifest(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"go.mod", true},
		{"go.sum", true},
		{"package.json", true},
		{"web/package.json", true},
		{"requirements.txt", true},
		{"pyproject.toml", true},
		{"Cargo.toml", true},
		{"Gemfile", true},
		{"/abs/repo/go.mod", true},
		{"main.go", false},
		{"README.md", false},
		{"gomod", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsDependencyManifest(tc.path); got != tc.want {
			t.Errorf("IsDependencyManifest(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
