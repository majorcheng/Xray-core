package router

import (
	"math"
	"time"

	"github.com/xtls/xray-core/features/extension"
)

const championQualityCooldown = 30 * time.Second

type championQualityState struct {
	current          string
	generation       uint64
	tentative        bool
	initialized      bool
	lastEpoch        int64
	lastVersion      uint64
	lastEvaluation   time.Time
	lastSwitch       time.Time
	challenger, kind string
	// wins 是同类挑战的总票数；challengerWins 与 candidateEvidence 是当前挑战者自己的票数和最近一次探测票所用的证据。
	wins, challengerWins       int
	started, candidateEvidence time.Time
	// currentEvidence 与 runtimeEvidence 是本擂主任期内最近一次探测票、运行时票所用的现任观测时间。
	// 重新计数时保留、换擂主时清空，现任的每个观测因此最多支撑一票。
	currentEvidence, runtimeEvidence time.Time
	// votes 保存同类挑战中每个挑战者的票数与证据；只能在 s.mu 内读写，日志与状态只读上面的字段。
	votes  map[string]championQualityVote
	reason string
}

// championQualityVote 是某个挑战者在本次同类挑战中的票数和最近一次探测票所用的证据时间。
type championQualityVote struct {
	wins     int
	evidence time.Time
}

type championQualityCandidate struct {
	quality                                       extension.OutboundQuality
	known                                         bool
	rank                                          int
	score, recentScore, recentProbeCost           float64
	probeCost, runtimeCost, failureCost, lossCost float64
	evidence, runtimeEvidence                     time.Time
}

func qualityMilliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func qualityAgeWeight(updated, until, now time.Time) float64 {
	if updated.IsZero() || !now.Before(until) {
		return 0
	}
	span := until.Sub(updated)
	if span <= 0 {
		return 0
	}
	return math.Min(1, math.Max(0, float64(until.Sub(now))/float64(span)))
}

func qualityLatencyKnown(m extension.QualityLatency, now time.Time) bool {
	return m.Samples > m.Failures && !m.Updated.IsZero() && now.Before(m.FreshUntil)
}

func qualityConfidence(m extension.QualityLatency, now time.Time, fullSamples float64) float64 {
	return math.Min(1, float64(m.Samples)/fullSamples) * math.Min(1, float64(m.Buckets)/3) * qualityAgeWeight(m.Updated, m.FreshUntil, now)
}

func qualityFailureCost(m extension.QualityLatency, now time.Time) float64 {
	if m.Samples == 0 {
		return 0
	}
	return 2000 * float64(m.Failures) / float64(m.Samples) * qualityConfidence(m, now, 20)
}

func qualityRecent(updated, now time.Time) bool {
	return !updated.IsZero() && now.Sub(updated) < 2*extension.QualityInterval
}

