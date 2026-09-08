package router

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/extension"
	"google.golang.org/protobuf/proto"
)

// 确定性快照只测试决策规则，不模拟真实网络测量。
func championQualitySnapshot(at time.Time, delays ...time.Duration) extension.OutboundQualitySnapshot {
	s := extension.OutboundQualitySnapshot{Time: at, Epoch: at.UnixNano() / int64(extension.QualityInterval), Version: uint64(at.UnixNano())}
	for i, d := range delays {
		s.Outbounds = append(s.Outbounds, extension.OutboundQuality{
			Tag: string(rune('a' + i)), Generation: 1, State: extension.QualityAvailable, Updated: at,
			Probe: extension.QualityLatency{Mean: d, Latest: d, RecentMean: d, Samples: 20, Buckets: 4, Updated: at, FreshUntil: at.Add(time.Minute)},
		})
	}
	return s
}

func newQualityChampion() *ChampionStrategy {
	return &ChampionStrategy{ctx: context.Background(), Settings: ChampionSettings{QualityMode: ChampionQualitySelect, PreferredTag: "a"}.normalized()}
}

func TestChampionQualityThirdCandidateAndPreferredRecovery(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b", "c"}
	at := time.Unix(1000, 0)
	if got := s.pickQuality(tags, championQualitySnapshot(at, 300*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond)); got != "b" {
		t.Fatalf("initial best=%s", got)
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		for step := 1; step <= 6; step++ {
			snapshot := championQualitySnapshot(at.Add(time.Duration(step)*extension.QualityInterval), 300*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond)
			want := "b"
			if step == 6 {
				want = "c"
			}
			if got := s.pickQuality(tags, snapshot); got != want {
				t.Fatalf("step %d got %s, want %s", step, got, want)
			}
		}
		requireLogContains(t, logs, "reason=quality_promoted", "new_tag=c", "wins=4", "transport_rtt_ms=unknown")
	})
	for step := 7; step <= 16; step++ {
		// preferred 已恢复成功，但仍明显较差，不应回切。
		if got := s.pickQuality(tags, championQualitySnapshot(at.Add(time.Duration(step)*extension.QualityInterval), 100*time.Millisecond, 150*time.Millisecond, 50*time.Millisecond)); got != "c" {
			t.Fatalf("recovery alone reclaimed preferred: %s", got)
		}
	}
	for step := 17; step <= 23; step++ {
		want := "c"
		if step == 23 {
			want = "a"
		}
		if got := s.pickQuality(tags, championQualitySnapshot(at.Add(time.Duration(step)*extension.QualityInterval), 58*time.Millisecond, 150*time.Millisecond, 50*time.Millisecond)); got != want {
			t.Fatalf("preferred near step %d got %s want %s", step, got, want)
		}
	}
}

func TestChampionQualityPreferredCannotBlockQualifiedBest(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b", "c"}
	at := time.Unix(1000, 0)
	s.pickQuality(tags, championQualitySnapshot(at, 200*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond))
	for step := 1; step <= 7; step++ {
		// a 与 c 接近，但 a 相对现任只好 15ms，不足普通晋级门槛。
		s.pickQuality(tags, championQualitySnapshot(at.Add(time.Duration(step)*extension.QualityInterval), 85*time.Millisecond, 100*time.Millisecond, 78*time.Millisecond))
	}
	if s.qualityState.current != "c" {
		t.Fatalf("preferred blocked qualified third candidate: %s", s.qualityState.current)
	}
}

func TestChampionQualityConcurrentAndStaleSnapshots(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b"}
	at := time.Unix(1000, 0)
	s.pickQuality(tags, championQualitySnapshot(at, 100*time.Millisecond, 200*time.Millisecond))
	snapshot := championQualitySnapshot(at.Add(extension.QualityInterval), 100*time.Millisecond, 50*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			if got := s.pickQuality(tags, snapshot); got != "a" {
				t.Errorf("same snapshot changed selection: %s", got)
			}
		})
	}
	wg.Wait()
	if s.qualityState.wins != 1 {
		t.Fatalf("duplicate snapshot votes=%d", s.qualityState.wins)
	}
	stale := snapshot
	for step := 2; step <= 6; step++ {
		s.pickQuality(tags, championQualitySnapshot(at.Add(time.Duration(step)*extension.QualityInterval), 100*time.Millisecond, 50*time.Millisecond))
	}
	if got := s.pickQuality(tags, stale); got != "b" {
		t.Fatalf("stale snapshot overwrote champion: %s", got)
	}
	if got := s.pickQuality([]string{"a"}, stale); got != "" {
		t.Fatalf("returned tag outside candidate set: %s", got)
	}
}

