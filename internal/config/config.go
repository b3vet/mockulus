// SPDX-License-Identifier: Apache-2.0

// Package config defines mockulus' typed configuration together with its
// binding from environment variables and an optional YAML file, its
// validation, and the generator behind the reference table in SPEC.md §13.
//
// Precedence is env var > YAML file > default (SPEC §13). Struct tags are the
// single source of truth: `yaml` names the key, `default` its default value,
// `doc` its description, and the same tags drive `make config-docs`, so the
// spec table and the code cannot drift.
//
// Struct tag values go through Go's unquoting rules, so two characters stand in
// for Markdown that cannot be written there directly: `~` becomes a backtick,
// and `¦` becomes a pipe escaped for a table cell.
package config

import (
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
)

// EnvPrefix is prepended to every configuration key's environment variable
// name; the rest of the name is the upper-snake form of the YAML path
// (`couchbase.connstr` -> `MOCKULUS_COUCHBASE_CONNSTR`).
const EnvPrefix = "MOCKULUS_"

// FileEnvVar names the environment variable holding a path to the optional
// YAML configuration file; the `--config` flag takes precedence over it.
const FileEnvVar = EnvPrefix + "CONFIG"

// Config is the complete configuration surface of a mockulus instance.
// Field order determines the row order of the generated SPEC §13 table.
type Config struct {
	Port            int    `yaml:"port" default:"8080" doc:"Mock listener (~0~ binds an ephemeral port)"`
	AdminPort       int    `yaml:"admin_port" default:"9090" doc:"Admin/ops listener (~0~ binds an ephemeral port)"`
	AdminOnMockPort bool   `yaml:"admin_on_mock_port" default:"true" doc:"Serve ~/__admin~ on the mock port (compat)"`
	Store           string `yaml:"store" default:"auto" doc:"~auto~ (couchbase if connstr set, else memory) ¦ ~couchbase~ ¦ ~memory~ ¦ ~file~"`

	Couchbase CouchbaseConfig `yaml:"couchbase"`

	ScenarioKVTimeout Duration `yaml:"scenario_kv_timeout" default:"250ms" doc:"Budget for scenario reads/CAS on the request path"`

	File FileConfig `yaml:"file"`

	SyncInterval      Duration `yaml:"sync_interval" default:"1s" doc:"Epoch poll interval (min ~100ms~)"`
	ResyncInterval    Duration `yaml:"resync_interval" default:"5m" doc:"Unconditional full reload (expiry sweep, self-heal)"`
	EphemeralStubTTL  Duration `yaml:"ephemeral_stub_ttl" default:"24h" doc:"TTL for ~persistent:false~ stubs (~0~ = none)"`
	StartWithoutStore bool     `yaml:"start_without_store" default:"false" doc:"Become ready with empty snapshot if store is down at boot"`

	JournalEnabled        bool     `yaml:"journal_enabled" default:"false" doc:"Master switch"`
	JournalTTL            Duration `yaml:"journal_ttl" default:"30m" doc:"Entry TTL"`
	JournalMaxBody        Bytes    `yaml:"journal_max_body" default:"64KiB" doc:"Per-entry stored body cap"`
	JournalBuffer         int      `yaml:"journal_buffer" default:"8192" docrow:"journal_buffer|journal_buffer_bytes" doc:"Queue caps — entry count and total bytes, whichever first"`
	JournalBufferBytes    Bytes    `yaml:"journal_buffer_bytes" default:"64MiB" docrow:"journal_buffer|journal_buffer_bytes"`
	JournalFlushWorkers   int      `yaml:"journal_flush_workers" default:"4" docrow:"journal_flush_workers|journal_batch_size|journal_flush_interval" doc:"Writer tuning (bulk KV)"`
	JournalBatchSize      int      `yaml:"journal_batch_size" default:"500" docrow:"journal_flush_workers|journal_batch_size|journal_flush_interval"`
	JournalFlushInterval  Duration `yaml:"journal_flush_interval" default:"200ms" docrow:"journal_flush_workers|journal_batch_size|journal_flush_interval"`
	JournalQueryScanLimit int      `yaml:"journal_query_scan_limit" default:"10000" doc:"Criteria-query scan guard"`

	TemplatingEnabled      string `yaml:"templating_enabled" default:"wm-compat" doc:"~wm-compat~ (mirror pinned WM activation, §10.1) ¦ ~on~ (force global) ¦ ~off~"`
	TemplateMaxOutputBytes Bytes  `yaml:"template_max_output_bytes" default:"10MiB" doc:""`

	MaxBodyBytes Bytes    `yaml:"max_body_bytes" default:"10MiB" doc:"Request body cap (~0~ = unbounded)"`
	RegexTimeout Duration `yaml:"regex_timeout" default:"100ms" doc:"regexp2 fallback match timeout"`

	DiagnosticsOnUnmatched bool `yaml:"diagnostics_on_unmatched" default:"false" doc:"Near-miss detail in 404s"`

	AdminAuthToken       string `yaml:"admin_auth_token" default:"" secret:"true" doc:"If set, admin API requires ~Authorization: Token <t>~ (§17)"`
	AdminShutdownEnabled bool   `yaml:"admin_shutdown_enabled" default:"false" doc:"Enable ~POST /__admin/shutdown~"`

	TLSCertFile string `yaml:"tls_cert_file" default:"" docrow:"tls_cert_file|tls_key_file" doc:"Enable TLS on mock port"`
	TLSKeyFile  string `yaml:"tls_key_file" default:"" docrow:"tls_cert_file|tls_key_file"`

	H2CEnabled bool     `yaml:"h2c_enabled" default:"false" doc:"Cleartext HTTP/2 on mock port (off by default — fault fidelity, §12.5)"`
	WriteSlack Duration `yaml:"write_slack" default:"10s" doc:"Mock-port per-response write deadline = configured delay + this slack"`

	ShutdownDrain   Duration `yaml:"shutdown_drain" default:"5s" docrow:"shutdown_drain|shutdown_timeout" doc:"§4.5"`
	ShutdownTimeout Duration `yaml:"shutdown_timeout" default:"15s" docrow:"shutdown_drain|shutdown_timeout"`

	Log LogConfig `yaml:"log"`

	MetricsEnabled bool `yaml:"metrics_enabled" default:"true" doc:""`
}

