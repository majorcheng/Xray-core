package router

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xtls/xray-core/app/observatory"
	clog "github.com/xtls/xray-core/common/log"
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
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 400},
				{OutboundTag: "b", Alive: true, Delay: 80},
			},
		},
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		for i := 0; i < 3; i++ {
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
		observatory: &staticObservatory{
			status: []*observatory.OutboundStatus{
				{OutboundTag: "a", Alive: true, Delay: 90},
				{OutboundTag: "b", Alive: true, Delay: 150},
			},
		},
	}

	for i := 0; i < 2; i++ {
		if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "b" {
			t.Fatalf("preferred should keep building reclaim streak before switch, got=%q at round=%d", tag, i)
		}
	}
	if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "a" {
		t.Fatalf("preferred should reclaim after 3 close wins, got=%q", tag)
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
