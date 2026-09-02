package router

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	clog "github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/extension"
	"google.golang.org/protobuf/proto"
)

type staticObservatory struct {
	status []*observatory.OutboundStatus
}

func (s *staticObservatory) Start() error {
	return nil
}

func (s *staticObservatory) Close() error {
	return nil
}

func (s *staticObservatory) Type() interface{} {
	return extension.ObservatoryType()
}

func (s *staticObservatory) GetObservation(context.Context) (proto.Message, error) {
	return &observatory.ObservationResult{Status: s.status}, nil
}

type captureLogHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *captureLogHandler) Handle(msg clog.Message) {
	h.mu.Lock()
	h.messages = append(h.messages, msg.String())
	h.mu.Unlock()
}

func (h *captureLogHandler) text() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.messages, "\n")
}

func withLogCapture(t *testing.T, fn func(*captureLogHandler)) {
	t.Helper()
	h := &captureLogHandler{}
	clog.RegisterHandler(h)
	t.Cleanup(func() {
		clog.RegisterHandler(clog.NewLogger(clog.CreateStdoutLogWriter()))
	})
	fn(h)
}

func requireLogContains(t *testing.T, logs *captureLogHandler, parts ...string) {
	t.Helper()
	out := logs.text()
	for _, part := range parts {
		if !strings.Contains(out, part) {
			t.Fatalf("expected logs contain %q, got=%s", part, out)
		}
	}
}

func TestChampionStrategyFallbackToRoundRobinWithoutObservatory(t *testing.T) {
	strategy := &ChampionStrategy{}
	tags := []string{"a", "b", "c"}
	got := []string{
		strategy.PickOutbound(tags),
		strategy.PickOutbound(tags),
		strategy.PickOutbound(tags),
		strategy.PickOutbound(tags),
	}
	want := []string{"a", "b", "c", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected round-robin fallback order, got=%v want=%v", got, want)
	}
}

func TestChampionStrategyFallbackToPreferredTagWithoutObservatory(t *testing.T) {
	strategy := &ChampionStrategy{
		Settings: ChampionSettings{PreferredTag: " b "},
	}
	tags := []string{"a", "b", "c"}
	got := []string{
		strategy.PickOutbound(tags),
		strategy.PickOutbound(tags),
		strategy.PickOutbound(tags),
		strategy.PickOutbound(tags),
	}
	want := []string{"b", "c", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected preferred fallback order, got=%v want=%v", got, want)
	}
}

func TestChampionStrategyFallbackToPreferredTagWhenLastTagIsStale(t *testing.T) {
	strategy := &ChampionStrategy{
		lastTag:  "x",
		index:    1,
		Settings: ChampionSettings{PreferredTag: "b"},
	}
	if tag := strategy.PickOutbound([]string{"b", "a"}); tag != "b" {
		t.Fatalf("preferred fallback should restart from configured tag when lastTag is stale, got=%q", tag)
	}
}

func TestChampionStrategySwitchLogRoundRobinFallback(t *testing.T) {
	strategy := &ChampionStrategy{}
	withLogCapture(t, func(logs *captureLogHandler) {
		tag := strategy.PickOutbound([]string{"a", "b"})
		if tag != "a" {
			t.Fatalf("unexpected selected tag, got=%q", tag)
		}
		requireLogContains(t, logs,
			"champion switched",
			"reason=round_robin_fallback",
			"old_tag=none",
			"new_tag=a",
			"old_delay_ms=-1",
			"new_delay_ms=-1",
		)
	})
}

func TestChampionStrategySwitchLogChallengerPromoted(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx: context.Background(),
		Settings: ChampionSettings{
			CandidateObservationCount: 3,
			PreferredObservationCount: 3,
			HealthPingJitterScale:     0,
			PreferredMaxDelayGap:      100 * time.Millisecond,
		},
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 400, LastTryTime: 1},
				{OutboundTag: "b", Alive: true, Delay: 80, LastTryTime: 1},
			},
		},
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		for i := int64(1); i <= 3; i++ {
			strategy.observatory.(*staticObservatory).status[0].LastTryTime = i
			strategy.observatory.(*staticObservatory).status[1].LastTryTime = i
			strategy.PickOutbound([]string{"a", "b"})
		}
		requireLogContains(t, logs,
			"reason=challenger_promoted",
			"old_tag=a",
			"new_tag=b",
			"old_delay_ms=400",
			"new_delay_ms=80",
		)
	})
}