// CouchbaseConfig holds the settings of the `couchbase` store driver (SPEC §7.2).
type CouchbaseConfig struct {
	ConnStr      string   `yaml:"connstr" default:"" doc:"e.g. ~couchbase://cb.mockulus.svc~"`
	Username     string   `yaml:"username" default:"" docrow:"couchbase.username|couchbase.password" doc:"Password also via ~_FILE~ variant for mounted secrets"`
	Password     string   `yaml:"password" default:"" secret:"true" docrow:"couchbase.username|couchbase.password"`
	Bucket       string   `yaml:"bucket" default:"mockulus" docrow:"couchbase.bucket|couchbase.scope" doc:""`
	Scope        string   `yaml:"scope" default:"_default" docrow:"couchbase.bucket|couchbase.scope"`
	Durability   string   `yaml:"durability" default:"none" doc:"~none~ ¦ ~majority~"`
	ManageBucket bool     `yaml:"manage_bucket" default:"true" doc:"Auto-create collections/indexes at boot"`
	KVTimeout    Duration `yaml:"kv_timeout" default:"2500ms" docrow:"couchbase.kv_timeout|couchbase.query_timeout" doc:""`
	QueryTimeout Duration `yaml:"query_timeout" default:"10s" docrow:"couchbase.kv_timeout|couchbase.query_timeout"`
}

// FileConfig holds the settings of the `file` store driver (SPEC §7.1).
type FileConfig struct {
	Root string `yaml:"root" default:"" doc:"~file~ store: dir containing ~mappings/~ and ~__files/~"`
}

