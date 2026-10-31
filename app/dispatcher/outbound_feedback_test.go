package dispatcher

import (
	"context"
	"errors"
	"testing"

	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/extension"
	"google.golang.org/protobuf/proto"
)

type captureObservatory struct {
	signals []*extension.OutboundSignal
}

func (c *captureObservatory) Start() error      { return nil }
func (c *captureObservatory) Close() error      { return nil }
func (c *captureObservatory) Type() interface{} { return extension.ObservatoryType() }

func (c *captureObservatory) GetObservation(context.Context) (proto.Message, error) { return nil, nil }

func (c *captureObservatory) ReportOutboundSignal(signal *extension.OutboundSignal) {
	copied := *signal
	c.signals = append(c.signals, &copied)
}

func TestNewOutboundDispatchContextReportsSignal(t *testing.T) {
	obs := &captureObservatory{}
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "proxy-a"}})
	ctx = newOutboundDispatchContext(ctx, obs, "proxy-a")
	session.SubmitOutboundSignalToOriginator(ctx, &extension.OutboundSignal{Kind: extension.OutboundSignalDialFailure, Reason: "boom"})
	if len(obs.signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(obs.signals))
	}
	if obs.signals[0].OutboundTag != "proxy-a" {
		t.Fatalf("expected outbound tag proxy-a, got %q", obs.signals[0].OutboundTag)
	}
	if obs.signals[0].Kind != extension.OutboundSignalDialFailure {
		t.Fatalf("expected dial failure signal, got %s", obs.signals[0].Kind)
	}
}

func TestNewOutboundDispatchContextWithoutReporterKeepsContext(t *testing.T) {
	ctx := context.Background()
	if got := newOutboundDispatchContext(ctx, nil, "proxy-a"); got != ctx {
		t.Fatal("expected original context when reporter is unavailable")
	}
}

func TestOutboundDispatchTrackerKeepsDialerProxyHopsIndependent(t *testing.T) {
	obs := &captureObservatory{}
	outerCtx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "outer"}})
	ctx := newOutboundDispatchContext(outerCtx, obs, "outer")
	innerCtx := session.ContextWithOutbounds(ctx, []*session.Outbound{{Tag: "outer"}, {Tag: "inner"}})

	session.NoteOutboundRelayEstablished(innerCtx)
	if !session.OutboundRelayEstablished(innerCtx) {
		t.Fatal("inner hop was not marked as relayed")
	}
	if session.OutboundRelayEstablished(ctx) {
		t.Fatal("inner hop marked outer hop as relayed")
	}
	if len(obs.signals) != 1 || obs.signals[0].OutboundTag != "inner" {
		t.Fatalf("expected one inner relay signal, got %+v", obs.signals)
	}

	session.NoteOutboundRelayEstablished(ctx)
	if !session.OutboundRelayEstablished(ctx) {
		t.Fatal("outer hop was not marked as relayed after its own write")
	}
	if len(obs.signals) != 2 || obs.signals[1].OutboundTag != "outer" {
		t.Fatalf("expected one outer relay signal, got %+v", obs.signals)
	}
}

func TestOuterHopFailureIsNotSuppressedByInnerRelay(t *testing.T) {
	obs := &captureObservatory{}
	outerCtx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Tag: "outer"}})
	ctx := newOutboundDispatchContext(outerCtx, obs, "outer")
	innerCtx := session.ContextWithOutbounds(ctx, []*session.Outbound{{Tag: "outer"}, {Tag: "inner"}})

	session.NoteOutboundRelayEstablished(innerCtx)
	session.SubmitOutboundPreRelayProxyFailureToOriginator(ctx, errors.New("outer handshake failed"))
	if len(obs.signals) != 2 {
		t.Fatalf("expected relay and outer failure signals, got %+v", obs.signals)
	}
	if obs.signals[1].OutboundTag != "outer" || obs.signals[1].Kind != extension.OutboundSignalPreRelayProxyFailure {
		t.Fatalf("unexpected outer failure signal: %+v", obs.signals[1])
	}
}
