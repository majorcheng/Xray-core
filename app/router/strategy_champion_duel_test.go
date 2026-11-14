package router

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"google.golang.org/protobuf/proto"
)

// setChampionDelays 修改静态观测的延迟，代表 observatory 完成了一轮新测量。
func setChampionDelays(strategy *ChampionStrategy, delays ...int64) {
	status := strategy.observatory.(*staticObservatory).status
	for i, delay := range delays {
		status[i].Delay = delay
	}
}

// requireLogLine 要求同一行日志同时包含全部片段，用于同时校验级别与字段。
func requireLogLine(t *testing.T, logs *captureLogHandler, parts ...string) {
	t.Helper()
	for _, line := range strings.Split(logs.text(), "\n") {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("expected one log line contain %q, got=%s", parts, logs.text())
}

func countLogLines(logs *captureLogHandler, part string) int {
	count := 0
	for _, line := range strings.Split(logs.text(), "\n") {
		if strings.Contains(line, part) {
			count++
		}
	}
	return count
}

func newLegacyChampion(lastTag string, status ...*observatory.OutboundStatus) *ChampionStrategy {
	return &ChampionStrategy{
		ctx:         context.Background(),
		lastTag:     lastTag,
		Settings:    ChampionSettings{CandidateObservationCount: 3}.normalized(),
		observatory: &staticObservatory{status: status},
	}
}

func TestChampionStrategyTimestampOnlyRefreshDoesNotCount(t *testing.T) {
	strategy := newLegacyChampion("",
		&observatory.OutboundStatus{OutboundTag: "a", Alive: true, Delay: 400},
		&observatory.OutboundStatus{OutboundTag: "b", Alive: true, Delay: 80},
	)
	status := strategy.observatory.(*staticObservatory).status
	for i := int64(1); i <= 20; i++ {
		// runtime feedback 会刷新时间戳，但没有产生新的延迟测量。
		status[0].LastTryTime, status[0].LastSeenTime = i, i
		status[1].LastTryTime = i
		if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "a" {
			t.Fatalf("timestamp-only refresh promoted challenger at round=%d, got=%q", i, tag)
		}
	}
	setChampionDelays(strategy, 401, 80)
	if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "a" {
		t.Fatalf("second measurement should not promote yet, got=%q", tag)
	}
	setChampionDelays(strategy, 402, 80)
	if tag := strategy.PickOutbound([]string{"a", "b"}); tag != "b" {
		t.Fatalf("third distinct measurement should promote challenger, got=%q", tag)
	}
}

func TestChampionStrategyAlternatingChallengersPromote(t *testing.T) {
	strategy := newLegacyChampion("",
		&observatory.OutboundStatus{OutboundTag: "a", Alive: true, Delay: 400},
		&observatory.OutboundStatus{OutboundTag: "b", Alive: true},
		&observatory.OutboundStatus{OutboundTag: "c", Alive: true},
	)
	rounds := [][]int64{{400, 80, 90}, {400, 90, 80}, {400, 80, 90}}
	for i, delays := range rounds {
		setChampionDelays(strategy, delays...)
		want := "a"
		if i == len(rounds)-1 {
			want = "b"
		}
		if tag := strategy.PickOutbound([]string{"a", "b", "c"}); tag != want {
			t.Fatalf("round=%d got=%q want=%q", i, tag, want)
		}
	}
}

func TestChampionStrategyDegradedChampionYieldsToChallenger(t *testing.T) {
	cases := []struct {
		name      string
		preferred *observatory.OutboundStatus
		best      int64
		want      string
		reason    string
	}{
		{"preferred dead", &observatory.OutboundStatus{OutboundTag: "a"}, 50, "c", "challenger_promoted"},
		{"preferred slow", &observatory.OutboundStatus{OutboundTag: "a", Alive: true, Delay: 400}, 50, "c", "challenger_promoted"},
		// preferred 与 best 足够接近时直接回到 preferred，避免 b -> c -> a 两次切换。
		{"preferred near best", &observatory.OutboundStatus{OutboundTag: "a", Alive: true, Delay: 100}, 60, "a", "preferred_reclaimed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			strategy := newLegacyChampion("b", tc.preferred,
				&observatory.OutboundStatus{OutboundTag: "b", Alive: true},
				&observatory.OutboundStatus{OutboundTag: "c", Alive: true, Delay: tc.best},
			)
			status := strategy.observatory.(*staticObservatory).status
			withLogCapture(t, func(logs *captureLogHandler) {
				for i := int64(1); i <= 3; i++ {
					status[1].Delay = 2000 + i
					tag := strategy.PickOutbound([]string{"a", "b", "c"})
					if i < 3 && tag != "b" {
						t.Fatalf("round=%d switched before enough observations, got=%q", i, tag)
					}
					if i == 3 && tag != tc.want {
						t.Fatalf("degraded champion was kept, got=%q want=%q", tag, tc.want)
					}
				}
				requireLogLine(t, logs, "[Warning]", "reason="+tc.reason+" old_tag=b new_tag="+tc.want)
			})
		})
	}
}

