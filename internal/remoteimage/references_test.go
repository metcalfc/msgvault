package remoteimage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The reader requests URLs as the WHATWG URL parser serializes them, which
// is how a browser writes new URL(src).toString(); the stored HTML keeps
// whatever the sender wrote.
func TestReferencedMatchesTheBrowsersSerialization(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		stored  string
		request string
		want    bool
	}{
		{"dot segments", `<img src="https://images.example/a/../photo.png">`, "https://images.example/photo.png", true},
		{"single dot segments", `<img src="https://images.example/./a/./b.png">`, "https://images.example/a/b.png", true},
		{"dot segments above the root", `<img src="https://images.example/../../x.png">`, "https://images.example/x.png", true},
		{"encoded dot segments", `<img src="https://images.example/a/%2e%2e/photo.png">`, "https://images.example/photo.png", true},
		{"trailing dot keeps the slash", `<img src="https://images.example/a/b/.">`, "https://images.example/a/b/", true},
		{"case and default port", `<img src="HTTPS://Images.EXAMPLE:443/p.png">`, "https://images.example/p.png", true},
		{"empty path", `<img src="http://images.example">`, "http://images.example/", true},
		{"protocol-relative", `<img src="//images.example/p.png">`, "https://images.example/p.png", true},
		{"entity-escaped query", `<img src="https://images.example/p.png?a=1&amp;b=2">`, "https://images.example/p.png?a=1&b=2", true},
		{"percent-encoded space", `<img src="https://images.example/my photo.png">`, "https://images.example/my%20photo.png", true},
		{"percent-encoded query", `<img src="https://images.example/p.png?q=a b">`, "https://images.example/p.png?q=a%20b", true},
		{"fragment ignored", `<img src="https://images.example/p.png#top">`, "https://images.example/p.png", true},
		{"non-default port kept", `<img src="https://images.example:8443/p.png">`, "https://images.example/p.png", false},
		{"different path", `<img src="https://images.example/a/photo.png">`, "https://images.example/photo.png", false},
		{"different query", `<img src="https://images.example/p.png?a=1">`, "https://images.example/p.png?a=2", false},
		{"link, not image", `<a href="https://images.example/p.png">x</a>`, "https://images.example/p.png", false},
		{"encoded slash is not a separator", `<img src="https://images.example/a%2F..%2Fsecret.png">`, "https://images.example/secret.png", false},
		{"encoded slash matches itself", `<img src="https://images.example/a%2f..%2fsecret.png">`, "https://images.example/a%2F..%2Fsecret.png", true},
		{"encoded backslash is not a separator", `<img src="https://images.example/a%5C..%5Csecret.png">`, "https://images.example/secret.png", false},
		{"encoded question mark stays in the path", `<img src="https://images.example/a%3Fb.png">`, "https://images.example/a?b.png", false},
		{"encoded hash stays in the path", `<img src="https://images.example/a%23b.png">`, "https://images.example/a%23b.png", true},
		{"encoded unreserved letters decode", `<img src="https://images.example/%70hoto.png">`, "https://images.example/photo.png", true},
		{"encoded ampersand in the query is not a separator", `<img src="https://images.example/p.png?a=1%26b=2">`, "https://images.example/p.png?a=1&b=2", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Referenced(tt.request, tt.stored))
		})
	}
}

func TestResolveDotSegments(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"":             "/",
		"/":            "/",
		"/a/b/../c":    "/a/c",
		"/a/..":        "/",
		"/../a":        "/a",
		"/a/./b/./":    "/a/b/",
		"/a//b":        "/a//b",
		"/a/b/../../c": "/c",
	} {
		assert.Equal(t, want, resolveDotSegments(input), input)
	}
}
