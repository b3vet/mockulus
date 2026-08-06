// SPDX-License-Identifier: Apache-2.0

package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// updateSpec regenerates the SPEC §13 table instead of asserting on it;
// `make config-docs` runs this test with the flag set.
var updateSpec = flag.Bool("update", false, "rewrite the SPEC.md §13 table from the config struct")

func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

func TestDefaultsMatchSpec(t *testing.T) {
	c := Default()

	if c.Port != 8080 || c.AdminPort != 9090 {
		t.Errorf("ports = %d/%d, want 8080/9090", c.Port, c.AdminPort)
	}
	if !c.AdminOnMockPort {
		t.Error("admin_on_mock_port should default to true")
	}
	if c.Store != StoreAuto {
		t.Errorf("store = %q, want auto", c.Store)
	}
	if c.SyncInterval.D() != time.Second {
		t.Errorf("sync_interval = %s, want 1s", c.SyncInterval)
	}
	if c.ResyncInterval.D() != 5*time.Minute {
		t.Errorf("resync_interval = %s, want 5m", c.ResyncInterval)
	}
	if c.EphemeralStubTTL.D() != 24*time.Hour {
		t.Errorf("ephemeral_stub_ttl = %s, want 24h", c.EphemeralStubTTL)
	}
	if c.JournalEnabled {
		t.Error("journal must be off by default (deviation #1)")
	}
	if c.DiagnosticsOnUnmatched {
		t.Error("unmatched diagnostics must be off by default (deviation #2)")
	}
	if c.H2CEnabled {
		t.Error("h2c must be off by default (deviation #15)")
	}
	if c.AdminShutdownEnabled {
		t.Error("admin shutdown must be off by default (deviation #8)")
	}
	if c.MaxBodyBytes.B() != 10<<20 {
		t.Errorf("max_body_bytes = %s, want 10MiB", c.MaxBodyBytes)
	}
	if c.JournalMaxBody.B() != 64<<10 {
		t.Errorf("journal_max_body = %s, want 64KiB", c.JournalMaxBody)
	}
	if c.JournalBufferBytes.B() != 64<<20 {
		t.Errorf("journal_buffer_bytes = %s, want 64MiB", c.JournalBufferBytes)
	}
	if c.TemplatingEnabled != TemplatingWMCompat {
		t.Errorf("templating_enabled = %q, want wm-compat", c.TemplatingEnabled)
	}
	if c.Couchbase.Bucket != "mockulus" || c.Couchbase.Scope != "_default" {
		t.Errorf("couchbase keyspace = %s/%s, want mockulus/_default", c.Couchbase.Bucket, c.Couchbase.Scope)
	}
	if !c.MetricsEnabled {
		t.Error("metrics should default to enabled")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestEnvOverridesFileOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mockulus.yaml")
	body := strings.Join([]string{
		"# mockulus configuration",
		"port: 9999",
		"journal_enabled: true",
		"couchbase:",
		"  connstr: couchbase://cb.example",
		"  bucket: from-file",
		"log:",
		"  level: debug",
		"  format: text",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path, env(map[string]string{
		"MOCKULUS_COUCHBASE_BUCKET": "from-env",
		"MOCKULUS_SYNC_INTERVAL":    "250ms",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Port != 9999 {
		t.Errorf("port = %d, want 9999 (from file)", cfg.Port)
	}
	if !cfg.JournalEnabled {
		t.Error("journal_enabled should come from the file")
	}
	if cfg.Couchbase.Bucket != "from-env" {
		t.Errorf("bucket = %q, want from-env (env beats file)", cfg.Couchbase.Bucket)
	}
	if cfg.Couchbase.ConnStr != "couchbase://cb.example" {
		t.Errorf("connstr = %q", cfg.Couchbase.ConnStr)
	}
	if cfg.SyncInterval.D() != 250*time.Millisecond {
		t.Errorf("sync_interval = %s, want 250ms (from env)", cfg.SyncInterval)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("log = %s/%s, want debug/text", cfg.Log.Level, cfg.Log.Format)
	}
	if cfg.EffectiveStore() != StoreCouchbase {
		t.Errorf("store auto with a connstr should resolve to couchbase, got %s", cfg.EffectiveStore())
	}
	if cfg.AdminPort != 9090 {
		t.Errorf("untouched key should keep its default, got %d", cfg.AdminPort)
	}
}

func TestConfigFileEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("port: 7777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", env(map[string]string{FileEnvVar: path}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 7777 {
		t.Errorf("port = %d, want 7777", cfg.Port)
	}
}

func TestSecretFileVariant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "password")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", env(map[string]string{"MOCKULUS_COUCHBASE_PASSWORD_FILE": path}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Couchbase.Password != "s3cret" {
		t.Errorf("password = %q, want s3cret (trailing newline trimmed)", cfg.Couchbase.Password)
	}
}

func TestDumpRedactsSecrets(t *testing.T) {
	cfg := Default()
	cfg.Couchbase.Password = "hunter2"
	cfg.AdminAuthToken = "t0ken"
	cfg.Couchbase.Username = "admin"

	dump := strings.Join(cfg.Dump(), "\n")
	if strings.Contains(dump, "hunter2") || strings.Contains(dump, "t0ken") {
		t.Fatalf("secrets leaked into config dump:\n%s", dump)
	}
	if !strings.Contains(dump, "couchbase.password=[redacted]") {
		t.Errorf("password should be redacted, got:\n%s", dump)
	}
	if !strings.Contains(dump, "couchbase.username=admin") {
		t.Errorf("non-secret values should be dumped verbatim, got:\n%s", dump)
	}
}

func TestValidationCollectsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.Port = 70000
	cfg.Store = "postgres"
	cfg.Log.Level = "loud"
	cfg.SyncInterval = Duration(10 * time.Millisecond)
	cfg.TLSCertFile = "/tmp/cert.pem"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	for _, want := range []string{"port", "store", "log.level", "sync_interval", "tls_key_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validation error should mention %q, got:\n%v", want, err)
		}
	}
}