func TestChampionQualityUnknownFailoverAndMissingEvidence(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b"}
	at := time.Unix(1000, 0)
	unknown := extension.OutboundQualitySnapshot{Time: at, Epoch: 200, Version: 1}
	if got := s.pickQuality(tags, unknown); got != "a" {
		t.Fatalf("unverified startup=%s", got)
	}
	if !s.qualityState.tentative {
		t.Fatal("unknown startup claimed verified quality")
	}
	lastSwitch := s.qualityState.lastSwitch
	s.pickQuality(tags, unknown)
	if !lastSwitch.Equal(s.qualityState.lastSwitch) {
		t.Fatal("duplicate unknown snapshot reset cooldown")
	}
	snapshot := championQualitySnapshot(at.Add(extension.QualityInterval), 100*time.Millisecond, 120*time.Millisecond)
	s.pickQuality(tags, snapshot)
	// 明确故障允许同 epoch 快速切换。
	snapshot.Version++
	snapshot.Outbounds[0].State = extension.QualityUnavailable
	if got := s.pickQuality(tags, snapshot); got != "b" {
		t.Fatalf("failure did not bypass cooldown: %s", got)
	}
	snapshot = championQualitySnapshot(at.Add(2*extension.QualityInterval), 20*time.Millisecond)
	if got := s.pickQuality(tags, snapshot); got != "b" {
		t.Fatalf("missing current observation triggered switch: %s", got)
	}
	snapshot = championQualitySnapshot(at.Add(3*extension.QualityInterval), 50*time.Millisecond, 60*time.Millisecond)
	for i := range snapshot.Outbounds {
		snapshot.Outbounds[i].State = extension.QualityRecovering
	}
	if got := s.pickQuality(tags, snapshot); got != "" {
		t.Fatalf("all recovering should defer to fallback: %s", got)
	}
}

func TestChampionQualitySustainedLossAndRecoveredSpike(t *testing.T) {
	for _, sustained := range []bool{false, true} {
		t.Run(map[bool]string{false: "spike", true: "sustained"}[sustained], func(t *testing.T) {
			s := newQualityChampion()
			tags := []string{"a", "b"}
			at := time.Unix(1000, 0)
			s.pickQuality(tags, championQualitySnapshot(at, 100*time.Millisecond, 120*time.Millisecond))
			for step := 1; step <= 8; step++ {
				snapshot := championQualitySnapshot(at.Add(time.Duration(step)*extension.QualityInterval), 100*time.Millisecond, 120*time.Millisecond)
				lost := uint64(0)
				if sustained || step == 1 {
					lost = 10
				}
				snapshot.Outbounds[0].Loss = extension.QualityLoss{Sent: 1000, Lost: 50, Buckets: 6, Updated: snapshot.Time, RecentSent: 200, RecentLost: lost}
				s.pickQuality(tags, snapshot)
			}
			want := "a"
			if sustained {
				want = "b"
			}
			if s.qualityState.current != want {
				t.Fatalf("sustained=%v got %s want %s", sustained, s.qualityState.current, want)
			}
		})
	}
}

