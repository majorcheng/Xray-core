package burst

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/features/extension"
)

func TestBurstObserverBusinessFailureOverridesHealthPingAlive(t *testing.T) {
	observer := &Observer{
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}
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
	observer := &Observer{
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess})

	result := observer.createResultLocked()
	if len(result) != 1 {
		t.Fatalf("expected synthetic status, got %d", len(result))
	}
	if !result[0].Alive {
		t.Fatal("expected relay success to synthesize alive status")
	}
}

func TestBurstObserverRecentHealthPingClearsOlderFailureOverlay(t *testing.T) {
	observer := &Observer{
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}
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
	observer := &Observer{
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}
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
	observer := &Observer{
		hp:      NewHealthPing(nil, nil, &HealthPingConfig{Interval: int64(time.Second), SamplingCount: 1}),
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
	}
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
