package ledger

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// S3Dest keeps the ledger in an S3-compatible bucket owned by the hotel, signing requests with AWS Signature Version 4.
// The bucket should have versioning or object lock on so a deleted or overwritten ledger can be recovered; credentials
// need only read, write and list on the one prefix.
type S3Dest struct {
	Endpoint  string // "https://host[:port]"; empty means Amazon S3 for Region
	Region    string
	Bucket    string
	Prefix    string
	AccessKey string
	SecretKey string
	Client    *http.Client
	// AllowLocal permits an endpoint on this appliance, and plain http (tests only).
	AllowLocal bool
	Now        func() time.Time
}

func (d *S3Dest) Kind() string { return "s3" }
func (d *S3Dest) Describe() string {
	return "s3://" + d.Bucket + "/" + strings.Trim(d.Prefix, "/")
}

func (d *S3Dest) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (d *S3Dest) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *S3Dest) base() (*url.URL, bool, error) {
	if d.Endpoint == "" {
		if d.Region == "" {
			return nil, false, errors.New("a region is required")
		}
		u, _ := url.Parse("https://" + d.Bucket + ".s3." + d.Region + ".amazonaws.com")
		return u, false, nil
	}
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, false, errors.New("the endpoint must be an https address")
	}
	return u, true, nil
}

func (d *S3Dest) Check(ctx context.Context) error {
	if d.Bucket == "" || d.AccessKey == "" || d.SecretKey == "" {
		return errors.New("the bucket and its access key are required")
	}
	u, _, err := d.base()
	if err != nil {
		return err
	}
	if !d.AllowLocal {
		if u.Scheme != "https" {
			return errors.New("the endpoint must use https")
		}
		if isLocalHost(u.Hostname()) {
			return errors.New("that endpoint is this appliance: a copy kept here is not independent of it")
		}
	}
	return checkRoundTrip(ctx, d)
}

func (d *S3Dest) key(name string) string {
	p := strings.Trim(d.Prefix, "/")
	if p == "" {
		return name
	}
	return p + "/" + name
}

func (d *S3Dest) request(ctx context.Context, method, name string, body []byte) (*http.Response, error) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("ledger: bad object name %q", name)
	}
	return d.signed(ctx, method, name, body)
}

// signed sends one SigV4-signed request; an empty name addresses the bucket itself (used by tests to create one).
func (d *S3Dest) signed(ctx context.Context, method, name string, body []byte) (*http.Response, error) {
	sum := sha256.Sum256(body)
	return d.signedStream(ctx, method, name, bytes.NewReader(body), int64(len(body)), hex.EncodeToString(sum[:]))
}

// signedStream signs with the body's SHA-256 computed beforehand (never UNSIGNED-PAYLOAD), so a large file is streamed from
// disk and the bucket still refuses a body that is not the one signed.
func (d *S3Dest) signedStream(ctx context.Context, method, name string, body io.Reader, size int64, payloadHash string) (*http.Response, error) {
	u, pathStyle, err := d.base()
	if err != nil {
		return nil, err
	}
	reqURL := *u
	uriPath := "/" + d.key(name)
	if pathStyle {
		uriPath = "/" + d.Bucket + "/" + d.key(name)
		if name == "" {
			uriPath = "/" + d.Bucket
		}
	}
	reqURL.Path = ""
	reqURL.RawPath = ""
	enc := encodePath(uriPath)
	full := reqURL.String() + enc
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, err
	}
	now := d.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	host := req.URL.Host
	req.Host = host

	signed := "host;x-amz-content-sha256;x-amz-date"
	canon := strings.Join([]string{
		method, enc, "",
		"host:" + host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n",
		signed, payloadHash,
	}, "\n")
	scope := day + "/" + d.Region + "/s3/aws4_request"
	csum := sha256.Sum256([]byte(canon))
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(csum[:])
	sig := hex.EncodeToString(hmacSHA256(signingKey(d.SecretKey, day, d.Region, "s3"), sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+d.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	if method == http.MethodPut {
		req.ContentLength = size
	}
	return d.client().Do(req)
}

// PutFile uploads a file in one signed PUT (S3 accepts up to 5 GiB that way; a backup set is far smaller).
func (d *S3Dest) PutFile(ctx context.Context, name, localPath string) error {
	if err := validObjectName(name); err != nil {
		return err
	}
	sum, size, err := FileDigest(localPath)
	if err != nil {
		return err
	}
	if size > 5<<30 {
		return errors.New("the file is larger than one upload to the bucket allows (5 GiB)")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cl := *d.client()
	cl.Timeout = 0 // a large upload is bounded by ctx, not by the 60 s request timeout
	dd := *d
	dd.Client = &cl
	resp, err := dd.signedStream(ctx, http.MethodPut, name, contextReader{ctx, f}, size, sum)
	if err != nil {
		return fmt.Errorf("the bucket cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the bucket refused the write: %s", s3Problem(resp))
	}
	return nil
}

// Digest reads the object back in full and hashes it; an ETag is not a content hash for every S3 implementation.
func (d *S3Dest) Digest(ctx context.Context, name string) (string, int64, error) {
	if err := validObjectName(name); err != nil {
		return "", 0, err
	}
	cl := *d.client()
	cl.Timeout = 0
	dd := *d
	dd.Client = &cl
	resp, err := dd.signed(ctx, http.MethodGet, name, nil)
	if err != nil {
		return "", 0, fmt.Errorf("the bucket cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", 0, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return "", 0, fmt.Errorf("the bucket refused the read: %s", s3Problem(resp))
	}
	return digestReader(contextReader{ctx, resp.Body})
}

// Locate names the endpoint, bucket and prefix the objects are written to; a bucket has no free-space figure.
func (d *S3Dest) Locate(context.Context) (Location, error) {
	u, _, err := d.base()
	if err != nil {
		return Location{Medium: "s3", Where: d.Describe()}, err
	}
	return Location{Medium: "s3", Where: d.Describe(), Source: u.Host}, nil
}

func signingKey(secret, day, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// encodePath percent-encodes every path segment as S3 expects (RFC 3986 unreserved characters stay).
func encodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEscape(s)
	}
	return strings.Join(segs, "/")
}

func uriEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (d *S3Dest) Put(ctx context.Context, name string, data []byte) error {
	resp, err := d.request(ctx, http.MethodPut, name, data)
	if err != nil {
		return fmt.Errorf("the bucket cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the bucket refused the write: %s", s3Problem(resp))
	}
	return nil
}

func (d *S3Dest) Get(ctx context.Context, name string) ([]byte, error) {
	resp, err := d.request(ctx, http.MethodGet, name, nil)
	if err != nil {
		return nil, fmt.Errorf("the bucket cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// A missing KEY is "not found"; a missing BUCKET is a configuration error and must not read as an empty ledger.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if bytes.Contains(b, []byte("NoSuchBucket")) {
			return nil, errors.New("the bucket does not exist")
		}
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the bucket refused the read: %s", s3Problem(resp))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxObject))
}

func (d *S3Dest) Remove(ctx context.Context, name string) error {
	resp, err := d.request(ctx, http.MethodDelete, name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("the bucket refused the delete: %s", s3Problem(resp))
	}
	return nil
}

func s3Problem(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	code := between(string(b), "<Code>", "</Code>")
	if code == "" {
		return resp.Status
	}
	if m := between(string(b), "<Message>", "</Message>"); m != "" {
		return code + ": " + m
	}
	return code
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	i += len(a)
	j := strings.Index(s[i:], b)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}