func makeChampionQualityCandidate(q extension.OutboundQuality, now time.Time, settings ChampionSettings) championQualityCandidate {
	if q.State == "" {
		q.State = extension.QualityUnknown
	}
	c := championQualityCandidate{quality: q, score: math.Inf(1), recentScore: math.Inf(1), rank: 2}
	if q.State == extension.QualityUnavailable || q.State == extension.QualityRecovering || !qualityLatencyKnown(q.Probe, now) {
		return c
	}
	c.known, c.rank = true, 0
	c.evidence = q.Probe.Updated
	scale := settings.HealthPingJitterScale
	c.probeCost = qualityMilliseconds(q.Probe.Mean) + scale*qualityMilliseconds(q.Probe.Deviation)
	c.recentProbeCost = qualityMilliseconds(q.Probe.RecentMean) + scale*qualityMilliseconds(q.Probe.RecentDeviation)
	if q.Probe.RecentMean <= 0 {
		c.recentProbeCost = c.probeCost
	}
	var recentRuntime, recentFailure, recentLoss float64
	for i, m := range []extension.QualityLatency{q.Connect, q.RTT} {
		if !qualityLatencyKnown(m, now) || m.Baseline <= 0 {
			continue
		}
		fullSamples := float64(20)
		if i == 1 {
			fullSamples = 3
		}
		weight := qualityConfidence(m, now, fullSamples)
		cost := math.Max(0, qualityMilliseconds(m.Mean-m.Baseline)+scale*qualityMilliseconds(m.Deviation-m.BaselineDeviation)) * weight
		recentCost := math.Max(0, qualityMilliseconds(m.RecentMean-m.Baseline)+scale*qualityMilliseconds(m.RecentDeviation-m.BaselineDeviation)) * weight
		c.runtimeCost = math.Max(c.runtimeCost, cost)
		recentRuntime = math.Max(recentRuntime, recentCost)
		if recentCost > 0 && qualityRecent(m.Updated, now) && m.Updated.After(c.runtimeEvidence) {
			c.runtimeEvidence = m.Updated
		}
	}
	for _, m := range []extension.QualityLatency{q.Probe, q.Connect} {
		cost := qualityFailureCost(m, now)
		c.failureCost = math.Max(c.failureCost, cost)
		if m.RecentFailures > 0 && qualityRecent(m.Updated, now) {
			recentFailure = math.Max(recentFailure, cost)
			if m.Updated.After(c.runtimeEvidence) {
				c.runtimeEvidence = m.Updated
			}
			if m.Failures >= 2 && m.Samples >= 20 && float64(m.Failures)/float64(m.Samples) >= .05 {
				c.rank = 1
			}
		}
	}
	if q.ClosedConnections > 0 {
		weight := math.Min(1, float64(q.ClosedConnections)/20) * qualityAgeWeight(q.ConnectionFailureUpdated, q.ConnectionFailureUpdated.Add(time.Minute), now)
		cost := 2000 * float64(q.FailedConnections) / float64(q.ClosedConnections) * weight
		c.failureCost = math.Max(c.failureCost, cost)
		if q.FailedConnections > 0 && qualityRecent(q.ConnectionFailureUpdated, now) {
			recentFailure = math.Max(recentFailure, cost)
			if q.ConnectionFailureUpdated.After(c.runtimeEvidence) {
				c.runtimeEvidence = q.ConnectionFailureUpdated
			}
			if q.ClosedConnections >= 20 && float64(q.FailedConnections)/float64(q.ClosedConnections) >= .05 {
				c.rank = 1
			}
		}
	}
	if q.Loss.Sent >= 50 && q.Loss.Buckets >= 3 {
		weight := math.Min(1, float64(q.Loss.Sent)/500) * qualityAgeWeight(q.Loss.Updated, q.Loss.Updated.Add(time.Minute), now)
		if q.Loss.Corrected {
			weight *= .5
		}
		c.lossCost = 500 * weight * math.Min(1, (float64(q.Loss.Lost)/float64(q.Loss.Sent))/.05)
		if q.Loss.RecentSent > 0 && qualityRecent(q.Loss.Updated, now) {
			recentLoss = 500 * weight * math.Min(1, (float64(q.Loss.RecentLost)/float64(q.Loss.RecentSent))/.05)
			if recentLoss > 0 && q.Loss.Updated.After(c.runtimeEvidence) {
				c.runtimeEvidence = q.Loss.Updated
			}
		}
	}
	if q.State == extension.QualitySuspect && !c.runtimeEvidence.IsZero() {
		c.rank = 1
	}
	if c.runtimeEvidence.After(c.evidence) {
		c.evidence = c.runtimeEvidence
	}
	c.score = c.probeCost + c.runtimeCost + math.Max(c.failureCost, c.lossCost)
	c.recentScore = c.recentProbeCost + recentRuntime + math.Max(recentFailure, recentLoss)
	return c
}

// qualityImprovementThreshold 是晋级所需的最小改善：max(20ms, 现任分数的 15%)。
func qualityImprovementThreshold(score float64) float64 { return math.Max(20, score*.15) }

func qualityOrdinaryPromotion(current, candidate championQualityCandidate) bool {
	if !current.known || !candidate.known {
		return false
	}
	if candidate.rank < current.rank {
		return !current.runtimeEvidence.IsZero()
	}
	if candidate.rank > current.rank {
		return false
	}
	return current.score-candidate.score >= qualityImprovementThreshold(current.score) &&
		current.recentScore-candidate.recentScore >= qualityImprovementThreshold(current.recentScore)
}

