package outbound

import (
	"context"
	"errors"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/transport"
)

type relayTracker struct {
	signals []*extension.OutboundSignal
	relay   map[string]bool
}

func newRelayTracker() *relayTracker {
	return &relayTracker{relay: make(map[string]bool)}
}

func (t *relayTracker) SubmitOutboundSignal(signal *extension.OutboundSignal) {
	copied := *signal
	t.signals = append(t.signals, &copied)
}

func (t *relayTracker) MarkRelayEstablished(tag string) bool {
	if t.relay[tag] {
		return false
	}
	t.relay[tag] = true
	return true
}

func (t *relayTracker) RelayEstablished(tag string) bool {
	return t.relay[tag]
}

type staticReader struct {
	mb buf.MultiBuffer
}

func (r *staticReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb := r.mb
	r.mb = nil
	return mb, nil
}

type discardWriter struct{}

func (*discardWriter) WriteMultiBuffer(buf.MultiBuffer) error { return nil }

type errorWriter struct{ err error }

func (w *errorWriter) WriteMultiBuffer(buf.MultiBuffer) error { return w.err }

func TestNewRelayAwareLinkDoesNotMarkRelayOnRead(t *testing.T) {
	tracker := newRelayTracker()
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "proxy-a"}})
	ctx = session.TrackedOutboundSignal(ctx, tracker)
	link := newRelayAwareLink(ctx, &transport.Link{
		Reader: &staticReader{mb: buf.MultiBuffer{buf.FromBytes([]byte("hello"))}},
		Writer: &discardWriter{},
	})
	if _, err := link.Reader.ReadMultiBuffer(); err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if len(tracker.signals) != 0 {
		t.Fatalf("reading uplink data must not report relay success, got %d signals", len(tracker.signals))
	}
	if tracker.RelayEstablished("proxy-a") {
		t.Fatal("reading uplink data marked relay as established")
	}
}

func TestRelayAwareWriterMarksRelayEstablishedOnWrite(t *testing.T) {
	tracker := newRelayTracker()
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "proxy-a"}})
	ctx = session.TrackedOutboundSignal(ctx, tracker)
	writer := &relayAwareWriter{ctx: ctx, writer: &discardWriter{}}
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes([]byte("hello"))}); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if len(tracker.signals) != 1 {
		t.Fatalf("expected relay success signal, got %d", len(tracker.signals))
	}
	if tracker.signals[0].Kind != extension.OutboundSignalRelaySuccess {
		t.Fatalf("expected relay success, got %s", tracker.signals[0].Kind)
	}
}

func TestRelayAwareWriterDoesNotMarkRelayOnWriteFailure(t *testing.T) {
	tracker := newRelayTracker()
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "proxy-a"}})
	ctx = session.TrackedOutboundSignal(ctx, tracker)
	writeErr := errors.New("write failed")
	writer := &relayAwareWriter{ctx: ctx, writer: &errorWriter{err: writeErr}}
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes([]byte("hello"))}); !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}
	if len(tracker.signals) != 0 {
		t.Fatalf("failed write must not report relay success, got %d signals", len(tracker.signals))
	}
	if tracker.RelayEstablished("proxy-a") {
		t.Fatal("failed write marked relay as established")
	}
}
