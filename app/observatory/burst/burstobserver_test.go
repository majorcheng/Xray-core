package burst

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/outbound"
)

type staticOutboundSelector struct {
	outbound.Manager
	selected []string
}

func (s *staticOutboundSelector) Select([]string) []string {
	return append([]string(nil), s.selected...)
}

func newMonitoredBurstObserver() *Observer {
	return &Observer{
		config:  &Config{SubjectSelector: []string{"proxy-"}},
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		ohm:     &staticOutboundSelector{selected: []string{"proxy-a"}},
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}
}

func TestBurstObserverBusinessFailureOverridesHealthPingAlive(t *testing.T) {
	observer := newMonitoredBurstObserver()
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "boom"})

	result := observer.createResultLocked()
	if len(result) != 1 {
		t.Fatalf("expected 1 status, got %d", len(result))
	}
	if result[0].Alive {
		t.Fatal("expected business failure to override alive health ping")
	}
}

func TestBurstObserverBusinessSuccessRestoresAliveOverlay(t *testing.T) {
	observer := newMonitoredBurstObserver()
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess})

	result := observer.createResultLocked()
	if len(result) != 1 {
		t.Fatalf("expected synthetic status, got %d", len(result))
	}
	if !result[0].Alive {
		t.Fatal("expected relay success to synthesize alive status")
	}
}

func TestBurstObserverBusinessFailureIgnoresUnmonitoredTag(t *testing.T) {
	observer := &Observer{
		config:  &Config{SubjectSelector: []string{"proxy-"}},
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		ohm:     &staticOutboundSelector{selected: []string{"proxy-a"}},
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "direct", Kind: extension.OutboundSignalDialFailure, Reason: "boom"})

	if result := observer.createResultLocked(); len(result) != 0 {
		t.Fatalf("expected unmonitored tag to be ignored, got %d statuses", len(result))
	}
}

func TestBurstObserverBusinessFailureAcceptsMonitoredTagBeforeFirstHealthPing(t *testing.T) {
	observer := &Observer{
		config:  &Config{SubjectSelector: []string{"proxy-"}},
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		ohm:     &staticOutboundSelector{selected: []string{"proxy-a"}},
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "boom"})

	result := observer.createResultLocked()
	if len(result) != 1 {
		t.Fatalf("expected monitored tag to be accepted before first health ping, got %d statuses", len(result))
	}
	if result[0].OutboundTag != "proxy-a" || result[0].Alive {
		t.Fatalf("expected monitored tag synthetic dead status, got %+v", result[0])
	}
}

func TestBurstObserverAcceptsRuntimeFeedbackRefreshesSelectorChanges(t *testing.T) {
	selector := &staticOutboundSelector{selected: []string{"proxy-a"}}
	observer := &Observer{
		config:  &Config{SubjectSelector: []string{"proxy-"}},
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		ohm:     selector,
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}

	if !observer.acceptsRuntimeFeedback("proxy-a") {
		t.Fatal("expected initial monitored tag to be accepted")
	}

	selector.selected = []string{"proxy-b"}
	if !observer.acceptsRuntimeFeedback("proxy-b") {
		t.Fatal("expected newly monitored tag to be accepted after selector change")
	}
	if observer.acceptsRuntimeFeedback("proxy-a") {
		t.Fatal("expected removed tag to be ignored after selector change")
	}
}

func TestBurstObserverRecentHealthPingClearsOlderFailureOverlay(t *testing.T) {
	observer := newMonitoredBurstObserver()
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "boom"})
	time.Sleep(2 * time.Millisecond)
	observer.hp.PutResult("proxy-a", 30*time.Millisecond)

	result := observer.createResultLocked()
	if len(result) != 1 {
		t.Fatalf("expected 1 status, got %d", len(result))
	}
	if !result[0].Alive {
		t.Fatal("expected newer health ping to clear older failure overlay")
	}
}

func TestBurstObserverBusinessFailureNeedsSecondStrikeAfterWarmup(t *testing.T) {
	observer := newMonitoredBurstObserver()
	observer.hp.PutResult("proxy-a", 30*time.Millisecond)
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "boom"})

	result := observer.createResultLocked()
	if len(result) != 1 {
		t.Fatalf("expected 1 status, got %d", len(result))
	}
	if !result[0].Alive {
		t.Fatal("expected first business failure to keep warmed-up health ping alive")
	}
}

func TestBurstObserverNewHealthPingClearsOldFailureStreak(t *testing.T) {
	observer := newMonitoredBurstObserver()
	observer.hp.PutResult("proxy-a", 30*time.Millisecond)
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "boom-1"})
	time.Sleep(2 * time.Millisecond)
	observer.hp.PutResult("proxy-a", 35*time.Millisecond)

	result := observer.createResultLocked()
	if !result[0].Alive {
		t.Fatal("expected newer health ping to clear old failure streak")
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "boom-2"})
	result = observer.createResultLocked()
	if !result[0].Alive {
		t.Fatal("expected failure streak to restart after newer health ping")
	}
}