// qualityLatestLeads 用现任最新一次探测代替探测分量 P 后，判断挑战者是否仍领先晋级门槛。
// 探测间隔较长时窗口只有 2–3 个样本，单个慢样本会让 P 在整个有效期内偏高；最新样本恢复后不再计探测票。
// 最新一次探测失败或挑战者可靠性等级更好时直接支持挑战者。
func qualityLatestLeads(current, candidate championQualityCandidate) bool {
	probe := current.quality.Probe
	if probe.LastFailed || candidate.rank < current.rank {
		return true
	}
	latest := current.score - current.probeCost + qualityMilliseconds(probe.Latest)
	return latest-candidate.score >= qualityImprovementThreshold(latest)
}

// qualityRuntimeLed 判断运行时分量 D+max(F,L) 的差距本身（滚动分与最近分）是否达到晋级门槛，或挑战者可靠性等级更好。
// 现任实际 RTT 只略高于基线时，运行时证据与本次劣势无关，不能按 5s 评估周期计票。
func qualityRuntimeLed(current, candidate championQualityCandidate) bool {
	if candidate.rank < current.rank {
		return true
	}
	gap := current.score - current.probeCost - (candidate.score - candidate.probeCost)
	recentGap := current.recentScore - current.recentProbeCost - (candidate.recentScore - candidate.recentProbeCost)
	return gap >= qualityImprovementThreshold(current.score) && recentGap >= qualityImprovementThreshold(current.recentScore)
}

func qualityPreferredNear(preferred, best championQualityCandidate) bool {
	return preferred.known && best.known && preferred.rank == best.rank && preferred.score-best.score <= math.Max(10, best.score*.05)
}

// resetDuel 只清空计票；现任证据游标属于擂主任期，由 selectTag 清空。
// 否则类别变化、优势中断或评估中断后，已经计过票的同一个现任观测会被原样重读再计一票。
func (q *championQualityState) resetDuel() {
	q.challenger, q.kind, q.wins, q.challengerWins, q.votes = "", "", 0, 0, nil
	q.started, q.candidateEvidence = time.Time{}, time.Time{}
}

// selectTag 开始新的擂主任期。冷启动等待首选首个结果时会以同一 tag 反复重新选择，
// 只有擂主、代次或验证状态真正变化才更新切换时间，避免状态接口和日志把重复选择显示为切换并刷新冷却。
func (q *championQualityState) selectTag(tag string, candidate championQualityCandidate, now time.Time, tentative bool) {
	if tag != q.current || candidate.quality.Generation != q.generation || tentative != q.tentative {
		q.lastSwitch = now
	}
	q.current, q.generation, q.tentative = tag, candidate.quality.Generation, tentative
	q.currentEvidence, q.runtimeEvidence = time.Time{}, time.Time{}
	q.resetDuel()
}

