// Package storage owns the portable key layout and conditional S3 operations.
package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("object absent")
	ErrConflict = errors.New("object validator changed")
	ErrPath     = errors.New("invalid object path")
	ErrTooLarge = errors.New("object exceeds limit")
)

type Meta struct {
	Key, ETag, Version, ContentType string
	WriteETag                       string
	Size                            int64
	Modified                        time.Time
}

// Validator is the client-facing optimistic write token. Indexing and S3 reads
// continue using the underlying opaque ETag, even with database write revisions.
func (m Meta) Validator() string {
	if m.WriteETag != "" {
		return m.WriteETag
	}
	return m.ETag
}

func (m Meta) Equal(n Meta) bool {
	return m.ETag == n.ETag && m.Version == n.Version && m.Size == n.Size && m.Modified.Equal(n.Modified)
}

type Page struct {
	Objects []Meta
	Next    string
}
type Store interface {
	List(context.Context, string, string) (Page, error)
	Head(context.Context, string) (Meta, error)
	Get(context.Context, string, string, int64) ([]byte, Meta, error)
	Put(context.Context, string, []byte, string, string) (Meta, error)
	Delete(context.Context, string, string) error
}

type S3Config struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey, SessionToken string
	PrivateEndpoints                                             string
	AllowHTTP, PathStyle                                         bool
	DisableConditionalWrites                                     bool
}
type S3 struct {
	client                   *s3.Client
	bucket                   string
	disableConditionalWrites bool
}

func New(c S3Config) (*S3, error) {
	client, e := HTTPClient(c.Endpoint, c.PrivateEndpoints, c.AllowHTTP)
	if e != nil {
		return nil, e
	}
	opts := s3.Options{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, c.SessionToken), HTTPClient: client, UsePathStyle: c.PathStyle, RetryMaxAttempts: 3}
	if c.Endpoint != "" {
		opts.BaseEndpoint = aws.String(c.Endpoint)
		// Match rclone's compatible-provider defaults. Optional AWS CRC/trailer
		// headers are not implemented by every S3 provider; retain Content-MD5
		// integrity checking and all conditional-write headers instead.
		opts.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		opts.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}
	client.Transport = diagnosticTransport{client.Transport}
	return &S3{client: s3.New(opts), bucket: c.Bucket, disableConditionalWrites: c.DisableConditionalWrites}, nil
}
func (s *S3) List(ctx context.Context, prefix, cursor string) (Page, error) {
	in := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(200)}
	if cursor != "" {
		in.ContinuationToken = aws.String(cursor)
	}
	out, e := s.client.ListObjectsV2(ctx, in)
	if e != nil {
		return Page{}, classify("list_objects", e)
	}
	p := Page{Objects: make([]Meta, 0, len(out.Contents))}
	for _, o := range out.Contents {
		p.Objects = append(p.Objects, Meta{Key: aws.ToString(o.Key), ETag: aws.ToString(o.ETag), Size: aws.ToInt64(o.Size), Modified: aws.ToTime(o.LastModified)})
	}
	if aws.ToBool(out.IsTruncated) {
		p.Next = aws.ToString(out.NextContinuationToken)
		if p.Next == "" || p.Next == cursor {
			return Page{}, errors.New("invalid listing continuation")
		}
	}
	return p, nil
}
func (s *S3) Head(ctx context.Context, key string) (Meta, error) {
	out, e := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if e != nil {
		return Meta{}, classify("head_object", e)
	}
	return Meta{Key: key, ETag: aws.ToString(out.ETag), Version: aws.ToString(out.VersionId), Size: aws.ToInt64(out.ContentLength), Modified: aws.ToTime(out.LastModified), ContentType: aws.ToString(out.ContentType)}, nil
}
func (s *S3) Get(ctx context.Context, key, etag string, limit int64) ([]byte, Meta, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if etag != "" {
		in.IfMatch = aws.String(etag)
	}
	out, e := s.client.GetObject(ctx, in)
	if e != nil {
		return nil, Meta{}, classify("get_object", e)
	}
	defer out.Body.Close()
	b, e := io.ReadAll(io.LimitReader(out.Body, limit+1))
	if e != nil {
		return nil, Meta{}, errors.New("S3 read failed")
	}
	if int64(len(b)) > limit {
		return nil, Meta{}, ErrTooLarge
	}
	return b, Meta{Key: key, ETag: aws.ToString(out.ETag), Version: aws.ToString(out.VersionId), Size: aws.ToInt64(out.ContentLength), Modified: aws.ToTime(out.LastModified), ContentType: aws.ToString(out.ContentType)}, nil
}