func TestChampionStrategyAllDeadReturnsEmpty(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx: context.Background(),
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: false},
				{OutboundTag: "b", Alive: false},
			},
		},
	}
	tag := strategy.PickOutbound([]string{"a", "b"})
	if tag != "" {
		t.Fatalf("expected empty tag when all candidates are dead, got=%q", tag)
	}
}

func TestChampionStrategySwitchLogAllCandidatesDead(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:     context.Background(),
		lastTag: "a",
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: false},
				{OutboundTag: "b", Alive: false},
			},
		},
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		tag := strategy.PickOutbound([]string{"a", "b"})
		if tag != "" {
			t.Fatalf("expected empty tag, got=%q", tag)
		}
		requireLogContains(t, logs,
			"reason=all_candidates_dead",
			"old_tag=a",
			"new_tag=none",
			"old_delay_ms=-1",
			"new_delay_ms=-1",
		)
	})
}

func TestChampionStrategyIgnoreStaleLastTag(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:     context.Background(),
		lastTag: "stale",
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: false},
				{OutboundTag: "b", Alive: true, Delay: 50},
			},
		},
	}
	tag := strategy.PickOutbound([]string{"a", "b"})
	if tag != "b" {
		t.Fatalf("expected selected tag to be current live candidate, got=%q", tag)
	}
}

func TestChampionStrategyPreferredReclaimNeedsCloseGap(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:     context.Background(),
		lastTag: "b",
		Settings: ChampionSettings{
			CandidateObservationCount: 3,
			PreferredObservationCount: 3,
			HealthPingJitterScale:     0,
			PreferredMaxDelayGap:      80 * time.Millisecond,
		},
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 120},
				{OutboundTag: "b", Alive: true, Delay: 60},
			},
		},
	}

	for i := 0; i < 3; i++ {
		if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "b" {
			t.Fatalf("preferred should not reclaim with large gap, got=%q at round=%d", tag, i)
		}
	}
}

func TestChampionStrategyPreferredReclaimWhenCloseAndWithinThreshold(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:     context.Background(),
		lastTag: "b",
		Settings: ChampionSettings{
			CandidateObservationCount: 3,
			PreferredObservationCount: 3,
			HealthPingJitterScale:     0,
			PreferredMaxDelayGap:      80 * time.Millisecond,
		},
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 90, LastTryTime: 1},
				{OutboundTag: "b", Alive: true, Delay: 150, LastTryTime: 1},
			},
		},
	}

	for i := int64(1); i <= 2; i++ {
		strategy.observatory.(*staticObservatory).status[0].LastTryTime = i
		strategy.observatory.(*staticObservatory).status[1].LastTryTime = i
		if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "b" {
			t.Fatalf("preferred should keep building reclaim streak before switch, got=%q at round=%d", tag, i)
		}
	}
	strategy.observatory.(*staticObservatory).status[0].LastTryTime = 3
	strategy.observatory.(*staticObservatory).status[1].LastTryTime = 3
	if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "a" {
		t.Fatalf("preferred should reclaim after 3 close wins, got=%q", tag)
	}
}

func TestChampionStrategyPreferredReclaimWhenPreferredMuchFaster(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:     context.Background(),
		lastTag: "b",
		Settings: ChampionSettings{
			CandidateObservationCount: 3,
			PreferredObservationCount: 3,
			HealthPingJitterScale:     0,
			PreferredMaxDelayGap:      80 * time.Millisecond,
		},
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 20, LastTryTime: 1},
				{OutboundTag: "b", Alive: true, Delay: 150, LastTryTime: 1},
			},
		},
	}

	for i := int64(1); i <= 2; i++ {
		strategy.observatory.(*staticObservatory).status[0].LastTryTime = i
		strategy.observatory.(*staticObservatory).status[1].LastTryTime = i
		if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "b" {
			t.Fatalf("preferred should keep building reclaim streak before switch, got=%q at round=%d", tag, i)
		}
	}
	strategy.observatory.(*staticObservatory).status[0].LastTryTime = 3
	strategy.observatory.(*staticObservatory).status[1].LastTryTime = 3
	if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "a" {
		t.Fatalf("preferred should reclaim when it is much faster, got=%q", tag)
	}
}

