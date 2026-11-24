package s3api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/models"
)

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	emptySHA256    = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	ErrMissingAuthentication = errors.New("缺少 Signature V4 鉴权信息")
	ErrInvalidAuthentication = errors.New("Signature V4 鉴权信息无效")
	ErrSignatureMismatch     = errors.New("请求签名不匹配")
	ErrRequestExpired        = errors.New("请求时间已过期")
)

type authenticatedRequest struct {
	User        *models.User
	AccessKeyID string
	PayloadHash string
}

type signatureVerifier struct {
	credentials *Credentials
	now         func() time.Time
}

func newSignatureVerifier(credentials *Credentials) *signatureVerifier {
	return &signatureVerifier{credentials: credentials, now: time.Now}
}

func (v *signatureVerifier) verify(r *http.Request) (*authenticatedRequest, error) {
	if r.URL.Query().Get("X-Amz-Algorithm") != "" {
		return v.verifyPresigned(r)
	}
	return v.verifyHeader(r)
}

func (v *signatureVerifier) verifyHeader(r *http.Request) (*authenticatedRequest, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, sigV4Algorithm+" ") {
		return nil, ErrMissingAuthentication
	}
	fields, err := parseAuthorizationFields(strings.TrimPrefix(authorization, sigV4Algorithm+" "))
	if err != nil {
		return nil, err
	}
	accessKeyID, date, region, signedHeaders, scope, err := parseCredentialScope(fields["Credential"], fields["SignedHeaders"])
	if err != nil {
		return nil, err
	}
	signature := fields["Signature"]
	if len(signature) != 64 {
		return nil, ErrInvalidAuthentication
	}
	amzDate := r.Header.Get("X-Amz-Date")
	requestTime, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil || requestTime.UTC().Format("20060102") != date {
		return nil, ErrInvalidAuthentication
	}
	if delta := v.now().UTC().Sub(requestTime); delta < -15*time.Minute || delta > 15*time.Minute {
		return nil, ErrRequestExpired
	}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		return nil, ErrInvalidAuthentication
	}
	canonical, err := canonicalRequest(r, signedHeaders, payloadHash, false)
	if err != nil {
		return nil, err
	}
	return v.finish(r, accessKeyID, date, region, scope, amzDate, signature, payloadHash, canonical)
}

func (v *signatureVerifier) verifyPresigned(r *http.Request) (*authenticatedRequest, error) {
	query := r.URL.Query()
	if query.Get("X-Amz-Algorithm") != sigV4Algorithm {
		return nil, ErrInvalidAuthentication
	}
	accessKeyID, date, region, signedHeaders, scope, err := parseCredentialScope(query.Get("X-Amz-Credential"), query.Get("X-Amz-SignedHeaders"))
	if err != nil {
		return nil, err
	}
	amzDate := query.Get("X-Amz-Date")
	requestTime, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil || requestTime.UTC().Format("20060102") != date {
		return nil, ErrInvalidAuthentication
	}
	expires, err := strconv.Atoi(query.Get("X-Amz-Expires"))
	if err != nil || expires < 1 || expires > 604800 {
		return nil, ErrInvalidAuthentication
	}
	now := v.now().UTC()
	if now.Before(requestTime.Add(-15*time.Minute)) || now.After(requestTime.Add(time.Duration(expires)*time.Second)) {
		return nil, ErrRequestExpired
	}
	signature := query.Get("X-Amz-Signature")
	if len(signature) != 64 {
		return nil, ErrInvalidAuthentication
	}
	const payloadHash = "UNSIGNED-PAYLOAD"
	canonical, err := canonicalRequest(r, signedHeaders, payloadHash, true)
	if err != nil {
		return nil, err
	}
	return v.finish(r, accessKeyID, date, region, scope, amzDate, signature, payloadHash, canonical)
}

func (v *signatureVerifier) finish(r *http.Request, accessKeyID, date, region, scope, amzDate, signature, payloadHash, canonical string) (*authenticatedRequest, error) {
	user, secret, err := v.credentials.Resolve(accessKeyID)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) || errors.Is(err, ErrCredentialDisabled) {
			return nil, ErrInvalidAuthentication
		}
		return nil, err
	}
	stringToSign := sigV4Algorithm + "\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	expected := hex.EncodeToString(hmacSHA256(signingKey(secret, date, region, "s3"), stringToSign))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(expected)) != 1 {
		return nil, ErrSignatureMismatch
	}
	v.credentials.Touch(accessKeyID)
	return &authenticatedRequest{User: user, AccessKeyID: accessKeyID, PayloadHash: payloadHash}, nil
}

func parseAuthorizationFields(value string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || key == "" || val == "" {
			return nil, ErrInvalidAuthentication
		}
		out[key] = val
	}
	for _, key := range []string{"Credential", "SignedHeaders", "Signature"} {
		if out[key] == "" {
			return nil, ErrInvalidAuthentication
		}
	}
	return out, nil
}

