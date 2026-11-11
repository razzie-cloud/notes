package storage

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

func TestSafeFailureDiagnostics(t *testing.T) {
	secret := "SECRET https://private.example/bucket/note.md?signature=credential"
	for _, code := range []string{"InvalidArgument", "AccessDenied", "NotImplemented", secret} {
		err := classify("put_object", &smithy.GenericAPIError{Code: code, Message: secret})
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatal(err)
		}
		b, e := json.Marshal(ErrorAttrs(err))
		if e != nil || strings.Contains(string(b), secret) || strings.Contains(err.Error(), secret) {
			t.Fatal("raw backend data leaked", e)
		}
		if code == secret && failure.Code != "other" {
			t.Fatal("unknown code not redacted")
		}
	}
	if attrs := ErrorAttrs(classify("put_object", context.DeadlineExceeded)); attrs[1] != "timeout" {
		t.Fatal(attrs)
	}
}
func TestPutFailureRetainsSafeProviderStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(400)
		w.Write([]byte(`<Error><Code>InvalidArgument</Code><Message>private path and credential</Message></Error>`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	s, e := New(S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "test", AccessKey: "test", SecretKey: "test", PrivateEndpoints: endpoint.Host, AllowHTTP: true, PathStyle: true})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Put(context.Background(), "note.md", []byte("private note body"), "*", "text/markdown")
	var failure *Failure
	if !errors.As(e, &failure) || failure.Code != "InvalidArgument" || failure.Status != 400 || failure.Operation != "put_object" {
		t.Fatal(e, failure)
	}
}

func TestUnsupportedHeaderDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	for _, header := range []string{"If-None-Match", "x-amz-sdk-checksum-algorithm", "private-object-key"} {
		t.Run(header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(501)
				io.WriteString(w, `<Error><Code>NotImplemented</Code><Message>A header implies functionality that is not implemented</Message><Header>`+header+`</Header><Key>private-object-key</Key></Error>`)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			s, err := New(S3Config{Endpoint: server.URL, Region: "test", Bucket: "test", AccessKey: "test", SecretKey: "test", PrivateEndpoints: endpoint.Host, AllowHTTP: true, PathStyle: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Put(context.Background(), "private-object-key", []byte("private body"), "*", "text/markdown")
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "NotImplemented" || failure.Status != 501 || failure.Feature != headerFeature(header) {
				t.Fatal(err, failure)
			}
			data, _ := json.Marshal(ErrorAttrs(err))
			if strings.Contains(string(data), "private") {
				t.Fatal("private response data leaked")
			}
		})
	}
	reader := &diagnosticBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", 16384)))}
	data, err := io.ReadAll(reader)
	if err != nil || len(data) != 16384 || len(reader.prefix) != 8192 {
		t.Fatal("diagnostic capture changed or failed to bound the response")
	}
}

func TestCompatibleProviderUploadsPreserveIntegrityAndPreconditions(t *testing.T) {
	for _, validator := range []string{"*", `"existing"`} {
		t.Run(validator, func(t *testing.T) {
			payload := []byte("markdown body")
			digest := md5.Sum(payload)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, header := range []string{"X-Amz-Sdk-Checksum-Algorithm", "X-Amz-Checksum-Crc32", "X-Amz-Trailer", "Content-Encoding"} {
					if r.Header.Get(header) != "" {
						t.Errorf("unsupported optional header %s", header)
						w.WriteHeader(501)
						return
					}
				}
				if r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(digest[:]) {
					t.Error("missing transfer integrity checksum")
				}
				if validator == "*" && r.Header.Get("If-None-Match") != "*" || validator != "*" && r.Header.Get("If-Match") != validator {
					t.Error("write precondition lost")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != string(payload) || r.ContentLength != int64(len(payload)) {
					t.Error("upload body or length changed")
				}
				w.Header().Set("ETag", `"saved"`)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			s, err := New(S3Config{Endpoint: server.URL, Region: "test", Bucket: "test", AccessKey: "test", SecretKey: "test", PrivateEndpoints: endpoint.Host, PathStyle: true})
			if err != nil {
				t.Fatal(err)
			}
			client := s.client.Options().HTTPClient.(*http.Client)
			client.Transport.(diagnosticTransport).RoundTripper.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			meta, err := s.Put(context.Background(), "note.md", payload, validator, "text/markdown")
			if err != nil || meta.ETag != `"saved"` {
				t.Fatal(meta, err)
			}
		})
	}
}