func TestChampionObservationPreferredCanReclaimBlocksLargeUpwardGap(t *testing.T) {
	obs := &championObservation{
		settings: ChampionSettings{
			PreferredMaxDelayGap: 30 * time.Millisecond,
		}.normalized(),
	}
	if obs.preferredCanReclaim("preferred", 150, "anchor", 100) {
		t.Fatal("preferred should not reclaim when it is slower and upward gap exceeds threshold")
	}
}

func TestChampionObservationPreferredCanReclaimIgnoresLargeDownwardGap(t *testing.T) {
	obs := &championObservation{
		settings: ChampionSettings{
			PreferredMaxDelayGap: 80 * time.Millisecond,
		}.normalized(),
	}
	if !obs.preferredCanReclaim("preferred", 20, "anchor", 150) {
		t.Fatal("preferred should reclaim when it is faster even if downward gap exceeds threshold")
	}
}

func TestChampionStrategyRequiresDistinctObservationsForPromotion(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx: context.Background(),
		Settings: ChampionSettings{
			CandidateObservationCount: 3,
			PreferredObservationCount: 3,
			HealthPingJitterScale:     0,
			PreferredMaxDelayGap:      80 * time.Millisecond,
		},
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 400, LastTryTime: 1},
				{OutboundTag: "b", Alive: true, Delay: 80, LastTryTime: 1},
			},
		},
	}
	for i := 0; i < 5; i++ {
		if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "a" {
			t.Fatalf("expected champion to stay on old tag during same observation, got=%q at round=%d", tag, i)
		}
	}
}

func TestChampionStrategyHealthPingJitterPenaltyPrefersStableNode(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx: context.Background(),
		Settings: ChampionSettings{
			CandidateObservationCount: 3,
			PreferredObservationCount: 3,
			HealthPingJitterScale:     1,
			PreferredMaxDelayGap:      80 * time.Millisecond,
		},
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{
					OutboundTag: "a",
					Alive:       true,
					Delay:       70,
					LastTryTime: 1,
					HealthPing:  &observatory.HealthPingMeasurementResult{Average: 70 * int64(time.Millisecond), Deviation: 180 * int64(time.Millisecond), All: 10},
				},
				{
					OutboundTag: "b",
					Alive:       true,
					Delay:       120,
					LastTryTime: 1,
					HealthPing:  &observatory.HealthPingMeasurementResult{Average: 120 * int64(time.Millisecond), Deviation: 10 * int64(time.Millisecond), All: 10},
				},
			},
		},
	}
	for i := int64(1); i <= 3; i++ {
		strategy.observatory.(*staticObservatory).status[0].LastTryTime = i
		strategy.observatory.(*staticObservatory).status[1].LastTryTime = i
		strategy.PickOutbound([]string{"a", "b"})
	}
	if tag := strategy.currentChampion(); tag != "b" {
		t.Fatalf("expected stable node to become champion, got=%q", tag)
	}
}

func TestChampionObservationPreferredUsesConfiguredTag(t *testing.T) {
	obs := newChampionObservation(
		[]string{"a", "b"},
		&observatory.ObservationResult{
			Status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 20, LastTryTime: 1},
				{OutboundTag: "b", Alive: true, Delay: 40, LastTryTime: 1},
			},
		},
		ChampionSettings{PreferredTag: "b"},
	)
	tag, _ := obs.preferred()
	if tag != "b" {
		t.Fatalf("unexpected preferred tag: %q", tag)
	}
}