func TestChampionQualityCostsAndReliability(t *testing.T) {
	at := time.Unix(1000, 0)
	settings := defaultChampionSettings()
	q := championQualitySnapshot(at, 100*time.Millisecond).Outbounds[0]
	unknown := makeChampionQualityCandidate(extension.OutboundQuality{}, at, settings)
	if unknown.known || !math.IsInf(unknown.score, 1) {
		t.Fatal("unknown quality manufactured a delay")
	}
	q.Loss = extension.QualityLoss{Sent: 49, Lost: 10, Buckets: 3, Updated: at, RecentSent: 49, RecentLost: 10}
	if makeChampionQualityCandidate(q, at, settings).lossCost != 0 {
		t.Fatal("insufficient loss sample penalized")
	}
	q.Loss.Sent = 1000
	q.Loss.Lost = 50
	q.Loss.RecentSent = 100
	q.Loss.RecentLost = 5
	q.Probe.Failures = 2
	q.Probe.RecentFailures = 1
	c := makeChampionQualityCandidate(q, at, settings)
	if c.score != c.probeCost+c.runtimeCost+math.Max(c.failureCost, c.lossCost) || c.rank != 1 {
		t.Fatalf("cost/reliability mismatch: %+v", c)
	}
	q.Loss.Corrected = true
	if makeChampionQualityCandidate(q, at, settings).lossCost >= c.lossCost {
		t.Fatal("corrected loss retained full confidence")
	}
	q.Loss = extension.QualityLoss{}
	q.Probe.Failures = 0
	q.Probe.RecentFailures = 0
	q.RTT = extension.QualityLatency{Mean: 50 * time.Millisecond, RecentMean: 50 * time.Millisecond, Baseline: 50 * time.Millisecond, BaselineDeviation: time.Millisecond, Deviation: 30 * time.Millisecond, RecentDeviation: 30 * time.Millisecond, Samples: 10, Buckets: 3, Updated: at, FreshUntil: at.Add(time.Minute)}
	if makeChampionQualityCandidate(q, at, settings).runtimeCost <= 0 {
		t.Fatal("transport jitter increase was ignored")
	}
}

type championQualityProvider struct {
	staticObservatory
	snapshot extension.OutboundQualitySnapshot
	reads    int
}

func (*championQualityProvider) EnableOutboundQuality() {}
func (*championQualityProvider) NewOutboundQualityReporter(string) extension.OutboundQualityReporter {
	return nil
}
func (p *championQualityProvider) GetOutboundQuality() extension.OutboundQualitySnapshot {
	p.reads++
	return p.snapshot
}

func TestChampionQualityModesAndProto(t *testing.T) {
	for _, mode := range []string{ChampionQualityOff, ChampionQualityShadow, ChampionQualitySelect} {
		t.Run(mode, func(t *testing.T) {
			config := &StrategyChampionConfig{QualityMode: mode}
			data, err := proto.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			decoded := new(StrategyChampionConfig)
			if err = proto.Unmarshal(data, decoded); err != nil {
				t.Fatal(err)
			}
			balancer, err := (&BalancingRule{Strategy: "champion", StrategySettings: serial.ToTypedMessage(decoded)}).Build(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := balancer.strategy.(*ChampionStrategy)
			s.ctx = context.Background()
			p := &championQualityProvider{staticObservatory: staticObservatory{status: []*observatory.OutboundStatus{{OutboundTag: "a", Alive: true, Delay: 50}, {OutboundTag: "b", Alive: true, Delay: 150}}}, snapshot: championQualitySnapshot(time.Unix(1000, 0), 300*time.Millisecond, 50*time.Millisecond)}
			s.quality = p
			s.observatory = p
			want := "a"
			if mode == ChampionQualitySelect {
				want = "b"
			}
			if got := s.PickOutbound([]string{"a", "b"}); got != want {
				t.Fatalf("mode=%s got %s want %s", mode, got, want)
			}
			if mode == ChampionQualityOff && p.reads != 0 {
				t.Fatal("off mode collected quality")
			}
			if mode == ChampionQualityShadow && s.lastTag != "a" {
				t.Fatal("shadow changed actual selection")
			}
		})
	}
	if _, err := (&BalancingRule{Strategy: "champion", StrategySettings: serial.ToTypedMessage(&StrategyChampionConfig{QualityMode: "typo"})}).Build(nil, nil); err == nil {
		t.Fatal("invalid protobuf mode accepted")
	}
}

func BenchmarkChampionQualityPick(b *testing.B) {
	for _, mode := range []string{ChampionQualityOff, ChampionQualitySelect} {
		b.Run(mode, func(b *testing.B) {
			delays := make([]time.Duration, 16)
			tags := make([]string, 16)
			p := &championQualityProvider{}
			for i := range delays {
				delays[i] = 100 * time.Millisecond
				tags[i] = string(rune('a' + i))
				p.status = append(p.status, &observatory.OutboundStatus{OutboundTag: tags[i], Alive: true, Delay: 100, LastTryTime: 1})
			}
			p.snapshot = championQualitySnapshot(time.Unix(1000, 0), delays...)
			s := newQualityChampion()
			s.Settings.QualityMode = mode
			s.observatory = p
			s.quality = p
			s.PickOutbound(tags)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s.PickOutbound(tags)
			}
		})
	}
}