// recordWin 只在同类挑战内累计票数，返回本次票型（none、probe、runtime、probe+runtime）与是否晋级。
// 挑战者轮换时保留总票数，否则两个相近的快节点交替领先时永远无法晋级。每票都要有与本次判断有关的新观测：
// 探测票要求现任有新探测样本、挑战者有新证据，且挑战者在现任最新一次探测下仍然领先；
// 运行时票要求现任有新的运行时证据，且运行时分量本身足以让挑战者领先。preferred_near 只计探测票，不比较最新样本。
// 一票只消耗它实际使用的观测，未用的观测留给后续同类票；现任的观测在整个擂主任期内最多支撑一票。
// 每个挑战者单独记录已计票证据，轮换回来的挑战者不能用旧证据再拿一票；
// 晋级目标自身还要有至少一半票，避免只在最后一轮领先的候选兑现其他挑战者的票。
func (q *championQualityState) recordWin(tag, kind string, current, candidate championQualityCandidate, now time.Time, threshold int) (string, bool) {
	if kind != q.kind {
		q.resetDuel()
		q.kind, q.started = kind, now
	}
	vote := q.votes[tag]
	q.challenger, q.challengerWins, q.candidateEvidence = tag, vote.wins, vote.evidence
	probeUpdated := current.quality.Probe.Updated
	probe := probeUpdated.After(q.currentEvidence) && candidate.evidence.After(vote.evidence) &&
		(kind == "preferred_near" || qualityLatestLeads(current, candidate))
	runtime := kind != "preferred_near" && current.runtimeEvidence.After(q.runtimeEvidence) && qualityRuntimeLed(current, candidate)
	if !probe && !runtime {
		return "none", false
	}
	if q.votes == nil {
		q.votes = make(map[string]championQualityVote)
	}
	vote.wins = min(threshold, vote.wins+1)
	voteText := "probe+runtime"
	if probe {
		vote.evidence, q.currentEvidence = candidate.evidence, probeUpdated
	} else {
		voteText = "runtime"
	}
	if runtime {
		q.runtimeEvidence = current.runtimeEvidence
	} else {
		voteText = "probe"
	}
	q.votes[tag] = vote
	q.wins = min(threshold, q.wins+1)
	q.challengerWins, q.candidateEvidence = vote.wins, vote.evidence
	return voteText, q.wins >= threshold && vote.wins >= championOwnVotesRequired(threshold) &&
		now.Sub(q.started) >= time.Duration(threshold)*extension.QualityInterval && now.Sub(q.lastSwitch) >= championQualityCooldown
}

// reselect 在没有已验证擂主、擂主故障或代次变化时重新选择，返回决策原因。
// wait 为真时首选还没有任何探测结果，先按未验证处理，不让先完成探测的其他候选决定首次选择。
func (q *championQualityState) reselect(tags []string, candidates map[string]championQualityCandidate, best, preferred, oldTag string, failover, wait bool, now time.Time) string {
	if best != "" && !wait {
		chosen := best
		if qualityPreferredNear(candidates[preferred], candidates[best]) {
			chosen = preferred
		}
		q.selectTag(chosen, candidates[chosen], now, false)
		if failover {
			return "quality_failover"
		}
		return "quality_initial_best"
	}
	// 无数据与已确认故障分开处理，绝不把 down 线路因缺样本复活。
	chosen := ""
	for _, tag := range tags {
		status := candidates[tag].quality.State
		if status == extension.QualityUnavailable || status == extension.QualityRecovering {
			continue
		}
		if chosen == "" || tag == oldTag || chosen != oldTag && tag == preferred {
			chosen = tag
		}
	}
	q.selectTag(chosen, candidates[chosen], now, chosen != "")
	if chosen == "" {
		return "all_candidates_dead"
	}
	return "quality_unverified"
}

// qualityChallenger 返回本轮挑战者、挑战类型与所需票数：普通晋级优先，首选接近最佳时由首选代替；否则检查首选回切。
func qualityChallenger(candidates map[string]championQualityCandidate, best, preferred, oldTag string, settings ChampionSettings) (string, string, int) {
	current := candidates[oldTag]
	if best != oldTag && qualityOrdinaryPromotion(current, candidates[best]) {
		if preferred != oldTag && qualityPreferredNear(candidates[preferred], candidates[best]) && qualityOrdinaryPromotion(current, candidates[preferred]) {
			return preferred, "quality_promoted", settings.CandidateObservationCount
		}
		return best, "quality_promoted", settings.CandidateObservationCount
	}
	if preferred != oldTag && qualityPreferredNear(candidates[preferred], candidates[best]) {
		return preferred, "preferred_near", settings.PreferredObservationCount
	}
	return "", "", 0
}

// qualityCandidates 为全部候选计算质量评分，并按可靠性等级与分数选出最佳候选，分数相同时保留现任。
// pending 表示首选已在观测范围内、未确认故障，但还没有任何探测结果（成功或失败）。
func qualityCandidates(tags []string, outbounds []extension.OutboundQuality, oldTag, preferred string, now time.Time, settings ChampionSettings) (map[string]championQualityCandidate, string, bool) {
	observed := make(map[string]extension.OutboundQuality, len(outbounds))
	for _, q := range outbounds {
		observed[q.Tag] = q
	}
	candidates := make(map[string]championQualityCandidate, len(tags))
	best := ""
	for _, tag := range tags {
		q := observed[tag]
		q.Tag = tag
		c := makeChampionQualityCandidate(q, now, settings)
		candidates[tag] = c
		if !c.known {
			continue
		}
		b := candidates[best]
		if best == "" || c.rank < b.rank || c.rank == b.rank && (c.score < b.score || c.score == b.score && tag == oldTag) {
			best = tag
		}
	}
	p, ok := observed[preferred]
	return candidates, best, ok && p.Probe.Samples == 0 && p.State != extension.QualityUnavailable && p.State != extension.QualityRecovering
}

