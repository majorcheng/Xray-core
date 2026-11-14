package router

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/features/extension"
)

// qualityTestProbeTTL 对应 29s 探测间隔的有效期 2×(29s+5s)。
const qualityTestProbeTTL = 68 * time.Second

// setQualityProbe 改写探测窗口。滚动与最近统计取同一批样本，对应 29s 探测时窗口只有 2–3 个样本。
func setQualityProbe(s *extension.OutboundQualitySnapshot, i int, mean, deviation, latest time.Duration, updated time.Time) {
	p := &s.Outbounds[i].Probe
	p.Mean, p.Deviation, p.RecentMean, p.RecentDeviation = mean, deviation, mean, deviation
	p.Latest, p.Updated, p.FreshUntil = latest, updated, updated.Add(qualityTestProbeTTL)
}

// setQualityRTT 设置传输 RTT，高于基线的部分即运行时分量 D。Updated 取快照时间，模拟每次评估都有流量的现任。
func setQualityRTT(s *extension.OutboundQualitySnapshot, i int, mean, baseline time.Duration) {
	s.Outbounds[i].RTT = extension.QualityLatency{
		Mean: mean, RecentMean: mean, Baseline: baseline, Samples: 12, Buckets: 12,
		Updated: s.Time, FreshUntil: s.Time.Add(time.Minute),
	}
}

type qualityProbeRound struct{ mean, deviation, latest time.Duration }

// runQualityRounds 每 5s 评估一次，每 6 次评估（30s）双方各有一轮新探测；a 的 RTT 比基线高 rttExcess。
func runQualityRounds(t *testing.T, s *ChampionStrategy, base time.Time, steps int, rounds []qualityProbeRound, rttExcess time.Duration, want func(step int) string) {
	t.Helper()
	tags := []string{"a", "b"}
	for step := 0; step < steps; step++ {
		now := base.Add(time.Duration(step) * extension.QualityInterval)
		probed := base.Add(time.Duration(step/6) * 6 * extension.QualityInterval)
		r := rounds[min(step/6, len(rounds)-1)]
		snapshot := championQualitySnapshot(now, r.latest, 236*time.Millisecond)
		setQualityProbe(&snapshot, 0, r.mean, r.deviation, r.latest, probed)
		setQualityProbe(&snapshot, 1, 236*time.Millisecond, 0, 236*time.Millisecond, probed)
		setQualityRTT(&snapshot, 0, 100*time.Millisecond+rttExcess, 100*time.Millisecond)
		if got := s.pickQuality(tags, snapshot); got != want(step) {
			t.Fatalf("step %d got %s want %s (wins=%d)", step, got, want(step), s.qualityState.wins)
		}
	}
}

// newSettledQualityChampion 先以 a=215ms、b=236ms 完成首次选择，返回冷却结束后的起始时间。
func newSettledQualityChampion(t *testing.T) (*ChampionStrategy, time.Time) {
	t.Helper()
	s := newQualityChampion()
	at := time.Unix(1000, 0)
	if got := s.pickQuality([]string{"a", "b"}, championQualitySnapshot(at, 215*time.Millisecond, 236*time.Millisecond)); got != "a" {
		t.Fatalf("initial=%s", got)
	}
	return s, at.Add(time.Minute)
}

// 一个 800ms 慢样本让 P 偏高约 68s；现任 RTT 只比基线高 10ms，这类运行时证据与本次劣势无关，不能每 5s 计一票。
func TestChampionQualitySingleSlowProbeDoesNotPromote(t *testing.T) {
	s, base := newSettledQualityChampion(t)
	// a 的窗口依次为 [215,800]、[215,800,220]、[800,220,218]、[220,218,221]。
	rounds := []qualityProbeRound{
		{507500 * time.Microsecond, 292500 * time.Microsecond, 800 * time.Millisecond},
		{411667 * time.Microsecond, 274600 * time.Microsecond, 220 * time.Millisecond},
		{412667 * time.Microsecond, 273900 * time.Microsecond, 218 * time.Millisecond},
		{219667 * time.Microsecond, 1247 * time.Microsecond, 221 * time.Millisecond},
	}
	withLogCapture(t, func(logs *captureLogHandler) {
		runQualityRounds(t, s, base, 18, rounds, 10*time.Millisecond, func(int) string {
			if s.qualityState.wins > 1 {
				t.Fatalf("stale slow sample kept voting: wins=%d", s.qualityState.wins)
			}
			return "a"
		})
		requireLogLine(t, logs, "reason=quality_observing", "challenger=b", "vote=none wins=1")
		runQualityRounds(t, s, base.Add(18*extension.QualityInterval), 1, rounds[3:], 10*time.Millisecond, func(int) string { return "a" })
	})
	if s.qualityState.challenger != "" || s.qualityState.wins != 0 {
		t.Fatalf("challenge not cleared after slow sample expired: challenger=%s wins=%d", s.qualityState.challenger, s.qualityState.wins)
	}
}

