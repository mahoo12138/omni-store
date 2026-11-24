package s3api

import (
	"net/http"
	"net/url"
	"testing"
)

func TestCanonicalURIPreservesEncodedSlashAndNormalizesSegments(t *testing.T) {
	cases := []struct {
		name   string
		rawURL string
		want   string
	}{
		{name: "root", rawURL: "http://example.com/", want: "/"},
		{name: "plain path", rawURL: "http://example.com/bucket/a/b.txt", want: "/bucket/a/b.txt"},
		{name: "encoded slash distinct from separator", rawURL: "http://example.com/bucket/a%2Fb.txt", want: "/bucket/a%2Fb.txt"},
		{name: "lowercase percent normalized to upper", rawURL: "http://example.com/bucket/a%2fb%20c.txt", want: "/bucket/a%2Fb%20c.txt"},
		{name: "unreserved tilde decoded", rawURL: "http://example.com/bucket/doc%7Ev1.txt", want: "/bucket/doc~v1.txt"},
		{name: "trailing slash preserved", rawURL: "http://example.com/bucket/dir/", want: "/bucket/dir/"},
		{name: "unicode utf8 percent encoded", rawURL: "http://example.com/bucket/%E6%B5%8B%E8%AF%95.txt", want: "/bucket/%E6%B5%8B%E8%AF%95.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.rawURL, err)
			}
			if got := canonicalURI(u); got != tc.want {
				t.Fatalf("canonicalURI(%q) = %q, want %q", tc.rawURL, got, tc.want)
			}
		})
	}
}

func TestAWSEncodeHandlesInvalidUTF8RawBytes(t *testing.T) {
	got := awsEncode(string([]byte{0x80, 0xFF, 'a', '~', '/'}), false)
	want := "%80%FFa~%2F"
	if got != want {
		t.Fatalf("awsEncode invalid utf8 = %q, want %q", got, want)
	}
}

// TestSigV4IndependentVector 使用独立于服务端辅助函数的 AWS 官方示例风格向量校验签名计算。
func TestSigV4IndependentVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com/src-demo/a%2Fb%20c.txt?list-type=2&prefix=a%2Fb", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.com"
	req.Header.Set("X-Amz-Date", "20261124T010203Z")
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)

	gotCanonical, err := canonicalRequest(req, []string{"host", "x-amz-date"}, emptySHA256, false)
	if err != nil {
		t.Fatalf("canonicalRequest: %v", err)
	}
	wantCanonical := "GET\n" +
		"/src-demo/a%2Fb%20c.txt\n" +
		"list-type=2&prefix=a%2Fb\n" +
		"host:example.com\n" +
		"x-amz-date:20261124T010203Z\n\n" +
		"host;x-amz-date\n" +
		emptySHA256
	if gotCanonical != wantCanonical {
		t.Fatalf("canonicalRequest mismatch:\ngot:\n%s\nwant:\n%s", gotCanonical, wantCanonical)
	}
}