// LogConfig holds logging settings (SPEC §14.2).
type LogConfig struct {
	Level          string `yaml:"level" default:"info" docrow:"log.level|log.format" doc:"~text~ for local dev"`
	Format         string `yaml:"format" default:"json" docrow:"log.level|log.format"`
	Requests       bool   `yaml:"requests" default:"false" doc:"Per-request access logs (hot path — keep off under load)"`
	RequestSampleN int    `yaml:"request_sample_n" default:"100" doc:"With ~log.requests~, log every Nth request"`
}

// Store driver names accepted by the `store` key.
const (
	StoreAuto      = "auto"
	StoreCouchbase = "couchbase"
	StoreMemory    = "memory"
	StoreFile      = "file"
)

// Templating activation modes accepted by the `templating_enabled` key (SPEC §10.1).
const (
	TemplatingWMCompat = "wm-compat"
	TemplatingOn       = "on"
	TemplatingOff      = "off"
)

// minSyncInterval is the floor documented for `sync_interval` in SPEC §13.
const minSyncInterval = 100_000_000 // 100ms

// Default returns a Config with every key at its documented default.
func Default() Config {
	var c Config
	if err := applyDefaults(&c); err != nil {
		// Defaults come from struct tags in this file; a failure here is a
		// programming error, not a user error.
		panic("config: invalid default tags: " + err.Error())
	}
	return c
}

