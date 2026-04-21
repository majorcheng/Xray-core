package observatory

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/features/extension"
)

func TestObserverUpdateStatusPrunesStaleOutbounds(t *testing.T) {
	observer := &Observer{
		status: []*OutboundStatus{
			{
				OutboundTag:     "keep",
				Alive:           true,
				Delay:           42,
				LastErrorReason: "",
				LastSeenTime:    111,
				LastTryTime:     222,
			},
			{
				OutboundTag:     "drop",
				Alive:           false,
				Delay:           deadDelayMs,
				LastErrorReason: "probe failed",
				LastSeenTime:    333,
				LastTryTime:     444,
			},
		},
		overlay: newRuntimeFeedbackOverlay(),
	}

	observer.clearRemovedOutbounds([]string{"keep"})

	if len(observer.status) != 1 {
		t.Fatalf("expected 1 status after pruning, got %d", len(observer.status))
	}

	got := observer.status[0]
	if got.OutboundTag != "keep" {
		t.Fatalf("expected remaining status for keep, got %q", got.OutboundTag)
	}
	if !got.Alive {
		t.Fatal("expected remaining status to preserve Alive field")
	}
	if got.Delay != 42 {
		t.Fatalf("expected remaining status to preserve Delay, got %d", got.Delay)
	}
	if got.LastSeenTime != 111 {
		t.Fatalf("expected remaining status to preserve LastSeenTime, got %d", got.LastSeenTime)
	}
	if got.LastTryTime != 222 {
		t.Fatalf("expected remaining status to preserve LastTryTime, got %d", got.LastTryTime)
	}
}

func TestObserverUpdateStatusClearsWhenNoOutboundsRemain(t *testing.T) {
	observer := &Observer{
		status:  []*OutboundStatus{{OutboundTag: "drop-1"}, {OutboundTag: "drop-2"}},
		overlay: newRuntimeFeedbackOverlay(),
	}
	observer.overlay.apply(&extension.OutboundSignal{OutboundTag: "drop-3", Kind: extension.OutboundSignalDialFailure, Reason: "boom"})

	observer.clearRemovedOutbounds(nil)

	if len(observer.status) != 0 {
		t.Fatalf("expected all statuses to be removed, got %d", len(observer.status))
	}
	if len(observer.overlay.statusByTag) != 0 {
		t.Fatalf("expected overlay statuses to be removed, got %d", len(observer.overlay.statusByTag))
	}
}

func TestObserverBusinessFailureOverridesProbeStatus(t *testing.T) {
	observer := &Observer{overlay: newRuntimeFeedbackOverlay()}
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "dial failed"})

	result := observer.snapshotObservationStatusLocked()
	if len(result) != 1 {
		t.Fatalf("expected 1 status, got %d", len(result))
	}
	if result[0].Alive {
		t.Fatal("expected business failure to mark outbound dead")
	}
	if result[0].Delay != deadDelayMs {
		t.Fatalf("expected dead delay, got %d", result[0].Delay)
	}
	if result[0].LastErrorReason != "dial failed" {
		t.Fatalf("expected failure reason to be preserved, got %q", result[0].LastErrorReason)
	}
}

func TestObserverBusinessSuccessRestoresSyntheticAliveStatus(t *testing.T) {
	observer := &Observer{overlay: newRuntimeFeedbackOverlay()}
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess})

	result := observer.snapshotObservationStatusLocked()
	if len(result) != 1 {
		t.Fatalf("expected synthetic status, got %d entries", len(result))
	}
	if !result[0].Alive {
		t.Fatal("expected relay success to synthesize alive status")
	}
	if result[0].Delay <= 0 {
		t.Fatalf("expected positive synthetic delay, got %d", result[0].Delay)
	}
}

func TestObserverBusinessFailureNeedsSecondStrikeAfterWarmup(t *testing.T) {
	observer := &Observer{
		status:  []*OutboundStatus{{OutboundTag: "proxy-a", Alive: true, Delay: 20, LastSeenTime: 10, LastTryTime: 10}},
		overlay: newRuntimeFeedbackOverlay(),
	}
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "dial failed"})

	result := observer.snapshotObservationStatusLocked()
	if len(result) != 1 {
		t.Fatalf("expected 1 status, got %d", len(result))
	}
	if !result[0].Alive {
		t.Fatal("expected first failure after warmup to keep outbound alive")
	}
}

func TestObserverBusinessSuccessNeedsRecoveryStreakAfterDown(t *testing.T) {
	observer := &Observer{
		status:  []*OutboundStatus{{OutboundTag: "proxy-a", Alive: true, Delay: 20, LastSeenTime: 10, LastTryTime: 10}},
		overlay: newRuntimeFeedbackOverlay(),
	}
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "dial failed"})
	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "dial failed"})

	for i := 0; i < 5; i++ {
		observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess})
		result := observer.snapshotObservationStatusLocked()
		if result[0].Alive {
			t.Fatalf("expected recovery streak to continue building before restore, round=%d", i)
		}
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess})
	result := observer.snapshotObservationStatusLocked()
	if result[0].Alive {
		t.Fatal("expected sixth success to keep outbound down")
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess})
	result = observer.snapshotObservationStatusLocked()
	if !result[0].Alive {
		t.Fatal("expected seventh success to restore outbound alive")
	}
}

func TestObserverNewProbeClearsOldFailureStreak(t *testing.T) {
	observer := &Observer{
		status:  []*OutboundStatus{{OutboundTag: "proxy-a", Alive: true, Delay: 20, LastSeenTime: 10, LastTryTime: 10}},
		overlay: newRuntimeFeedbackOverlay(),
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "dial failed 1"})
	observer.status[0].LastTryTime = time.Now().Unix() + 2
	observer.status[0].LastSeenTime = observer.status[0].LastTryTime
	result := observer.snapshotObservationStatusLocked()
	if !result[0].Alive {
		t.Fatal("expected newer probe result to keep outbound alive")
	}

	observer.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalDialFailure, Reason: "dial failed 2"})
	result = observer.snapshotObservationStatusLocked()
	if !result[0].Alive {
		t.Fatal("expected fresh failure streak to restart after newer probe")
	}
}