func parseCredentialScope(credential, signedHeaderValue string) (accessKeyID, date, region string, signedHeaders []string, scope string, err error) {
	parts := strings.Split(credential, "/")
	if len(parts) != 5 || parts[0] == "" || len(parts[1]) != 8 || parts[2] == "" || parts[3] != "s3" || parts[4] != "aws4_request" {
		err = ErrInvalidAuthentication
		return
	}
	signedHeaders = strings.Split(signedHeaderValue, ";")
	if len(signedHeaders) == 0 || !sort.StringsAreSorted(signedHeaders) {
		err = ErrInvalidAuthentication
		return
	}
	foundHost := false
	for i, header := range signedHeaders {
		if header == "" || header != strings.ToLower(header) || (i > 0 && signedHeaders[i-1] == header) {
			err = ErrInvalidAuthentication
			return
		}
		foundHost = foundHost || header == "host"
	}
	if !foundHost {
		err = ErrInvalidAuthentication
		return
	}
	accessKeyID, date, region = parts[0], parts[1], parts[2]
	scope = strings.Join(parts[1:], "/")
	return
}

func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string, presigned bool) (string, error) {
	if err := validateSignedHeaders(r, signedHeaders); err != nil {
		return "", err
	}
	var headers strings.Builder
	for _, name := range signedHeaders {
		var values []string
		if name == "host" {
			values = []string{r.Host}
		} else {
			values = r.Header.Values(http.CanonicalHeaderKey(name))
		}
		if len(values) == 0 {
			return "", ErrInvalidAuthentication
		}
		for i := range values {
			values[i] = strings.Join(strings.Fields(values[i]), " ")
		}
		headers.WriteString(name)
		headers.WriteByte(':')
		headers.WriteString(strings.Join(values, ","))
		headers.WriteByte('\n')
	}
	return strings.Join([]string{
		r.Method,
		canonicalURI(r.URL),
		canonicalQuery(r.URL.Query(), presigned),
		headers.String(),
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n"), nil
}

func validateSignedHeaders(r *http.Request, signedHeaders []string) error {
	signed := make(map[string]bool, len(signedHeaders))
	for _, name := range signedHeaders {
		signed[name] = true
	}
	if r.Header.Get("X-Amz-Date") != "" && !signed["x-amz-date"] {
		return ErrInvalidAuthentication
	}
	for name := range r.Header {
		lower := strings.ToLower(name)
		// S3 在 PayloadHash 行中隐式纳入 x-amz-content-sha256，其余 x-amz-* 必须签名。
		if strings.HasPrefix(lower, "x-amz-") && lower != "x-amz-content-sha256" && !signed[lower] {
			return ErrInvalidAuthentication
		}
	}
	return nil
}

// canonicalURI 基于原始转义路径（EscapedPath / RawPath）逐段归一化编码，
// 保持 %2F 与真实路径分隔符 / 的语义区分，符合 AWS Signature V4 规范。
func canonicalURI(u *url.URL) string {
	raw := u.EscapedPath()
	if raw == "" {
		return "/"
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	segments := strings.Split(raw, "/")
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		if decoded, err := url.PathUnescape(seg); err == nil {
			segments[i] = awsEncode(decoded, false)
		} else {
			segments[i] = awsEncode(seg, false)
		}
	}
	return strings.Join(segments, "/")
}

func canonicalQuery(values url.Values, omitSignature bool) string {
	type pair struct{ key, value string }
	pairs := []pair{}
	for key, items := range values {
		if omitSignature && strings.EqualFold(key, "X-Amz-Signature") {
			continue
		}
		if len(items) == 0 {
			items = []string{""}
		}
		for _, value := range items {
			pairs = append(pairs, pair{awsEncode(key, false), awsEncode(value, false)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	parts := make([]string, len(pairs))
	for i, item := range pairs {
		parts[i] = item.key + "=" + item.value
	}
	return strings.Join(parts, "&")
}

func awsEncode(value string, preserveSlash bool) string {
	const hexChars = "0123456789ABCDEF"
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' || (preserveSlash && b == '/') {
			out.WriteByte(b)
		} else {
			out.WriteByte('%')
			out.WriteByte(hexChars[b>>4])
			out.WriteByte(hexChars[b&15])
		}
	}
	return out.String()
}

func signingKey(secret, date, region, service string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), date)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, service)
	return hmacSHA256(serviceKey, "aws4_request")
}

func hmacSHA256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validatePayloadHash(value string) error {
	if value == "UNSIGNED-PAYLOAD" || value == streamingUnsignedTrailer {
		return nil
	}
	if strings.HasPrefix(value, "STREAMING-") {
		return fmt.Errorf("流式 aws-chunked payload 尚未支持")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return ErrInvalidAuthentication
	}
	return nil
}