// 现任每轮探测都慢时仍换线，但票数按探测轮累计：第 4 轮（约 90s）才晋级。
func TestChampionQualitySustainedSlowProbePromotesByRounds(t *testing.T) {
	s, base := newSettledQualityChampion(t)
	rounds := []qualityProbeRound{{500 * time.Millisecond, 0, 500 * time.Millisecond}}
	withLogCapture(t, func(logs *captureLogHandler) {
		runQualityRounds(t, s, base, 19, rounds, 10*time.Millisecond, func(step int) string {
			if step == 18 {
				return "b"
			}
			return "a"
		})
		requireLogLine(t, logs, "[Warning]", "reason=quality_promoted old_tag=a new_tag=b", "vote=probe wins=4")
	})
}

// 现任实际 RTT 比基线高 200ms，运行时证据与劣势有关，仍按 5s 周期计票，约 20s 换线。
func TestChampionQualityRuntimeDegradationPromotesQuickly(t *testing.T) {
	s, base := newSettledQualityChampion(t)
	rounds := []qualityProbeRound{{215 * time.Millisecond, 0, 215 * time.Millisecond}}
	withLogCapture(t, func(logs *captureLogHandler) {
		runQualityRounds(t, s, base, 5, rounds, 200*time.Millisecond, func(step int) string {
			if step == 4 {
				return "b"
			}
			return "a"
		})
		requireLogLine(t, logs, "reason=quality_observing", "vote=probe+runtime wins=1")
		requireLogLine(t, logs, "[Warning]", "reason=quality_promoted old_tag=a new_tag=b", "vote=runtime wins=4", "challenger_wins=4")
	})
}

// pendingPreferredSnapshot 返回首选 a 已进入观测范围、但还没有任何探测结果的快照。
func pendingPreferredSnapshot(at time.Time, others ...time.Duration) extension.OutboundQualitySnapshot {
	s := championQualitySnapshot(at, append([]time.Duration{0}, others...)...)
	s.Outbounds[0].State, s.Outbounds[0].Probe = extension.QualityUnknown, extension.QualityLatency{}
	return s
}

// 冷启动时首选还没有探测结果，先到的其他样本不能决定首次选择。
func TestChampionQualityColdStartWaitsForPreferred(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b"}
	at := time.Unix(1000, 0)
	if got := s.pickQuality(tags, pendingPreferredSnapshot(at, 484*time.Millisecond)); got != "a" || !s.qualityState.tentative || s.qualityState.reason != "quality_unverified" {
		t.Fatalf("preferred pending: got %s tentative=%v reason=%s", got, s.qualityState.tentative, s.qualityState.reason)
	}
	// 同一 5s 周期内首选出了第一个结果，仍按新版本重新选择。
	ready := championQualitySnapshot(at.Add(2*time.Second), 300*time.Millisecond, 484*time.Millisecond)
	if got := s.pickQuality(tags, ready); got != "a" || s.qualityState.tentative || s.qualityState.reason != "quality_initial_best" {
		t.Fatalf("preferred ready: got %s tentative=%v reason=%s", got, s.qualityState.tentative, s.qualityState.reason)
	}
}

