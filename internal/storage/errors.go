package storage

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Providers may name the unsupported header in XML fields omitted by the SDK's
// generic API error. Observe at most 8 KiB while the SDK reads the same response;
// never log the response body or retain it in the classified failure.
type diagnosticTransport struct{ http.RoundTripper }

const diagnosticFeatureHeader = "X-Notes-Internal-S3-Feature"

func (t diagnosticTransport) CloseIdleConnections() {
	if closer, ok := t.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.RoundTripper.RoundTrip(r)
	if err == nil {
		response.Header.Del(diagnosticFeatureHeader)
		if response.StatusCode >= 400 && response.Body != nil {
			if response.Header == nil {
				response.Header = make(http.Header)
			}
			response.Body = &diagnosticBody{ReadCloser: response.Body, headers: response.Header}
		}
	}
	return response, err
}

type diagnosticBody struct {
	io.ReadCloser
	prefix  []byte
	headers http.Header
}

func (b *diagnosticBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	remaining := 8192 - len(b.prefix)
	if remaining > n {
		remaining = n
	}
	if remaining > 0 {
		b.prefix = append(b.prefix, p[:remaining]...)
	}
	return n, err
}

func (b *diagnosticBody) Close() error {
	var detail struct {
		Header       string
		ArgumentName string
	}
	if b.headers != nil && xml.NewDecoder(bytes.NewReader(b.prefix)).Decode(&detail) == nil {
		feature := headerFeature(detail.Header)
		if feature == "" {
			feature = headerFeature(detail.ArgumentName)
		}
		if feature != "" {
			b.headers.Set(diagnosticFeatureHeader, feature)
		}
	}
	return b.ReadCloser.Close()
}

func headerFeature(header string) string {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case "if-match", "if-none-match":
		return "conditional_write"
	case "x-amz-trailer", "transfer-encoding", "content-encoding":
		return "streaming_checksum"
	case "x-amz-sdk-checksum-algorithm", "x-amz-checksum-algorithm", "x-amz-checksum-crc32", "x-amz-checksum-crc32c", "x-amz-checksum-crc64nvme", "x-amz-checksum-sha1", "x-amz-checksum-sha256":
		return "checksum"
	case "content-length":
		return "content_length"
	}
	return ""
}

// Failure retains only bounded, non-sensitive diagnostics. Never retain/log an
// SDK error message: it can contain object keys, signed URLs or response bodies.
type Failure struct {
	Operation, Kind, Code, Feature string
	Status                         int
}

func (e *Failure) Error() string { return "S3 operation failed" }
func classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	f := &Failure{Operation: operation, Kind: "s3", Code: "other"}
	var api smithy.APIError
	if errors.As(err, &api) {
		message := strings.ToLower(api.ErrorMessage())
		switch {
		case strings.Contains(message, "if-none-match"), strings.Contains(message, "if-match"), strings.Contains(message, "conditional"):
			f.Feature = "conditional_write"
		case strings.Contains(message, "streaming-unsigned-payload-trailer"), strings.Contains(message, "aws-chunked"), strings.Contains(message, "x-amz-trailer"):
			f.Feature = "streaming_checksum"
		case strings.Contains(message, "checksum"):
			f.Feature = "checksum"
		case strings.Contains(message, "content-length"):
			f.Feature = "content_length"
		}
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return ErrNotFound
		case "PreconditionFailed", "ConditionalRequestConflict":
			return ErrConflict
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "AuthorizationHeaderMalformed", "ExpiredToken", "InvalidToken", "NotImplemented", "InvalidRequest", "InvalidArgument", "InvalidDigest", "BadDigest", "XAmzContentSHA256Mismatch", "MissingContentLength", "MalformedXML", "RequestTimeout", "SlowDown", "InternalError", "ServiceUnavailable", "NoSuchBucket", "InvalidBucketName":
			f.Code = api.ErrorCode()
		}
	}
	var response interface{ HTTPStatusCode() int }
	if errors.As(err, &response) {
		status := response.HTTPStatusCode()
		if status >= 100 && status <= 599 {
			f.Status = status
		}
	}
	var raw *smithyhttp.ResponseError
	if errors.As(err, &raw) && raw.Response != nil {
		switch feature := raw.Response.Header.Get(diagnosticFeatureHeader); feature {
		case "conditional_write", "streaming_checksum", "checksum", "content_length":
			f.Feature = feature
		}
	}
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		f.Kind = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		f.Kind = "timeout"
	case errors.As(err, &network):
		if network.Timeout() {
			f.Kind = "timeout"
		} else {
			f.Kind = "network"
		}
	}
	return f
}

// ErrorAttrs is the only path from dependency failures to structured log fields.
func ErrorAttrs(err error) []any {
	var failure *Failure
	if errors.As(err, &failure) {
		attrs := []any{"error_kind", failure.Kind, "operation", failure.Operation, "s3_code", failure.Code}
		if failure.Feature != "" {
			attrs = append(attrs, "s3_feature", failure.Feature)
		}
		if failure.Status != 0 {
			attrs = append(attrs, "upstream_status", failure.Status)
		}
		return attrs
	}
	if errors.Is(err, context.Canceled) {
		return []any{"error_kind", "canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return []any{"error_kind", "timeout"}
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		code := state.SQLState()
		valid := len(code) == 5
		for _, b := range []byte(code) {
			if !(b >= '0' && b <= '9' || b >= 'A' && b <= 'Z') {
				valid = false
			}
		}
		if valid {
			return []any{"error_kind", "postgres_" + code}
		}
	}
	return []any{"error_kind", "dependency"}
}
