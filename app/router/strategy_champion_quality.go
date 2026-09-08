package router

import (
	"fmt"
	"math"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/features/extension"
)

const championQualityCooldown = 30 * time.Second

type championQualityState struct {
	current                            string
	generation                         uint64
	tentative                          bool
	initialized                        bool
	lastEpoch                          int64
	lastVersion                        uint64
	lastEvaluation                     time.Time
	lastSwitch                         time.Time
	challenger, kind                   string
	wins                               int
	started                            time.Time
	currentEvidence, candidateEvidence time.Time
	runtimeEvidence                    time.Time
}

type championQualityCandidate struct {
	quality                                       extension.OutboundQuality
	known                                         bool
	rank                                          int
	score, recentScore                            float64
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
	recentProbe := qualityMilliseconds(q.Probe.RecentMean) + scale*qualityMilliseconds(q.Probe.RecentDeviation)
	if q.Probe.RecentMean <= 0 {
		recentProbe = c.probeCost
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
	c.recentScore = recentProbe + recentRuntime + math.Max(recentFailure, recentLoss)
	return c
}

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
	threshold := math.Max(20, current.score*.15)
	return current.score-candidate.score >= threshold && current.recentScore-candidate.recentScore >= math.Max(20, current.recentScore*.15)
}

func qualityPreferredNear(preferred, best championQualityCandidate) bool {
	return preferred.known && best.known && preferred.rank == best.rank && preferred.score-best.score <= math.Max(10, best.score*.05)
}

func (q *championQualityState) resetDuel() {
	q.challenger, q.kind, q.wins = "", "", 0
	q.started, q.currentEvidence, q.candidateEvidence, q.runtimeEvidence = time.Time{}, time.Time{}, time.Time{}, time.Time{}
}

func (q *championQualityState) selectTag(tag string, candidate championQualityCandidate, now time.Time, tentative bool) {
	q.current, q.generation, q.tentative = tag, candidate.quality.Generation, tentative
	q.lastSwitch = now
	q.resetDuel()
}

func (q *championQualityState) recordWin(tag, kind string, current, candidate championQualityCandidate, now time.Time, threshold int) bool {
	if tag != q.challenger || kind != q.kind {
		q.resetDuel()
		q.challenger, q.kind, q.started = tag, kind, now
	}
	paired := current.evidence.After(q.currentEvidence) && candidate.evidence.After(q.candidateEvidence)
	runtime := kind != "preferred_near" && current.runtimeEvidence.After(q.runtimeEvidence)
	if q.wins == 0 || paired || runtime {
		q.wins = min(threshold, q.wins+1)
		q.currentEvidence, q.candidateEvidence, q.runtimeEvidence = current.evidence, candidate.evidence, current.runtimeEvidence
	} else {
		return false
	}
	return q.wins >= threshold && now.Sub(q.started) >= time.Duration(threshold)*extension.QualityInterval && now.Sub(q.lastSwitch) >= championQualityCooldown
}

