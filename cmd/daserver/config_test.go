package main

import "testing"

func TestDefaultP2PNetwork(t *testing.T) {
	if got := DefaultConfig().Celestia.P2PNetwork; got != "mocha-5" {
		t.Fatalf("expected default p2p network %q, got %q", "mocha-5", got)
	}
	if got := CelestiaP2PNetworkFlag.Value; got != "mocha-5" {
		t.Fatalf("expected default p2p network flag %q, got %q", "mocha-5", got)
	}
}
