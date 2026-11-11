package config

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/razzie-cloud/notes/internal/gatewayidentity"
)

type Config struct {
	AccessGatewayURL                                         string
	LandingOrigin                                            string
	Port, Origin, PostgresURI, PostgresURIFrom, PasswordFile string
	PrivateEndpoints                                         string
	AllowHTTP                                                bool
	ScanInterval                                             time.Duration
	Workers                                                  int
	MaxNoteBytes, MaxAttachmentBytes                         int64
}

func Load(args []string, env func(string) string) (Config, error) {
	var c Config
	f := flag.NewFlagSet("notes", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	str := func(p *string, flagName, key, fallback string) {
		v := env(key)
		if v == "" {
			v = fallback
		}
		f.StringVar(p, flagName, v, key)
	}
	str(&c.AccessGatewayURL, "access-gateway-url", "ACCESS_GATEWAY_URL", "")
	str(&c.Port, "port", "SERVICE_PORT", "8080")
	str(&c.LandingOrigin, "landing-origin", "LANDING_ORIGIN", "https://razzie.cloud")
	str(&c.Origin, "origin", "ORIGIN", "http://localhost:8080")
	str(&c.PostgresURI, "postgres-uri", "POSTGRES_URI", "")
	str(&c.PostgresURIFrom, "postgres-uri-from", "POSTGRES_URI_FROM", "")
	str(&c.PasswordFile, "postgres-password-file", "POSTGRES_PASSWORD_FILE", "")
	str(&c.PrivateEndpoints, "s3-allowed-private-endpoints", "S3_ALLOWED_PRIVATE_ENDPOINTS", "")
	var interval, workers, noteLimit, attachmentLimit string
	str(&interval, "scan-interval", "SCAN_INTERVAL", "5m")
	str(&workers, "index-workers", "INDEX_WORKERS", "4")
	str(&noteLimit, "max-note-bytes", "MAX_NOTE_BYTES", "2097152")
	str(&attachmentLimit, "max-attachment-bytes", "MAX_ATTACHMENT_BYTES", "33554432")
	allow, err := strconv.ParseBool(defaultValue(env("S3_ALLOW_HTTP"), "false"))
	if err != nil {
		return c, errors.New("invalid S3_ALLOW_HTTP")
	}
	f.BoolVar(&c.AllowHTTP, "s3-allow-http", allow, "allow HTTP for exact endpoint exceptions")
	if err = f.Parse(args); err != nil || f.NArg() != 0 {
		return c, errors.New("invalid command-line configuration")
	}
	if c.AccessGatewayURL != "" {
		if err := gatewayidentity.ValidateURL(c.AccessGatewayURL); err != nil {
			return c, err
		}
	}
	landing, landingErr := url.Parse(c.LandingOrigin)
	if landingErr != nil || landing.Hostname() == "" || (landing.Scheme != "http" && landing.Scheme != "https") || landing.User != nil || landing.Path != "" || landing.RawQuery != "" || landing.ForceQuery || landing.Fragment != "" {
		return c, errors.New("LANDING_ORIGIN must be an exact http(s) origin")
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return c, errors.New("invalid SERVICE_PORT")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("ORIGIN must be an exact http(s) origin")
	}
	if (c.PostgresURI == "") == (c.PostgresURIFrom == "") {
		return c, errors.New("set exactly one PostgreSQL URI source")
	}
	c.ScanInterval, err = time.ParseDuration(interval)
	if err != nil || c.ScanInterval < time.Second {
		return c, errors.New("invalid SCAN_INTERVAL")
	}
	c.Workers, err = strconv.Atoi(workers)
	if err != nil || c.Workers < 1 || c.Workers > 32 {
		return c, errors.New("invalid INDEX_WORKERS")
	}
	c.MaxNoteBytes, err = strconv.ParseInt(noteLimit, 10, 64)
	if err != nil || c.MaxNoteBytes < 1 || c.MaxNoteBytes > 16<<20 {
		return c, errors.New("invalid MAX_NOTE_BYTES")
	}
	c.MaxAttachmentBytes, err = strconv.ParseInt(attachmentLimit, 10, 64)
	if err != nil || c.MaxAttachmentBytes < 1 || c.MaxAttachmentBytes > 1<<30 {
		return c, errors.New("invalid MAX_ATTACHMENT_BYTES")
	}

	return c, nil
}
func defaultValue(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (c Config) DatabaseURI(ctx context.Context) (string, error) {
	raw := c.PostgresURI
	if c.PostgresURIFrom != "" {
		u, e := url.Parse(c.PostgresURIFrom)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", errors.New("invalid broker URL")
		}
		client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for attempt := 0; attempt < 6; attempt++ {
			req, _ := http.NewRequestWithContext(ctx, "GET", c.PostgresURIFrom, nil)
			resp, e := client.Do(req)
			if e == nil {
				b, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
				resp.Body.Close()
				if resp.StatusCode == 200 && readErr == nil && len(b) <= 4096 {
					raw = strings.TrimSpace(string(b))
					break
				}
				if resp.StatusCode >= 300 && resp.StatusCode < 500 {
					return "", errors.New("broker rejected discovery")
				}
			}
			if attempt == 5 {
				return "", errors.New("broker unavailable")
			}
			timer := time.NewTimer(time.Duration(1<<attempt) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || u.User == nil || u.Path == "" {
		return "", errors.New("invalid PostgreSQL URI")
	}
	if c.PasswordFile != "" {
		b, e := os.ReadFile(c.PasswordFile)
		if e != nil {
			return "", errors.New("cannot read PostgreSQL password file")
		}
		u.User = url.UserPassword(u.User.Username(), strings.TrimRight(string(b), "\r\n"))
	}
	return u.String(), nil
}