func TestUnknownKeysFailLoudly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("port: 8080\nturbo_mode: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, env(nil))
	if err == nil || !strings.Contains(err.Error(), "turbo_mode") {
		t.Fatalf("unknown key must be rejected by name, got %v", err)
	}
}

func TestStoreValidation(t *testing.T) {
	cfg := Default()
	cfg.Store = StoreCouchbase
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "connstr") {
		t.Errorf("couchbase store without a connstr must fail, got %v", err)
	}
	cfg = Default()
	cfg.Store = StoreFile
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "file.root") {
		t.Errorf("file store without a root must fail, got %v", err)
	}
	cfg = Default()
	if cfg.EffectiveStore() != StoreMemory {
		t.Errorf("auto without a connstr should resolve to memory, got %s", cfg.EffectiveStore())
	}
}

func TestBytesRoundTrip(t *testing.T) {
	cases := map[string]int64{
		"0":     0,
		"8192":  8192,
		"64KiB": 64 << 10,
		"10MiB": 10 << 20,
		"1GiB":  1 << 30,
		"512B":  512,
	}
	for in, want := range cases {
		var b Bytes
		if err := b.parse(in); err != nil {
			t.Errorf("parse(%q): %v", in, err)
			continue
		}
		if b.B() != want {
			t.Errorf("parse(%q) = %d, want %d", in, b.B(), want)
		}
	}
	for _, in := range []string{"", "10 gigs", "-5", "MiB", "1.5MiB"} {
		var b Bytes
		if err := b.parse(in); err == nil {
			t.Errorf("parse(%q) should fail, got %d", in, b.B())
		}
	}
	if got := Bytes(10 << 20).String(); got != "10MiB" {
		t.Errorf("Bytes.String() = %q, want 10MiB", got)
	}
}

func TestEnvNamesFollowThePrefixRule(t *testing.T) {
	want := map[string]string{
		"port":                 "MOCKULUS_PORT",
		"couchbase.connstr":    "MOCKULUS_COUCHBASE_CONNSTR",
		"log.request_sample_n": "MOCKULUS_LOG_REQUEST_SAMPLE_N",
		"scenario_kv_timeout":  "MOCKULUS_SCENARIO_KV_TIMEOUT",
	}
	got := map[string]string{}
	for _, f := range fields() {
		got[f.Path] = f.Env
	}
	for path, envName := range want {
		if got[path] != envName {
			t.Errorf("%s -> %q, want %q", path, got[path], envName)
		}
	}
}