func TestChampionStrategyPromotionClearsDuel(t *testing.T) {
	strategy := newLegacyChampion("",
		&observatory.OutboundStatus{OutboundTag: "a", Alive: true, Delay: 400},
		&observatory.OutboundStatus{OutboundTag: "b", Alive: true},
		&observatory.OutboundStatus{OutboundTag: "c", Alive: true, Delay: 90},
	)
	tags := []string{"a", "b", "c"}
	for i := int64(1); i <= 3; i++ {
		setChampionDelays(strategy, 400, 80+i)
		strategy.PickOutbound(tags)
	}
	if strategy.lastTag != "b" {
		t.Fatalf("expected b promoted, got=%q", strategy.lastTag)
	}
	// 新擂主立即失效时回到 preferred；旧胜场不能让 c 在第一次观测就晋级。
	strategy.observatory.(*staticObservatory).status[1].Alive = false
	withLogCapture(t, func(logs *captureLogHandler) {
		if tag := strategy.PickOutbound(tags); tag != "a" {
			t.Fatalf("stale duel streak promoted challenger, got=%q", tag)
		}
		requireLogLine(t, logs, "[Warning]",
			"reason=champion_unavailable old_tag=b new_tag=a old_score_ms=dead new_score_ms=400")
	})
}

// roundObservatory 每次读取都返回新一轮测量，模拟高并发请求跨越多个探测周期。
type roundObservatory struct {
	staticObservatory
	round atomic.Int64
}

func (o *roundObservatory) GetObservation(context.Context) (proto.Message, error) {
	r := o.round.Add(1)
	return &observatory.ObservationResult{Status: []*observatory.OutboundStatus{
		{OutboundTag: "a", Alive: true, Delay: 400},
		{OutboundTag: "b", Alive: true, Delay: 80 + r%7},
	}}, nil
}

func TestChampionStrategyConcurrentPicksDoNotRevertPromotion(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:         context.Background(),
		Settings:    ChampionSettings{CandidateObservationCount: 3}.normalized(),
		observatory: &roundObservatory{},
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				for j := 0; j < 200; j++ {
					strategy.PickOutbound([]string{"a", "b"})
				}
			})
		}
		wg.Wait()
		if n := countLogLines(logs, "new_tag=b"); n != 1 {
			t.Fatalf("expected exactly one promotion, got=%d logs=%s", n, logs.text())
		}
		if n := countLogLines(logs, "old_tag=b"); n != 0 {
			t.Fatalf("stale decision reverted promotion, logs=%s", logs.text())
		}
	})
	if strategy.lastTag != "b" {
		t.Fatalf("unexpected final champion=%q", strategy.lastTag)
	}
}

// gatedObservatory 在第一次读取时阻塞，用于确认观测读取处于决策锁内。
type gatedObservatory struct {
	staticObservatory
	calls            atomic.Int32
	entered, release chan struct{}
}

