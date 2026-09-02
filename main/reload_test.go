package main

import "testing"

func TestParseReloadScopesRejectsInboundAndOutbound(t *testing.T) {
	for _, input := range []string{"inbound", "outbound", "routing,inbound", "log,outbound"} {
		if _, err := parseReloadScopes([]string{input}); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestParseReloadScopesAcceptsSupportedModules(t *testing.T) {
	scopes, err := parseReloadScopes([]string{"routing,log"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scopes["routing"] || !scopes["log"] {
		t.Fatalf("unexpected scopes: %#v", scopes)
	}
}
