package security

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRelPath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "root", input: "///", want: ""},
		{name: "normalizes separators", input: `\\photos//2026/trip.jpg/`, want: "photos/2026/trip.jpg"},
		{name: "keeps unicode and spaces", input: "/相册/夏 日.jpg", want: "相册/夏 日.jpg"},
		{name: "rejects dot", input: "docs/./readme.md", wantErr: true},
		{name: "rejects traversal", input: "docs/../secret", wantErr: true},
		{name: "rejects blank segment", input: "docs/   /secret", wantErr: true},
		{name: "rejects nul", input: "docs/\x00secret", wantErr: true},
		{name: "rejects control character", input: "docs/line\nbreak", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRelPath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeRelPath(%q) error=%v wantErr=%v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeRelPath(%q)=%q want=%q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateFileName(t *testing.T) {
	for _, name := range []string{"photo.jpg", "夏 日.png", "..."} {
		if err := ValidateFileName(name); err != nil {
			t.Errorf("ValidateFileName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", ".", "..", "dir/file", `dir\\file`, "line\nbreak"} {
		if err := ValidateFileName(name); err == nil {
			t.Errorf("ValidateFileName(%q) unexpectedly succeeded", name)
		}
	}
}

func TestReservedNamesAreRejectedInEveryPathSegment(t *testing.T) {
	reserved := []string{
		".omnistore-upload-0123456789abcdef.tmp",
		".omnistore-copy-0123456789abcdef01234567.staging",
		".omnistore-upload-not-created-by-us.tmp",
	}
	for _, name := range reserved {
		if err := ValidateFileName(name); !errors.Is(err, ErrReservedName) {
			t.Errorf("ValidateFileName(%q) error=%v, want ErrReservedName", name, err)
		}
		if err := ValidateUserRelPath("parent/" + name + "/child"); !errors.Is(err, ErrReservedName) {
			t.Errorf("ValidateUserRelPath(%q) error=%v, want ErrReservedName", name, err)
		}
	}
	for _, relPath := range []string{"photos/2026", ".env", ".omnistore-write-test-user-file"} {
		if err := ValidateUserRelPath(relPath); err != nil {
			t.Errorf("ValidateUserRelPath(%q): %v", relPath, err)
		}
	}
}

func TestExcludeMatcher(t *testing.T) {
	matcher := NewExcludeMatcher([]string{"private/**", "**/*.tmp", `cache\*`, " "})
	tests := []struct {
		path        string
		match       bool
		matchPrefix bool
	}{
		{path: "public/readme.md"},
		{path: ".omnistore-upload-123", match: true, matchPrefix: true},
		{path: "nested/.OMNISTORE-UPLOAD-123", match: true, matchPrefix: true},
		{path: "nested/.OMNISTORE/img.png", match: true, matchPrefix: true},
		{path: "nested/.omnistore-upload-123", match: true, matchPrefix: true},
		{path: ".omnistore-write-test-a1b2c3", match: true, matchPrefix: true},
		{path: "nested/.OMNISTORE-WRITE-TEST-a1b2c3", match: true, matchPrefix: true},
		{path: "nested/.omnistore-write-test-a1b2c3", match: true, matchPrefix: true},
		{path: "private", match: true, matchPrefix: true},
		{path: "private/docs/readme.md", match: true, matchPrefix: true},
		{path: "build/output.tmp", match: true, matchPrefix: true},
		{path: "cache/item", match: true, matchPrefix: true},
	}
	for _, tt := range tests {
		if got := matcher.Match(tt.path); got != tt.match {
			t.Errorf("Match(%q)=%v want=%v", tt.path, got, tt.match)
		}
		if got := matcher.MatchPrefix(tt.path); got != tt.matchPrefix {
			t.Errorf("MatchPrefix(%q)=%v want=%v", tt.path, got, tt.matchPrefix)
		}
	}
	if matcher.Match("") || matcher.MatchPrefix("") {
		t.Fatal("root path must never be excluded")
	}
}

func TestProxyResolverClientIP(t *testing.T) {
	resolver := NewProxyResolver([]string{"127.0.0.1", "10.0.0.0/8", "2001:db8::1", "invalid"})
	tests := []struct {
		name   string
		remote string
		xff    string
		want   string
	}{
		{name: "direct client ignores spoofed header", remote: "203.0.113.9:4321", xff: "198.51.100.2", want: "203.0.113.9"},
		{name: "trusted proxy returns nearest untrusted hop", remote: "127.0.0.1:8080", xff: "198.51.100.8, 10.1.2.3", want: "198.51.100.8"},
		{name: "all trusted returns chain origin", remote: "127.0.0.1:8080", xff: "10.9.0.1, 127.0.0.1", want: "10.9.0.1"},
		{name: "trusted proxy without header", remote: "127.0.0.1:8080", want: "127.0.0.1"},
		{name: "remote without port", remote: "198.51.100.4", want: "198.51.100.4"},
		{name: "skips invalid forwarded entry", remote: "127.0.0.1:8080", xff: "198.51.100.6, garbage", want: "198.51.100.6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{RemoteAddr: tt.remote, Header: make(http.Header)}
			req.Header.Set("X-Forwarded-For", tt.xff)
			if got := resolver.ClientIP(req); got != tt.want {
				t.Fatalf("ClientIP()=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestResolveInSourceRejectsEscapeAndSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "photos", "2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "photos", "2026", "new.jpg")
	got, err := ResolveInSource(root, "photos/2026/new.jpg")
	if err != nil || got != want {
		t.Fatalf("ResolveInSource()=%q, %v want=%q", got, err, want)
	}
	if _, err := ResolveInSource(root, "../outside"); err == nil {
		t.Fatal("path escape unexpectedly succeeded")
	}

	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveInSource(root, "linked/file.txt"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink error=%v want=%v", err, ErrSymlink)
	}
}