// TestSpecConfigTable is the drift gate of SPEC §13: the table in the spec must
// be exactly what the config struct generates. Run with -update to regenerate.
func TestSpecConfigTable(t *testing.T) {
	spec := filepath.Join("..", "..", "SPEC.md")
	if *updateSpec {
		changed, err := UpdateSpec(spec)
		if err != nil {
			t.Fatalf("UpdateSpec: %v", err)
		}
		if changed {
			t.Log("SPEC.md §13 table regenerated")
		}
		return
	}
	if err := CheckSpec(spec); err != nil {
		t.Fatal(err)
	}
}

func TestEphemeralPortsAreValid(t *testing.T) {
	cfg := Default()
	cfg.Port = 0
	cfg.AdminPort = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("port 0 must be accepted so instances can bind ephemeral ports: %v", err)
	}
}

func TestTracingValidation(t *testing.T) {
	// Enabled and aimed at nothing: the exporter would build spans and drop
	// every one, with a counter nobody has reason to read as the only evidence.
	cfg := Default()
	cfg.Tracing.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tracing.endpoint") {
		t.Errorf("tracing without an endpoint must fail by name, got %v", err)
	}

	// `tracing.insecure` already answers the scheme question, so an endpoint
	// carrying one is two answers to it. Refused rather than reinterpreted (P3).
	cfg = Default()
	cfg.Tracing.Enabled = true
	cfg.Tracing.Endpoint = "https://collector:4318"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tracing.endpoint") {
		t.Errorf("an endpoint carrying a scheme must be refused, got %v", err)
	}

	cfg = Default()
	cfg.Tracing.Enabled = true
	cfg.Tracing.Endpoint = "collector:4318"
	if err := cfg.Validate(); err != nil {
		t.Errorf("a host:port endpoint should validate, got %v", err)
	}

	// The ratio is checked whether or not tracing is on, so a deployment that
	// turns it on later is not surprised by a value it has been carrying.
	for _, ratio := range []float64{-0.1, 1.5} {
		cfg = Default()
		cfg.Tracing.SampleRatio = ratio
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tracing.sample_ratio") {
			t.Errorf("sample ratio %v must be refused, got %v", ratio, err)
		}
	}

	cfg = Default()
	cfg.Tracing.Headers = "not-a-pair"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tracing.headers") {
		t.Errorf("a malformed header list must be refused, got %v", err)
	}
}

func TestTracingHeadersParse(t *testing.T) {
	cfg := Default()
	cfg.Tracing.Headers = " x-scope-orgid=checkout , authorization=Bearer tok "
	got, err := cfg.Tracing.ParsedHeaders()
	if err != nil {
		t.Fatalf("parse headers: %v", err)
	}
	if got["x-scope-orgid"] != "checkout" {
		t.Errorf("x-scope-orgid = %q, want checkout", got["x-scope-orgid"])
	}
	// A value may contain spaces; only the surrounding ones are trimmed.
	if got["authorization"] != "Bearer tok" {
		t.Errorf("authorization = %q, want %q", got["authorization"], "Bearer tok")
	}

	cfg = Default()
	if h, err := cfg.Tracing.ParsedHeaders(); err != nil || h != nil {
		t.Errorf("an unset header list should parse to nothing, got %v, %v", h, err)
	}
}

func TestTracingSampleRatioBindsAsANumber(t *testing.T) {
	// float64 is the one field kind the binder grew for tracing; a key that
	// silently failed to bind would leave the documented default in place and
	// look like it had worked.
	cfg, err := Load("", env(map[string]string{"MOCKULUS_TRACING_SAMPLE_RATIO": "0.25"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Tracing.SampleRatio != 0.25 {
		t.Errorf("sample_ratio = %v, want 0.25", cfg.Tracing.SampleRatio)
	}

	if _, err := Load("", env(map[string]string{"MOCKULUS_TRACING_SAMPLE_RATIO": "half"})); err == nil {
		t.Error("a non-numeric sample ratio must be refused rather than left at the default")
	}
}

func TestTracingHeadersAreRedactedInTheDump(t *testing.T) {
	cfg := Default()
	cfg.Tracing.Headers = "authorization=Bearer supersecret"
	for _, line := range cfg.Dump() {
		if strings.HasPrefix(line, "tracing.headers=") && strings.Contains(line, "supersecret") {
			t.Errorf("the startup dump printed an ingestion token: %s", line)
		}
	}
}