// 首选已有失败结果、不在观测范围或已确认故障时，不等待。
func TestChampionQualityColdStartWithoutPendingPreferred(t *testing.T) {
	at := time.Unix(1000, 0)
	failed := pendingPreferredSnapshot(at, 484*time.Millisecond)
	failed.Outbounds[0].Probe = extension.QualityLatency{Samples: 1, Failures: 1, LastFailed: true, Updated: at, FreshUntil: at.Add(time.Minute)}
	unobserved := championQualitySnapshot(at, 484*time.Millisecond)
	unobserved.Outbounds[0].Tag = "b"
	down := pendingPreferredSnapshot(at, 484*time.Millisecond)
	down.Outbounds[0].State = extension.QualityUnavailable
	for name, snapshot := range map[string]extension.OutboundQualitySnapshot{"failed": failed, "unobserved": unobserved, "down": down} {
		t.Run(name, func(t *testing.T) {
			s := newQualityChampion()
			if got := s.pickQuality([]string{"a", "b"}, snapshot); got != "b" || s.qualityState.reason != "quality_initial_best" {
				t.Fatalf("got %s reason=%s", got, s.qualityState.reason)
			}
		})
	}
}

// 现任故障时立即切到已知的最佳候选，不等待首选的首个结果。
func TestChampionQualityFailoverDoesNotWaitForPreferred(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b", "c"}
	at := time.Unix(1000, 0)
	if got := s.pickQuality(tags, championQualitySnapshot(at, 400*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond)); got != "b" {
		t.Fatalf("initial=%s", got)
	}
	next := pendingPreferredSnapshot(at.Add(extension.QualityInterval), 200*time.Millisecond, 300*time.Millisecond)
	next.Outbounds[1].State = extension.QualityUnavailable
	if got := s.pickQuality(tags, next); got != "c" || s.qualityState.reason != "quality_failover" {
		t.Fatalf("failover got %s reason=%s", got, s.qualityState.reason)
	}
}

// twoLineSnapshot 构造现任 a（探测 500ms）与挑战者 b（探测 236ms）的快照；aRTT、bRTT 是高于 100ms 基线的部分，0 表示没有 RTT 数据。
func twoLineSnapshot(now, aProbe, bProbe time.Time, aRTT, bRTT time.Duration) extension.OutboundQualitySnapshot {
	s := championQualitySnapshot(now, 500*time.Millisecond, 236*time.Millisecond)
	setQualityProbe(&s, 0, 500*time.Millisecond, 0, 500*time.Millisecond, aProbe)
	setQualityProbe(&s, 1, 236*time.Millisecond, 0, 236*time.Millisecond, bProbe)
	for i, excess := range []time.Duration{aRTT, bRTT} {
		if excess > 0 {
			setQualityRTT(&s, i, 100*time.Millisecond+excess, 100*time.Millisecond)
		}
	}
	return s
}

// pickVote 执行一次评估，要求 a 仍是擂主且总票数符合预期。
func pickVote(t *testing.T, s *ChampionStrategy, snapshot extension.OutboundQualitySnapshot, wantWins int) {
	t.Helper()
	if got := s.pickQuality([]string{"a", "b"}, snapshot); got != "a" || s.qualityState.wins != wantWins {
		t.Fatalf("got %s wins=%d, want a wins=%d", got, s.qualityState.wins, wantWins)
	}
}

// 一票只消耗它实际使用的观测：运行时票不消耗现任未用的探测样本，探测票也不消耗未用的运行时证据。
func TestChampionQualityVoteConsumesOnlyUsedEvidence(t *testing.T) {
	t.Run("runtime vote keeps probe sample", func(t *testing.T) {
		s, base := newSettledQualityChampion(t)
		next, later := base.Add(extension.QualityInterval), base.Add(2*extension.QualityInterval)
		withLogCapture(t, func(logs *captureLogHandler) {
			pickVote(t, s, twoLineSnapshot(base, base, base, 200*time.Millisecond, 0), 1)
			// b 没有新证据，只计运行时票；a 在 next 的新探测样本没有被使用。
			pickVote(t, s, twoLineSnapshot(next, next, base, 200*time.Millisecond, 0), 2)
			requireLogLine(t, logs, "current_evidence_at="+qualityTimeText(base), "runtime_evidence_at="+qualityTimeText(next), "vote=runtime wins=2")
		})
		// b 有了新证据，a 上一轮未用的探测样本可以计探测票。
		pickVote(t, s, twoLineSnapshot(later, next, later, 0, 0), 3)
	})
	t.Run("probe vote keeps runtime evidence", func(t *testing.T) {
		s, base := newSettledQualityChampion(t)
		// 双方运行时分量相同，只计探测票；a 的运行时证据没有被使用。
		pickVote(t, s, twoLineSnapshot(base, base, base, 200*time.Millisecond, 200*time.Millisecond), 1)
		// b 的运行时恢复后，a 尚未使用、仍在 10s 内的运行时证据足以计运行时票。
		next := twoLineSnapshot(base.Add(extension.QualityInterval), base, base, 200*time.Millisecond, 0)
		next.Outbounds[0].RTT.Updated, next.Outbounds[0].RTT.FreshUntil = base, base.Add(time.Minute)
		pickVote(t, s, next, 2)
	})
}

