package router

import (
	"fmt"
	"strconv"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/features/extension"
)

func qualityScoreText(c championQualityCandidate) string {
	if c.quality.Tag == "" {
		return "none"
	}
	if !c.known {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", c.score)
}

func qualityComponentsText(c championQualityCandidate) string {
	if c.quality.Tag == "" {
		return "none"
	}
	if !c.known {
		return "unknown"
	}
	return fmt.Sprintf("[%.2f,%.2f,%.2f,%.2f]", c.probeCost, c.runtimeCost, c.failureCost, c.lossCost)
}

// qualityCandidateText 对不在候选集的擂主输出 none，避免零值被误读为真实状态。
func qualityCandidateText(c championQualityCandidate, value any) any {
	if c.quality.Tag == "" {
		return "none"
	}
	return value
}

// qualityTimeText 输出不含空格的毫秒级 RFC3339 时间，保证日志可按 key=value 解析。
func qualityTimeText(t time.Time) string {
	if t.IsZero() {
		return "none"
	}
	return t.Format("2006-01-02T15:04:05.000Z07:00")
}

// qualityFailureText 加引号输出最近一次探测失败原因；该原因不会在恢复后清空。
func qualityFailureText(reason string) string {
	if reason == "" {
		return "none"
	}
	return strconv.Quote(reason)
}

func qualityLatencyText(m extension.QualityLatency, now time.Time) string {
	if !qualityLatencyKnown(m, now) {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", qualityMilliseconds(m.Mean))
}

func (s *ChampionStrategy) logQualityDecision(mode, reason, vote, oldTag, newTag string, old, next, challenger championQualityCandidate, decision championQualityState) {
	log := errors.LogDebug
	if oldTag != newTag {
		log = errors.LogInfo
		// 首次选择不是故障切换，只有 select 模式下替换已有擂主才告警。
		if mode == ChampionQualitySelect && oldTag != "" {
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
		threshold = fmt.Sprintf("%.2f", qualityImprovementThreshold(old.score))
	}
	log(s.ctx, "champion quality", " balancer=", championTagForLog(s.BalancerTag), " mode=", mode, " reason=", reason,
		" old_tag=", championTagForLog(oldTag), " new_tag=", championTagForLog(newTag),
		" old_score_ms=", qualityScoreText(old), " new_score_ms=", qualityScoreText(next),
		" state=", qualityCandidateText(next, next.quality.State), " old_state=", qualityCandidateText(old, old.quality.State),
		" old_last_failure=", qualityCandidateText(old, qualityFailureText(old.quality.Reason)),
		" reliability_rank=", qualityCandidateText(next, next.rank), " old_reliability_rank=", qualityCandidateText(old, old.rank),
		" generation=", next.quality.Generation,
		" probe_ms=", qualityLatencyText(next.quality.Probe, now), " probe_updated=", qualityTimeText(next.quality.Probe.Updated),
		" probe_fresh_until=", qualityTimeText(next.quality.Probe.FreshUntil),
		" transport_rtt_ms=", qualityLatencyText(next.quality.RTT, now), " rtt_updated=", qualityTimeText(next.quality.RTT.Updated),
		" score_components_ms[P,D,F,L]=", qualityComponentsText(next), " old_components_ms[P,D,F,L]=", qualityComponentsText(old),
		" sender=client", " loss_sent=", next.quality.Loss.Sent,
		" loss_estimated=", next.quality.Loss.Lost, " loss_corrected=", next.quality.Loss.Corrected,
		" loss_buckets=", next.quality.Loss.Buckets, " loss_updated=", qualityTimeText(next.quality.Loss.Updated),
		" challenger=", championTagForLog(decision.challenger), " challenger_score_ms=", qualityScoreText(challenger),
		" improvement_required_ms=", threshold,
		" evidence_at=", qualityTimeText(next.evidence),
		" current_evidence_at=", qualityTimeText(decision.currentEvidence), " candidate_evidence_at=", qualityTimeText(decision.candidateEvidence),
		" runtime_evidence_at=", qualityTimeText(decision.runtimeEvidence),
		" vote=", vote, " wins=", decision.wins, " required_wins=", count,
		" challenger_wins=", decision.challengerWins, " challenger_required_wins=", championOwnVotesRequired(count),
		" cooldown_remaining=", max(time.Duration(0), championQualityCooldown-now.Sub(decision.lastSwitch)).Round(time.Millisecond))
}
