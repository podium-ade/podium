// Package fakes3 is an in-process S3-compatible endpoint for Podium's own tests.
//
// It exists because this repository may not pull a MinIO image, and because a fake that
// ignores authentication would make every presigned-URL test worthless: a URL that is
// accepted no matter what it is signed with proves nothing about signing. So the object
// semantics come from gofakes3, which speaks enough of the S3 API for minio-go — including
// the aws-chunked bodies minio-go sends over plain HTTP — and the AWS SigV4 verification in
// front of it is real: a presigned request whose signature, key, expiry or path has been
// touched is refused with 403.
//
// Header-authenticated requests (everything the server itself does) are not verified. The
// credential is still checked for presence, but the signature is not recomputed: minio-go
// signs those bodies with a streaming signature whose chunk framing gofakes3 already
// unwraps, and re-deriving it would be reimplementing the client. Presigned URLs are the
// interesting half — they are the only thing Podium ever hands to somebody else — and those
// are verified in full.
//
// It is test infrastructure. Nothing in cmd/ imports it.
package fakes3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/alvaroibarguen/podium/internal/server/artifacts"
)

// Credentials the fake accepts. They are obvious fixtures, not secrets.
const (
	AccessKey = "podium-test-access-key"
	SecretKey = "podium-test-secret-key"
	Bucket    = "podium-test"
)

// Server is a running fake endpoint.
type Server struct {
	// URL is the endpoint, "http://127.0.0.1:port".
	URL string
	// Rejected counts presigned requests refused for a bad signature.
	Rejected int
	srv      *httptest.Server
}

// Start brings up a fake endpoint bound to loopback and stops it when the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	backend := s3mem.New()
	faker := gofakes3.New(backend)
	inner := faker.Server()

	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Amz-Algorithm") != "" {
			if err := VerifyPresigned(r, AccessKey, SecretKey); err != nil {
				s.Rejected++
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprintf(w, "<Error><Code>SignatureDoesNotMatch</Code><Message>%s</Message></Error>", err)
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	s.URL = s.srv.URL
	return s
}

// Config is the Podium configuration that points at this endpoint.
func (s *Server) Config() artifacts.Config {
	return artifacts.Config{
		Endpoint:  strings.TrimPrefix(s.URL, "http://"),
		Bucket:    Bucket,
		AccessKey: AccessKey,
		SecretKey: SecretKey,
		Region:    artifacts.DefaultRegion,
	}
}

// Close stops the endpoint early. The test cleanup does it otherwise.
func (s *Server) Close() { s.srv.Close() }

// DeadConfig points at a loopback port with nothing listening on it: what "MinIO is down"
// looks like to the control plane.
func DeadConfig(t *testing.T) artifacts.Config {
	t.Helper()
	// A closed httptest server leaves a port that is guaranteed to have been bindable and
	// is now refusing connections.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	return artifacts.Config{
		Endpoint:  addr,
		Bucket:    Bucket,
		AccessKey: AccessKey,
		SecretKey: SecretKey,
		Region:    artifacts.DefaultRegion,
	}
}

// unsignedPayload is the payload hash AWS specifies for a presigned request.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// VerifyPresigned recomputes the AWS SigV4 query-string signature of r and reports whether
// it matches. It is the whole point of this package: without it a presign test asserts
// only that a URL was produced.
func VerifyPresigned(r *http.Request, accessKey, secretKey string) error {
	q := r.URL.Query()
	if alg := q.Get("X-Amz-Algorithm"); alg != "AWS4-HMAC-SHA256" {
		return fmt.Errorf("unsupported algorithm %q", alg)
	}
	sig := q.Get("X-Amz-Signature")
	if sig == "" {
		return errors.New("no X-Amz-Signature")
	}
	parts := strings.Split(q.Get("X-Amz-Credential"), "/")
	if len(parts) != 5 {
		return fmt.Errorf("malformed X-Amz-Credential %q", q.Get("X-Amz-Credential"))
	}
	if parts[0] != accessKey {
		return fmt.Errorf("unknown access key %q", parts[0])
	}
	amzDate := q.Get("X-Amz-Date")
	issued, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return fmt.Errorf("malformed X-Amz-Date %q", amzDate)
	}
	expires, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil {
		return fmt.Errorf("malformed X-Amz-Expires %q", q.Get("X-Amz-Expires"))
	}
	if time.Now().UTC().After(issued.Add(time.Duration(expires) * time.Second)) {
		return errors.New("expired")
	}

	signedHeaders := strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	q.Del("X-Amz-Signature")

	var headers strings.Builder
	for _, h := range signedHeaders {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		headers.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}

	canonicalRequest := strings.Join([]string{
		r.Method,
		encodePath(r.URL.Path),
		canonicalQuery(q),
		headers.String(),
		strings.Join(signedHeaders, ";"),
		unsignedPayload,
	}, "\n")

	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		strings.Join(parts[1:], "/"),
		hex.EncodeToString(crHash[:]),
	}, "\n")

	key := []byte("AWS4" + secretKey)
	for _, p := range parts[1:] {
		key = hmacSHA256(key, p)
	}
	want := hex.EncodeToString(hmacSHA256(key, stringToSign))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("signature does not match")
	}
	return nil
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(uriEncode(k) + "=" + uriEncode(v))
		}
	}
	return b.String()
}

// encodePath encodes each path segment and keeps the separators, which is what the AWS
// canonical URI is.
func encodePath(p string) string {
	if p == "" {
		return "/"
	}
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = uriEncode(seg)
	}
	return strings.Join(segments, "/")
}

// uriEncode is RFC 3986 percent-encoding with AWS's unreserved set.
func uriEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
