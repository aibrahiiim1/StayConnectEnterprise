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
	"strings"
	"time"
)

// S3Dest keeps the ledger in an S3-compatible bucket owned by the hotel: Amazon S3, Backblaze B2, Wasabi, MinIO, a NAS that
// speaks S3. Requests are signed with AWS Signature Version 4; nothing else of the vendor's SDK is needed for three calls.
//
// The bucket should have versioning (or object lock) on so that a deleted or overwritten ledger can be recovered; the ledger
// does not depend on it, because every restore verifies the chain, but it is the customer's cheapest protection against their
// own mistakes. Credentials need only read, write and list on the one prefix.
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
	// Now is the clock, replaceable in tests.
	Now func() time.Time
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
	req, err := http.NewRequestWithContext(ctx, method, full, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	now := d.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
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
		req.ContentLength = int64(len(body))
	}
	return d.client().Do(req)
}

// signingKey is the AWS Signature Version 4 key derivation.
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

// Remove deletes an object (used to clean the probe up).
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