func (s *ChampionStrategy) pickQuality(tags []string, snapshot extension.OutboundQualitySnapshot) string {
	settings := s.Settings.normalized()
	now := snapshot.Time
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	state := &s.qualityState
	if state.initialized && (snapshot.Epoch < state.lastEpoch || snapshot.Version < state.lastVersion) {
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
	observed := make(map[string]extension.OutboundQuality, len(snapshot.Outbounds))
	for _, q := range snapshot.Outbounds {
		observed[q.Tag] = q
	}
	candidates := make(map[string]championQualityCandidate, len(tags))
	for _, tag := range tags {
		q := observed[tag]
		q.Tag = tag
		candidates[tag] = makeChampionQualityCandidate(q, now, settings)
	}
	current := candidates[oldTag]
	preferred := settings.PreferredTag
	if !hasCurrentCandidate(tags, preferred) {
		preferred = tags[0]
	}
	best := ""
	for _, tag := range tags {
		c := candidates[tag]
		if !c.known {
			continue
		}
		b := candidates[best]
		if best == "" || c.rank < b.rank || c.rank == b.rank && (c.score < b.score || c.score == b.score && tag == oldTag) {
			best = tag
		}
	}
	if !state.lastEvaluation.IsZero() && now.Sub(state.lastEvaluation) > 2*extension.QualityInterval {
		state.resetDuel()
	}
	state.lastEpoch, state.lastVersion, state.lastEvaluation, state.initialized = snapshot.Epoch, snapshot.Version, now, true
	reason := "quality_stable"
	var decision championQualityState
	if oldTag == "" || !exists || unavailable || state.tentative || generationChanged {
		chosen := best
		if chosen != "" {
			if qualityPreferredNear(candidates[preferred], candidates[best]) {
				chosen = preferred
			}
			reason = "quality_initial_best"
			if unavailable || oldTag != "" && !exists {
				reason = "quality_failover"
			}
			state.selectTag(chosen, candidates[chosen], now, false)
		} else {
			// 无数据与已确认故障分开处理，绝不把 down 线路因缺样本复活。
			for _, tag := range tags {
				status := candidates[tag].quality.State
				if status == extension.QualityUnavailable || status == extension.QualityRecovering {
					continue
				}
				if chosen == "" || tag == oldTag || chosen != oldTag && tag == preferred {
					chosen = tag
				}
			}
			reason = "quality_unverified"
			if chosen == "" {
				reason = "all_candidates_dead"
			}
			state.selectTag(chosen, candidates[chosen], now, chosen != "")
		}
	} else if !current.known || best == "" {
		state.resetDuel()
		reason = "quality_evidence_missing"
	} else {
		challenger, kind, count := "", "", settings.CandidateObservationCount
		if best != oldTag && qualityOrdinaryPromotion(current, candidates[best]) {
			challenger, kind = best, "quality_promoted"
			if preferred != oldTag && qualityPreferredNear(candidates[preferred], candidates[best]) && qualityOrdinaryPromotion(current, candidates[preferred]) {
				challenger = preferred
			}
		} else if preferred != oldTag && qualityPreferredNear(candidates[preferred], candidates[best]) {
			challenger, kind, count = preferred, "preferred_near", settings.PreferredObservationCount
		}
		if challenger == "" {
			state.resetDuel()
		} else {
			reason = "quality_observing"
			promote := state.recordWin(challenger, kind, current, candidates[challenger], now, count)
			decision = *state
			if promote {
				reason = kind
				state.selectTag(challenger, candidates[challenger], now, false)
			}
		}
	}
	selected := state.current
	if decision.challenger == "" {
		decision = *state
	}
	if settings.QualityMode == ChampionQualitySelect {
		s.lastTag = selected
	}
	s.mu.Unlock()
	s.logQualityDecision(settings.QualityMode, reason, oldTag, selected, current, candidates[selected], candidates[decision.challenger], decision)
	return selected
}

func qualityScoreText(c championQualityCandidate) string {
	if !c.known {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", c.score)
}

func qualityLatencyText(m extension.QualityLatency, now time.Time) string {
	if !qualityLatencyKnown(m, now) {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", qualityMilliseconds(m.Mean))
}

func (s *ChampionStrategy) logQualityDecision(mode, reason, oldTag, newTag string, old, next, challenger championQualityCandidate, decision championQualityState) {
	log := errors.LogDebug
	if oldTag != newTag {
		log = errors.LogInfo
		if mode == ChampionQualitySelect {
			log = errors.LogWarning
		}
	}
	now := decision.lastEvaluation
	count := s.Settings.normalized().CandidateObservationCount
	if decision.kind == "preferred_near" {
		count = s.Settings.normalized().PreferredObservationCount
	}
	threshold := "unknown"
	if old.known {
		threshold = fmt.Sprintf("%.2f", math.Max(20, old.score*.15))
	}
	log(s.ctx, "champion quality", " mode=", mode, " reason=", reason,
		" old_tag=", championTagForLog(oldTag), " new_tag=", championTagForLog(newTag),
		" old_score_ms=", qualityScoreText(old), " new_score_ms=", qualityScoreText(next),
		" state=", next.quality.State, " old_state=", old.quality.State, " old_reason=", old.quality.Reason,
		" reliability_rank=", next.rank, " old_reliability_rank=", old.rank, " generation=", next.quality.Generation,
		" probe_ms=", qualityLatencyText(next.quality.Probe, now), " probe_updated=", next.quality.Probe.Updated, " probe_fresh_until=", next.quality.Probe.FreshUntil,
		" transport_rtt_ms=", qualityLatencyText(next.quality.RTT, now), " rtt_updated=", next.quality.RTT.Updated,
		" score_components_ms[P,D,F,L]=", fmt.Sprintf("[%.2f,%.2f,%.2f,%.2f]", next.probeCost, next.runtimeCost, next.failureCost, next.lossCost),
		" old_components_ms[P,D,F,L]=", fmt.Sprintf("[%.2f,%.2f,%.2f,%.2f]", old.probeCost, old.runtimeCost, old.failureCost, old.lossCost),
		" sender=client", " loss_sent=", next.quality.Loss.Sent,
		" loss_estimated=", next.quality.Loss.Lost, " loss_corrected=", next.quality.Loss.Corrected,
		" loss_buckets=", next.quality.Loss.Buckets, " loss_updated=", next.quality.Loss.Updated,
		" challenger=", championTagForLog(decision.challenger), " challenger_score_ms=", qualityScoreText(challenger),
		" improvement_required_ms=", threshold,
		" evidence_at=", next.quality.Updated, " wins=", decision.wins, " required_wins=", count,
		" cooldown_remaining=", max(time.Duration(0), championQualityCooldown-now.Sub(decision.lastSwitch)))
}
