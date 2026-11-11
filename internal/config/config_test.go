package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func values() map[string]string {
	return map[string]string{"POSTGRES_URI": "postgres://notes@localhost/notes"}
}
func load(m map[string]string, args ...string) (Config, error) {
	return Load(args, func(k string) string { return m[k] })
}
func TestConfig(t *testing.T) {
	c, e := load(values())
	if e != nil || c.Workers != 4 || c.ScanInterval.String() != "5m0s" {
		t.Fatal(c, e)
	}
	for key, value := range map[string]string{"LANDING_ORIGIN": "https://home.example/path", "SERVICE_PORT": "zero", "ORIGIN": "https://notes/path", "SCAN_INTERVAL": "0s", "INDEX_WORKERS": "100", "MAX_NOTE_BYTES": "0", "MAX_ATTACHMENT_BYTES": "-1", "S3_ALLOW_HTTP": "invalid", "POSTGRES_URI_FROM": "http://broker/uri"} {
		m := values()
		m[key] = value
		if _, e := load(m); e == nil {
			t.Fatal("accepted", key, value)
		}
	}
	c, e = load(values(), "--port", "9191")
	if e != nil || c.Port != "9191" {
		t.Fatal(c, e)
	}
	for _, args := range [][]string{{"--unknown"}, {"extra"}, {"--port", "0"}} {
		if _, e := load(values(), args...); e == nil {
			t.Fatal(args)
		}
	}
	// Storage credentials are entered in the UI; none are needed to start.
	if _, err := load(values(), "--s3-bucket", "obsolete"); err == nil {
		t.Fatal("obsolete operator bucket flag accepted")
	}

}
func TestDiscovery(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("postgres://notes:password@localhost/notes\n"))
	}))
	defer s.Close()
	m := values()
	m["POSTGRES_URI"] = ""
	m["POSTGRES_URI_FROM"] = s.URL
	c, e := load(m)
	if e != nil {
		t.Fatal(e)
	}
	uri, e := c.DatabaseURI(context.Background())
	if e != nil || !strings.Contains(uri, "notes:password@") {
		t.Fatal(uri, e)
	}
}