func (s *ChampionStrategy) pickQuality(tags []string, snapshot extension.OutboundQualitySnapshot) string {
	settings := s.Settings.normalized()
	now := snapshot.Time
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	state := &s.qualityState
	// Version 单调递增；Epoch 来自墙钟，只在 Version 相同时用于丢弃并发拿到的旧周期快照。
	// 墙钟回拨后新样本会推进 Version，故障切换因此不会被冻结。
	if state.initialized && (snapshot.Version < state.lastVersion || snapshot.Version == state.lastVersion && snapshot.Epoch < state.lastEpoch) {
		tag := state.current
		if !hasCurrentCandidate(tags, tag) {
			tag = ""
		}
		s.mu.Unlock()
		return tag
	}
	oldTag := state.current
	exists := hasCurrentCandidate(tags, oldTag)
	var currentQuality extension.OutboundQuality
	for _, q := range snapshot.Outbounds {
		if q.Tag == oldTag {
			currentQuality = q
			break
		}
	}
	unavailable := exists && (currentQuality.State == extension.QualityUnavailable || currentQuality.State == extension.QualityRecovering)
	generationChanged := exists && currentQuality.Generation != 0 && state.generation != currentQuality.Generation
	// 稳态请求只读当前资格；同周期不重新分配评分表。故障与代次变化仍即时处理。
	if state.initialized && snapshot.Epoch == state.lastEpoch && exists && !unavailable && !generationChanged && (!state.tentative || snapshot.Version == state.lastVersion) {
		state.lastVersion = max(state.lastVersion, snapshot.Version)
		s.mu.Unlock()
		return oldTag
	}
	preferred := settings.PreferredTag
	if !hasCurrentCandidate(tags, preferred) {
		preferred = tags[0]
	}
	candidates, best, preferredPending := qualityCandidates(tags, snapshot.Outbounds, oldTag, preferred, now, settings)
	current := candidates[oldTag]
	if !state.lastEvaluation.IsZero() && now.Sub(state.lastEvaluation) > 2*extension.QualityInterval {
		state.resetDuel()
	}
	state.lastEpoch, state.lastVersion, state.lastEvaluation, state.initialized = snapshot.Epoch, snapshot.Version, now, true
	reason, vote := "quality_stable", "none"
	var decision championQualityState
	failover := unavailable || oldTag != "" && !exists
	if oldTag == "" || !exists || unavailable || state.tentative || generationChanged {
		// 冷启动时首选还没有任何探测结果，继续按未验证使用首选；故障切换与代次变化不等待。
		reason = state.reselect(tags, candidates, best, preferred, oldTag, failover, preferredPending && !failover && !generationChanged, now)
	} else if !current.known || best == "" {
		state.resetDuel()
		reason = "quality_evidence_missing"
	} else if challenger, kind, count := qualityChallenger(candidates, best, preferred, oldTag, settings); challenger == "" {
		state.resetDuel()
	} else {
		reason = "quality_observing"
		var promote bool
		vote, promote = state.recordWin(challenger, kind, current, candidates[challenger], now, count)
		decision = *state
		if promote {
			reason = kind
			state.selectTag(challenger, candidates[challenger], now, false)
		}
	}
	state.reason = reason
	selected := state.current
	if decision.challenger == "" {
		decision = *state
	}
	if settings.QualityMode == ChampionQualitySelect {
		s.lastTag = selected
	}
	s.mu.Unlock()
	s.logQualityDecision(settings.QualityMode, reason, vote, oldTag, selected, current, candidates[selected], candidates[decision.challenger], decision)
	return selected
}
