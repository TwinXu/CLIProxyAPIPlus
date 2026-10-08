package managementasset

import "testing"

func TestResolveReleaseURL(t *testing.T) {
	pinned := "https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/tags/v1.24.2"

	tests := []struct {
		name string
		repo string
		want string
	}{
		{name: "empty uses pinned upstream", repo: "", want: pinned},
		{name: "invalid uses pinned upstream", repo: "not a url", want: pinned},
		{name: "upstream repo url", repo: "https://github.com/router-for-me/Cli-Proxy-API-Management-Center", want: pinned},
		{name: "upstream repo url with .git and trailing slash", repo: "https://github.com/router-for-me/cli-proxy-api-management-center.git/", want: pinned},
		{name: "upstream api url", repo: "https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center", want: pinned},
		{name: "upstream api latest url", repo: "https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest", want: pinned},
		{name: "custom repo url follows latest", repo: "https://github.com/acme/panel", want: "https://api.github.com/repos/acme/panel/releases/latest"},
		{name: "custom api url follows latest", repo: "https://api.github.com/repos/acme/panel", want: "https://api.github.com/repos/acme/panel/releases/latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveReleaseURL(tt.repo); got != tt.want {
				t.Fatalf("resolveReleaseURL(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}
