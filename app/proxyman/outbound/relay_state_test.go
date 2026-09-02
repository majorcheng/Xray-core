package outbound

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/extension"
)

type relayTracker struct {
	signals []*extension.OutboundSignal
	relay   bool
}

func (t *relayTracker) SubmitOutboundSignal(signal *extension.OutboundSignal) {
	copied := *signal
	t.signals = append(t.signals, &copied)
}

func (t *relayTracker) MarkRelayEstablished() bool {
	if t.relay {
		return false
	}
	t.relay = true
	return true
}

func (t *relayTracker) RelayEstablished() bool {
	return t.relay
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

func TestRelayAwareReaderMarksRelayEstablishedOnData(t *testing.T) {
	tracker := &relayTracker{}
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "proxy-a"}})
	ctx = session.TrackedOutboundSignal(ctx, tracker)
	reader := &relayAwareReader{ctx: ctx, reader: &staticReader{mb: buf.MultiBuffer{buf.FromBytes([]byte("hello"))}}}
	if _, err := reader.ReadMultiBuffer(); err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if len(tracker.signals) != 1 {
		t.Fatalf("expected relay success signal, got %d", len(tracker.signals))
	}
	if tracker.signals[0].Kind != extension.OutboundSignalRelaySuccess {
		t.Fatalf("expected relay success, got %s", tracker.signals[0].Kind)
	}
}

func TestRelayAwareWriterMarksRelayEstablishedOnWrite(t *testing.T) {
	tracker := &relayTracker{}
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
