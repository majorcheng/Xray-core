package main

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func TestFormatAPIResultMarksFailure(t *testing.T) {
	result := formatAPIResult("routing", status.Error(codes.Unavailable, "backend down"))
	if result.ok {
		t.Fatal("API failure must not be reported as success")
	}
	if !strings.Contains(result.message, "failed") || !strings.Contains(result.message, "backend down") {
		t.Fatalf("unexpected failure message: %q", result.message)
	}
}

func TestFormatAPIResultTreatsUnimplementedAsSkipped(t *testing.T) {
	result := formatAPIResult("log", status.Error(codes.Unimplemented, "service disabled"))
	if !result.ok {
		t.Fatal("unimplemented service should be skipped, not failed")
	}
	if !strings.Contains(result.message, "skipped") {
		t.Fatalf("unexpected skipped message: %q", result.message)
	}
}