// 切换类别变化后重新计数，但现任已支撑过一票的探测样本不能再计票。
func TestChampionQualityRecountDoesNotReuseEvidence(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b", "c"}
	at := time.Unix(1000, 0)
	if got := s.pickQuality(tags, championQualitySnapshot(at, 400*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond)); got != "b" {
		t.Fatalf("initial=%s", got)
	}
	// 首选 a 接近现任 b：preferred_near 用 b 在 base 的探测样本计一票。
	base := at.Add(time.Minute)
	s.pickQuality(tags, championQualitySnapshot(base, 205*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond))
	if s.qualityState.kind != "preferred_near" || s.qualityState.wins != 1 {
		t.Fatalf("preferred_near kind=%s wins=%d", s.qualityState.kind, s.qualityState.wins)
	}
	// a、c 有新探测且明显快于 b，改为普通晋级并重新计数；b 没有新探测，同一样本不能再计票。
	next := base.Add(extension.QualityInterval)
	snapshot := championQualitySnapshot(next, 160*time.Millisecond, 200*time.Millisecond, 155*time.Millisecond)
	setQualityProbe(&snapshot, 1, 200*time.Millisecond, 0, 200*time.Millisecond, base)
	s.pickQuality(tags, snapshot)
	if s.qualityState.kind != "quality_promoted" || s.qualityState.challenger != "a" || s.qualityState.wins != 0 {
		t.Fatalf("recount reused evidence: kind=%s challenger=%s wins=%d", s.qualityState.kind, s.qualityState.challenger, s.qualityState.wins)
	}
	// b 有了新探测后照常计票。
	later := next.Add(extension.QualityInterval)
	if got := s.pickQuality(tags, championQualitySnapshot(later, 160*time.Millisecond, 200*time.Millisecond, 155*time.Millisecond)); got != "b" || s.qualityState.wins != 1 {
		t.Fatalf("new sample got %s wins=%d", got, s.qualityState.wins)
	}
}

// 等待首选首个结果期间重复选择同一未验证线路不算切换，不刷新切换时间与冷却；验证完成才更新。
func TestChampionQualityPendingPreferredKeepsSwitchTime(t *testing.T) {
	s := newQualityChampion()
	tags := []string{"a", "b"}
	at := time.Unix(1000, 0)
	s.pickQuality(tags, pendingPreferredSnapshot(at, 484*time.Millisecond))
	for _, d := range []time.Duration{2 * time.Second, 7 * time.Second} {
		if got := s.pickQuality(tags, pendingPreferredSnapshot(at.Add(d), 480*time.Millisecond)); got != "a" || !s.qualityState.lastSwitch.Equal(at) {
			t.Fatalf("pending +%v: got %s last_switch=%v", d, got, s.qualityState.lastSwitch)
		}
	}
	ready := at.Add(9 * time.Second)
	if got := s.pickQuality(tags, championQualitySnapshot(ready, 300*time.Millisecond, 480*time.Millisecond)); got != "a" || s.qualityState.tentative || !s.qualityState.lastSwitch.Equal(ready) {
		t.Fatalf("verified: got %s tentative=%v last_switch=%v", got, s.qualityState.tentative, s.qualityState.lastSwitch)
	}
}
