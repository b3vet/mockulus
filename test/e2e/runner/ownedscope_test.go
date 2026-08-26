package main

import "testing"

func TestOwnedScope(t *testing.T) {
	for name, want := range map[string]bool{
		"t2-default": true, "t3-default": true, "t2-fast-clock": true, "t10-x": true,
		"_default": false, "orders": false, "t-default": false, "t2": false,
		"tx-default": false, "": false, "t2-": false,
	} {
		if got := ownedScope(name); got != want {
			t.Errorf("ownedScope(%q) = %v, want %v", name, got, want)
		}
	}
}