// Load resolves configuration from defaults, an optional YAML file and the
// environment, in that precedence order, then validates the result.
//
// configPath is the value of the `--config` flag; when empty the MOCKULUS_CONFIG
// environment variable is consulted. lookupEnv is the environment source
// (os.LookupEnv in production, a fake in tests).
func Load(configPath string, lookupEnv func(string) (string, bool)) (Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	cfg := Default()

	if configPath == "" {
		if v, ok := lookupEnv(FileEnvVar); ok {
			configPath = v
		}
	}
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return Config{}, fmt.Errorf("read config file: %w", err)
		}
		doc, err := parseYAML(string(data))
		if err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", configPath, err)
		}
		if err := applyYAML(&cfg, doc); err != nil {
			return Config{}, fmt.Errorf("config file %s: %w", configPath, err)
		}
	}

	if err := applyEnv(&cfg, lookupEnv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// EffectiveStore resolves the `auto` store setting to a concrete driver name.
func (c Config) EffectiveStore() string {
	if c.Store != StoreAuto {
		return c.Store
	}
	if c.Couchbase.ConnStr != "" {
		return StoreCouchbase
	}
	return StoreMemory
}

// TLSEnabled reports whether the mock listener should serve TLS.
func (c Config) TLSEnabled() bool { return c.TLSCertFile != "" && c.TLSKeyFile != "" }

// AdminTokenAccepted reports whether an Authorization header satisfies
// `admin_auth_token`. It answers false for every request when no token is
// configured, so a caller must check AdminAuthToken first to keep the default
// open posture of SPEC §17.
//
// The comparison lives beside the key rather than in the handler because two
// listeners guard themselves with it — the admin API and the profiling
// endpoints of §14.3 — and a second copy is a second chance for one of them to
// drift into a byte-by-byte compare that leaks the token one character at a
// time.
func (c Config) AdminTokenAccepted(authorization string) bool {
	want := []byte("Token " + c.AdminAuthToken)
	return subtle.ConstantTimeCompare([]byte(authorization), want) == 1
}

// Validate reports every problem with the resolved configuration at once, so a
// misconfigured deployment does not need one restart per mistake.
func (c Config) Validate() error {
	var problems []string
	bad := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// Port 0 asks the OS for a free port, which is what lets many instances run
	// side by side on one machine.
	validPort := func(key string, p int) {
		if p < 0 || p > 65535 {
			bad("%s: %d is not a valid TCP port (0-65535, 0 = ephemeral)", key, p)
		}
	}
	validPort("port", c.Port)
	validPort("admin_port", c.AdminPort)
	if c.Port == c.AdminPort && c.Port != 0 {
		bad("port and admin_port must differ (both %d)", c.Port)
	}

	oneOf := func(key, got string, allowed ...string) {
		for _, a := range allowed {
			if got == a {
				return
			}
		}
		bad("%s: %q is not one of %s", key, got, strings.Join(allowed, ", "))
	}
	oneOf("store", c.Store, StoreAuto, StoreCouchbase, StoreMemory, StoreFile)
	oneOf("templating_enabled", c.TemplatingEnabled, TemplatingWMCompat, TemplatingOn, TemplatingOff)
	oneOf("couchbase.durability", c.Couchbase.Durability, "none", "majority")
	oneOf("log.level", c.Log.Level, "debug", "info", "warn", "error")
	oneOf("log.format", c.Log.Format, "json", "text")

	if c.Store == StoreCouchbase && c.Couchbase.ConnStr == "" {
		bad("store: couchbase requires couchbase.connstr")
	}
	if c.Store == StoreFile && c.File.Root == "" {
		bad("store: file requires file.root")
	}

	if c.SyncInterval.D() < minSyncInterval {
		bad("sync_interval: %s is below the %s minimum", c.SyncInterval, Duration(minSyncInterval))
	}
	if c.ResyncInterval.D() <= 0 {
		bad("resync_interval: must be positive")
	}
	if c.EphemeralStubTTL.D() < 0 {
		bad("ephemeral_stub_ttl: must not be negative (0 disables the TTL)")
	}
	if c.ScenarioKVTimeout.D() <= 0 {
		bad("scenario_kv_timeout: must be positive")
	}
	if c.RegexTimeout.D() <= 0 {
		bad("regex_timeout: must be positive")
	}
	if c.WriteSlack.D() < 0 {
		bad("write_slack: must not be negative")
	}
	if c.ShutdownDrain.D() < 0 {
		bad("shutdown_drain: must not be negative")
	}
	if c.ShutdownTimeout.D() <= 0 {
		bad("shutdown_timeout: must be positive")
	}
	if c.Couchbase.KVTimeout.D() <= 0 {
		bad("couchbase.kv_timeout: must be positive")
	}
	if c.Couchbase.QueryTimeout.D() <= 0 {
		bad("couchbase.query_timeout: must be positive")
	}

	positive := func(key string, v int) {
		if v <= 0 {
			bad("%s: must be positive", key)
		}
	}
	positive("journal_buffer", c.JournalBuffer)
	positive("journal_flush_workers", c.JournalFlushWorkers)
	positive("journal_batch_size", c.JournalBatchSize)
	positive("journal_query_scan_limit", c.JournalQueryScanLimit)
	positive("log.request_sample_n", c.Log.RequestSampleN)
	if c.JournalFlushInterval.D() <= 0 {
		bad("journal_flush_interval: must be positive")
	}
	if c.JournalTTL.D() < 0 {
		bad("journal_ttl: must not be negative")
	}
	if c.JournalBufferBytes.B() <= 0 {
		bad("journal_buffer_bytes: must be positive")
	}
	if c.JournalMaxBody.B() < 0 {
		bad("journal_max_body: must not be negative")
	}
	if c.MaxBodyBytes.B() < 0 {
		bad("max_body_bytes: must not be negative (0 disables the cap)")
	}
	if c.TemplateMaxOutputBytes.B() <= 0 {
		bad("template_max_output_bytes: must be positive")
	}

	// TLS is checked here, against the filesystem, rather than left to the
	// listener. ServeTLS loads the pair on its own goroutine, long after the
	// process has bound its ports and reported itself ready: a typo in a mounted
	// path would leave a pod that is live, ready and serving nothing on the mock
	// port, which is the worst of the available failures because Kubernetes
	// would route traffic straight at it. Exiting 1 at load time is the contract
	// (SPEC §4.4 step 1).
	switch {
	case (c.TLSCertFile == "") != (c.TLSKeyFile == ""):
		bad("tls_cert_file and tls_key_file must be set together")
	case c.TLSEnabled():
		if _, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile); err != nil {
			bad("tls_cert_file/tls_key_file: %v", err)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
}