func TestChampionObservationPreferredFallsBackWhenConfiguredTagMissing(t *testing.T) {
	obs := newChampionObservation(
		[]string{"a", "b"},
		&observatory.ObservationResult{
			Status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 20, LastTryTime: 1},
				{OutboundTag: "b", Alive: true, Delay: 40, LastTryTime: 1},
			},
		},
		ChampionSettings{PreferredTag: "missing"},
	)
	tag, _ := obs.preferred()
	if tag != "a" {
		t.Fatalf("unexpected fallback preferred tag: %q", tag)
	}
}

func TestBalancingRuleBuildChampion(t *testing.T) {
	rule := &BalancingRule{
		Strategy:         "champion",
		OutboundSelector: []string{"a"},
	}
	balancer, err := rule.Build(nil, nil)
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	if _, ok := balancer.strategy.(*ChampionStrategy); !ok {
		t.Fatalf("unexpected strategy type: %T", balancer.strategy)
	}
}

func TestBalancingRuleBuildChampionSettings(t *testing.T) {
	rule := &BalancingRule{
		Strategy:         "champion",
		OutboundSelector: []string{"a"},
		StrategySettings: serial.ToTypedMessage(&StrategyChampionConfig{
			CandidateObservationCount: 5,
			PreferredObservationCount: 8,
			HealthPingJitterScale:     1.5,
			PreferredMaxDelayGap:      int64(120 * time.Millisecond),
			PreferredTag:              "b",
		}),
	}
	balancer, err := rule.Build(nil, nil)
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	strategy, ok := balancer.strategy.(*ChampionStrategy)
	if !ok {
		t.Fatalf("unexpected strategy type: %T", balancer.strategy)
	}
	if strategy.Settings.CandidateObservationCount != 5 {
		t.Fatalf("unexpected candidate observation count: %d", strategy.Settings.CandidateObservationCount)
	}
	if strategy.Settings.PreferredObservationCount != 8 {
		t.Fatalf("unexpected preferred observation count: %d", strategy.Settings.PreferredObservationCount)
	}
	if strategy.Settings.HealthPingJitterScale != 1.5 {
		t.Fatalf("unexpected jitter scale: %v", strategy.Settings.HealthPingJitterScale)
	}
	if strategy.Settings.PreferredMaxDelayGap != 120*time.Millisecond {
		t.Fatalf("unexpected preferred max delay gap: %v", strategy.Settings.PreferredMaxDelayGap)
	}
	if strategy.Settings.PreferredTag != "b" {
		t.Fatalf("unexpected preferred tag: %q", strategy.Settings.PreferredTag)
	}
}

func TestBalancingRuleBuildChampionPartialSettingsKeepDefaults(t *testing.T) {
	rule := &BalancingRule{
		Strategy:         "champion",
		OutboundSelector: []string{"a"},
		StrategySettings: serial.ToTypedMessage(&StrategyChampionConfig{
			CandidateObservationCount: 5,
		}),
	}
	balancer, err := rule.Build(nil, nil)
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	strategy, ok := balancer.strategy.(*ChampionStrategy)
	if !ok {
		t.Fatalf("unexpected strategy type: %T", balancer.strategy)
	}
	if strategy.Settings.CandidateObservationCount != 5 {
		t.Fatalf("unexpected candidate observation count: %d", strategy.Settings.CandidateObservationCount)
	}
	if strategy.Settings.PreferredObservationCount != defaultChampionSettings().PreferredObservationCount {
		t.Fatalf("unexpected preferred observation count: %d", strategy.Settings.PreferredObservationCount)
	}
	if strategy.Settings.HealthPingJitterScale != defaultChampionSettings().HealthPingJitterScale {
		t.Fatalf("unexpected jitter scale: %v", strategy.Settings.HealthPingJitterScale)
	}
	if strategy.Settings.PreferredMaxDelayGap != defaultChampionSettings().PreferredMaxDelayGap {
		t.Fatalf("unexpected preferred max delay gap: %v", strategy.Settings.PreferredMaxDelayGap)
	}
	if strategy.Settings.PreferredTag != "" {
		t.Fatalf("unexpected preferred tag: %q", strategy.Settings.PreferredTag)
	}
}