// etag == "*" means create only. Updates require a concrete opaque ETag.
func (s *S3) Put(ctx context.Context, key string, b []byte, etag, contentType string) (Meta, error) {
	in := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(b), ContentType: aws.String(contentType)}
	digest := md5.Sum(b) // S3 transfer integrity, never a security-sensitive hash.
	in.ContentMD5 = aws.String(base64.StdEncoding.EncodeToString(digest[:]))
	if etag == "" {
		return Meta{}, ErrConflict
	}
	if s.disableConditionalWrites {
		if err := s.checkWriteVersion(ctx, key, etag); err != nil {
			return Meta{}, err
		}
	} else if etag == "*" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(etag)
	}
	out, e := s.client.PutObject(ctx, in)
	if e != nil {
		return Meta{}, classify("put_object", e)
	}
	return Meta{Key: key, ETag: aws.ToString(out.ETag), Version: aws.ToString(out.VersionId), Size: int64(len(b)), ContentType: contentType}, nil
}
func (s *S3) Delete(ctx context.Context, key, etag string) error {
	if etag == "" || etag == "*" {
		return ErrConflict
	}
	in := &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if s.disableConditionalWrites {
		if err := s.checkWriteVersion(ctx, key, etag); err != nil {
			return err
		}
	} else {
		in.IfMatch = aws.String(etag)
	}
	_, e := s.client.DeleteObject(ctx, in)
	return classify("delete_object", e)
}

// Compatibility mode checks current storage, never the derived index. Managed
// storage holds a per-object PostgreSQL lock through this check and mutation.
// External writers do not take that lock and can race this non-atomic sequence.
func (s *S3) checkWriteVersion(ctx context.Context, key, etag string) error {
	current, err := s.Head(ctx, key)
	if errors.Is(err, ErrNotFound) {
		if etag == "*" {
			return nil
		}
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if etag == "*" || current.ETag != etag {
		return ErrConflict
	}
	return nil
}

func Path(s string) bool {
	if s == "" || len(s) > 768 || !utf8.ValidString(s) || strings.ContainsAny(s, "\\\x00") || path.Clean(s) != s || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == "." || p == ".." || p == "" {
			return false
		}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func Owner(s string) bool { u, e := uuid.Parse(s); return e == nil && u != uuid.Nil && u.String() == s }

// Split accepts only explicitly owned Markdown and note attachment namespaces.
func Split(prefix, key string) (owner, kind, relative, note string, ok bool) {
	if !strings.HasPrefix(key, prefix) {
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(key, prefix), "/", 3)
	if len(parts) != 3 || !Owner(parts[0]) || !Path(parts[2]) {
		return
	}
	owner, kind, relative = parts[0], parts[1], parts[2]
	switch kind {
	case "notes":
		if !strings.HasSuffix(relative, ".md") {
			return "", "", "", "", false
		}
		note = relative
	case "attachments":
		i := strings.LastIndex(relative, "/")
		if i < 0 || !strings.HasSuffix(relative[:i], ".md") {
			return "", "", "", "", false
		}
		note = relative[:i]
	default:
		return "", "", "", "", false
	}
	ok = true
	return
}

// HTTPClient enforces the same connection-time policy as Files. The SDK uses
// this transport directly; there is no subprocess or general-purpose proxy.
func HTTPClient(endpoint, exceptions string, allowHTTP bool) (*http.Client, error) {
	allowed := map[string]bool{}
	for _, s := range strings.Split(exceptions, ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		h, p, e := net.SplitHostPort(s)
		if e != nil || h == "" || p == "" {
			return nil, errors.New("private endpoints must be exact host:port values")
		}
		allowed[s] = true
	}
	if endpoint != "" {
		u, e := url.Parse(endpoint)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("invalid S3_ENDPOINT")
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "http" {
				port = "80"
			} else {
				port = "443"
			}
		}
		if u.Scheme == "http" && (!allowHTTP || !allowed[net.JoinHostPort(strings.ToLower(u.Hostname()), port)]) {
			return nil, errors.New("HTTP requires exact operator endpoint exception")
		}
	}
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 32, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, p, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, errors.New("invalid destination")
		}
		exception := allowed[strings.ToLower(addr)]
		if p != "443" && !exception {
			return nil, errors.New("S3 port blocked")
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		if e != nil || len(ips) == 0 {
			return nil, errors.New("S3 DNS unavailable")
		}
		for _, ip := range ips {
			if !exception && !Public(ip) {
				return nil, errors.New("S3 private destination blocked")
			}
		}
		d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), p))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("S3 connection failed")
	}
	return &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return true
}
