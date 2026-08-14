// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// profileEnv builds a lookupEnv over a fixed map.
func profileEnv(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := pairs[k]; return v, ok }
}

// writeProfileConfig writes a config file into the test's own directory.
func writeProfileConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mockulus.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLocalProfilePresetsAStandaloneSetup(t *testing.T) {
	cfg, err := Load("", profileEnv(map[string]string{"MOCKULUS_PROFILE": "local"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Store != StoreMemory {
		t.Errorf("store = %q, want %q", cfg.Store, StoreMemory)
	}
	if !cfg.JournalEnabled {
		t.Error("the journal should be on: a local suite that calls verify() is the point")
	}
}

// The profile sets defaults, not overrides. This is the property the two-pass
// load exists for, and the one that would quietly invert if the preset were
// applied after the file and the environment.
func TestExplicitKeysBeatTheProfile(t *testing.T) {
	t.Run("the environment wins", func(t *testing.T) {
		cfg, err := Load("", profileEnv(map[string]string{
			"MOCKULUS_PROFILE":         "local",
			"MOCKULUS_JOURNAL_ENABLED": "false",
		}))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.JournalEnabled {
			t.Error("an explicit journal_enabled: false must beat the profile that turns it on")
		}
		// …and the key the operator did not write still comes from the profile.
		if cfg.Store != StoreMemory {
			t.Errorf("store = %q, want the profile's %q", cfg.Store, StoreMemory)
		}
	})

	t.Run("the file wins", func(t *testing.T) {
		path := writeProfileConfig(t, "profile: local\nstore: file\nfile:\n  root: /tmp/x\n")
		cfg, err := Load(path, profileEnv(nil))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Store != StoreFile {
			t.Errorf("store = %q, want the file's %q", cfg.Store, StoreFile)
		}
		if !cfg.JournalEnabled {
			t.Error("the profile still supplies the key the file did not write")
		}
	})
}

// An unknown profile is refused rather than applying nothing, which would start
// a deployment configured as though the operator had asked for something.
func TestUnknownProfileIsRefused(t *testing.T) {
	if _, err := Load("", profileEnv(map[string]string{"MOCKULUS_PROFILE": "prod"})); err == nil {
		t.Fatal("an unknown profile must be an error")
	}
}

// No profile changes nothing: the defaults are exactly what they were.
func TestNoProfileChangesNothing(t *testing.T) {
	cfg, err := Load("", profileEnv(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	base := Default()
	if cfg.Store != base.Store || cfg.JournalEnabled != base.JournalEnabled {
		t.Errorf("an empty profile moved a default: store=%q journal=%v", cfg.Store, cfg.JournalEnabled)
	}
}
