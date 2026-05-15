package main

import "testing"

func TestEnvOrFallsBackWhenUnset(t *testing.T) {
	t.Setenv("TEST_KEY_UNSET", "")
	got := envOr("TEST_KEY_UNSET", "fallback")
	if got != "fallback" {
		t.Fatalf("got %q, want %q", got, "fallback")
	}
}

func TestEnvOrReturnsValueWhenSet(t *testing.T) {
	t.Setenv("TEST_KEY_SET", "value")
	got := envOr("TEST_KEY_SET", "fallback")
	if got != "value" {
		t.Fatalf("got %q, want %q", got, "value")
	}
}