func (o *gatedObservatory) GetObservation(ctx context.Context) (proto.Message, error) {
	if o.calls.Add(1) == 1 {
		close(o.entered)
		<-o.release
	}
	return o.staticObservatory.GetObservation(ctx)
}

func TestChampionStrategyReadsObservationInsideLock(t *testing.T) {
	obs := &gatedObservatory{
		staticObservatory: staticObservatory{status: []*observatory.OutboundStatus{{OutboundTag: "a", Alive: true, Delay: 100}}},
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	strategy := &ChampionStrategy{ctx: context.Background(), Settings: ChampionSettings{}.normalized(), observatory: obs}
	var wg sync.WaitGroup
	wg.Go(func() { strategy.PickOutbound([]string{"a"}) })
	<-obs.entered
	wg.Go(func() { strategy.PickOutbound([]string{"a"}) })
	// 锁外读取时，第二个请求会在第一个请求提交前读到观测，提交顺序就可能与读取顺序相反。
	time.Sleep(50 * time.Millisecond)
	reads := obs.calls.Load()
	close(obs.release)
	wg.Wait()
	if reads != 1 {
		t.Fatalf("observation was read outside the decision lock, concurrent reads=%d", reads)
	}
}

func TestChampionStrategyEphemeralChallengerNotPromoted(t *testing.T) {
	strategy := newLegacyChampion("a",
		&observatory.OutboundStatus{OutboundTag: "a", Alive: true, Delay: 400},
		&observatory.OutboundStatus{OutboundTag: "b", Alive: true},
		&observatory.OutboundStatus{OutboundTag: "c"},
	)
	status := strategy.observatory.(*staticObservatory).status
	withLogCapture(t, func(logs *captureLogHandler) {
		var picks []string
		for i := int64(1); i <= 4; i++ {
			// c 只在第 3 轮存活且最快；它自己只有一票，不能兑现 b 攒下的票。
			status[1].Delay = 80 + i
			status[2].Alive, status[2].Delay = i == 3, 50
			picks = append(picks, strategy.PickOutbound([]string{"a", "b", "c"}))
		}
		if got := strings.Join(picks, ""); got != "aaab" {
			t.Fatalf("ephemeral challenger changed the promotion, picks=%v", picks)
		}
		if n := countLogLines(logs, "[Warning]"); n != 1 {
			t.Fatalf("expected one switch, got=%d logs=%s", n, logs.text())
		}
	})
}

func TestChampionStrategyPreferredNeedsOwnVotes(t *testing.T) {
	strategy := newLegacyChampion("b",
		&observatory.OutboundStatus{OutboundTag: "a"},
		&observatory.OutboundStatus{OutboundTag: "b", Alive: true, Delay: 400},
		&observatory.OutboundStatus{OutboundTag: "c", Alive: true},
	)
	status := strategy.observatory.(*staticObservatory).status
	withLogCapture(t, func(logs *captureLogHandler) {
		var picks []string
		for i := int64(1); i <= 4; i++ {
			// preferred 只在第 3 轮存活并接近 best，不能借普通挑战的票直接回切。
			status[2].Delay = 50 + i
			status[0].Alive, status[0].Delay = i == 3, 60
			picks = append(picks, strategy.PickOutbound([]string{"a", "b", "c"}))
		}
		if got := strings.Join(picks, ""); got != "bbbc" {
			t.Fatalf("preferred reclaimed without sustained evidence, picks=%v", picks)
		}
		requireLogLine(t, logs, "[Warning]", "reason=challenger_promoted old_tag=b new_tag=c")
		if n := countLogLines(logs, "[Warning]"); n != 1 {
			t.Fatalf("expected one switch, got=%d logs=%s", n, logs.text())
		}
	})
}

func TestChampionTagForLogQuotesAmbiguousTags(t *testing.T) {
	cases := map[string]string{
		"":        "none",
		"proxy-1": "proxy-1",
		"节点":      "节点",
		"none":    `"none"`,
		"out us":  `"out us"`,
		"a=b":     `"a=b"`,
		`a"b`:     `"a\"b"`,
		"a\tb":    `"a\tb"`,
	}
	for tag, want := range cases {
		if got := championTagForLog(tag); got != want {
			t.Fatalf("tag=%q got=%s want=%s", tag, got, want)
		}
	}
}

// failingObservatory 可按需返回读取错误，用于验证回退日志的进入和退出。
type failingObservatory struct {
	staticObservatory
	err error
}

func (o *failingObservatory) GetObservation(ctx context.Context) (proto.Message, error) {
	if o.err != nil {
		return nil, o.err
	}
	return o.staticObservatory.GetObservation(ctx)
}

func TestChampionStrategyRoundRobinFallbackLogsOnce(t *testing.T) {
	strategy := &ChampionStrategy{}
	withLogCapture(t, func(logs *captureLogHandler) {
		for _, want := range []string{"a", "b", "a"} {
			if tag := strategy.PickOutbound([]string{"a", "b"}); tag != want {
				t.Fatalf("unexpected selected tag, got=%q want=%q", tag, want)
			}
		}
		requireLogLine(t, logs, "[Warning]",
			"champion fallback to round robin balancer=none cause=observatory_unavailable quality_mode=off error=none")
		if n := countLogLines(logs, "champion fallback to round robin"); n != 1 {
			t.Fatalf("fallback should log once per cause, got=%d logs=%s", n, logs.text())
		}
		if n := countLogLines(logs, "champion switched"); n != 0 {
			t.Fatalf("round-robin rotation should not log switches, got=%s", logs.text())
		}
	})
}

func TestChampionStrategyFallbackLogsTransitions(t *testing.T) {
	obs := &failingObservatory{
		staticObservatory: staticObservatory{status: []*observatory.OutboundStatus{
			{OutboundTag: "a", Alive: true, Delay: 50},
			{OutboundTag: "b", Alive: true, Delay: 80},
		}},
		err: errors.New("store closed"),
	}
	strategy := &ChampionStrategy{ctx: context.Background(), BalancerTag: "auto", observatory: obs}
	withLogCapture(t, func(logs *captureLogHandler) {
		for i := 0; i < 3; i++ {
			strategy.PickOutbound([]string{"a", "b"})
		}
		obs.err = nil
		strategy.PickOutbound([]string{"a", "b"})
		requireLogLine(t, logs, "[Warning]",
			`champion fallback to round robin balancer=auto cause=observation_error quality_mode=off error="store closed"`)
		requireLogLine(t, logs, "[Info]", "champion fallback ended balancer=auto cause=observation_error champion=a")
		if n := countLogLines(logs, "champion fallback to round robin"); n != 1 {
			t.Fatalf("fallback should log once per cause, got=%d logs=%s", n, logs.text())
		}
	})
}

func TestChampionStrategyWarnsWhenQualityUnavailable(t *testing.T) {
	strategy := &ChampionStrategy{
		ctx:         context.Background(),
		BalancerTag: "auto",
		Settings:    ChampionSettings{QualityMode: ChampionQualitySelect}.normalized(),
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		strategy.attachObservatory(&staticObservatory{})
		requireLogLine(t, logs, "[Warning]",
			"champion quality mode unavailable balancer=auto quality_mode=select effective_mode=off cause=observatory_without_quality")
	})
	if strategy.observatory == nil || strategy.quality != nil {
		t.Fatalf("unexpected observatory binding: observatory=%v quality=%v", strategy.observatory, strategy.quality)
	}
	strategy.attachObservatory(&championQualityProvider{})
	if strategy.quality == nil {
		t.Fatal("quality observatory was not attached")
	}
}

func TestBalancingRuleBuildChampionKeepsBalancerTag(t *testing.T) {
	balancer, err := (&BalancingRule{Tag: "auto", Strategy: "champion", OutboundSelector: []string{"a"}}).Build(nil, nil)
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	if tag := balancer.strategy.(*ChampionStrategy).BalancerTag; tag != "auto" {
		t.Fatalf("balancer tag not passed to strategy logs, got=%q", tag)
	}
}
