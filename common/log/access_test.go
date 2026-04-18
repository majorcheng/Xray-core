package log

import (
	"net"
	"strings"
	"testing"
)

func TestAccessMessageStringWithDelay(t *testing.T) {
	msg := &AccessMessage{
		From:   "1.1.1.1:1234",
		To:     "example.com:443",
		Status: AccessAccepted,
		Delay:  123,
	}

	out := msg.String()
	if !strings.Contains(out, "delay: 123") {
		t.Fatalf("expected delay in access log, got %q", out)
	}
}

func TestAccessMessageStringWithoutDelay(t *testing.T) {
	msg := &AccessMessage{
		From:   "1.1.1.1:1234",
		To:     "example.com:443",
		Status: AccessAccepted,
	}

	out := msg.String()
	if strings.Contains(out, "delay:") {
		t.Fatalf("unexpected delay in access log, got %q", out)
	}
}

func TestAccessMessageStringWithEgress(t *testing.T) {
	msg := &AccessMessage{
		From:   "1.1.1.1:1234",
		To:     "example.com:443",
		Status: AccessAccepted,
		Egress: "v6",
	}

	out := msg.String()
	if !strings.Contains(out, "egress: v6") {
		t.Fatalf("expected egress in access log, got %q", out)
	}
}

func TestAccessMessageSetEgressFromAddr(t *testing.T) {
	msg := &AccessMessage{}
	if ok := msg.SetEgressFromAddr(&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 443}); !ok {
		t.Fatal("expected ipv6 addr to set egress")
	}
	if msg.Egress != "v6" {
		t.Fatalf("unexpected egress %q", msg.Egress)
	}

	msg = &AccessMessage{}
	if ok := msg.SetEgressFromAddr(&net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 53}); !ok {
		t.Fatal("expected ipv4 addr to set egress")
	}
	if msg.Egress != "v4" {
		t.Fatalf("unexpected egress %q", msg.Egress)
	}
}

func TestAccessMessageMarkLoggedOnlyOnce(t *testing.T) {
	msg := &AccessMessage{}
	if !msg.MarkLogged() {
		t.Fatal("expected first mark to succeed")
	}
	if msg.MarkLogged() {
		t.Fatal("expected second mark to be ignored")
	}
}
