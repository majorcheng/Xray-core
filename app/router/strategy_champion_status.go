package router

import (
	"context"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/features/extension"
)

// ChampionStatus 是 metrics/debug vars 使用的只读快照，不参与选路。
// 指针数值在数据不足时为 null，避免把 unknown 误显示成 0。
type ChampionStatus struct {
	Mode                   string                         `json:"mode"`
	Current                string                         `json:"current"`
	QualityCurrent         string                         `json:"quality_current,omitempty"`
	Preferred              string                         `json:"preferred,omitempty"`
	State                  extension.OutboundQualityState `json:"state,omitempty"`
	Reason                 string                         `json:"reason,omitempty"`
	Challenger             string                         `json:"challenger,omitempty"`
	ChallengeKind          string                         `json:"challenge_kind,omitempty"`
	Wins                   int                            `json:"wins"`
	RequiredWins           int                            `json:"required_wins"`
	CandidateCount         int                            `json:"candidate_count"`
	Initialized            bool                           `json:"initialized"`
	Tentative              bool                           `json:"tentative"`
	SnapshotEpoch          int64                          `json:"snapshot_epoch,omitempty"`
	SnapshotVersion        uint64                         `json:"snapshot_version,omitempty"`
	QualitySnapshotEpoch   int64                          `json:"quality_snapshot_epoch,omitempty"`
	QualitySnapshotVersion uint64                         `json:"quality_snapshot_version,omitempty"`
	LastEvaluationAt       *time.Time                     `json:"last_evaluation_at,omitempty"`
	LastSwitchAt           *time.Time                     `json:"last_switch_at,omitempty"`
	ChallengeStartedAt     *time.Time                     `json:"challenge_started_at,omitempty"`
	CurrentEvidenceAt      *time.Time                     `json:"current_evidence_at,omitempty"`
	CandidateEvidenceAt    *time.Time                     `json:"candidate_evidence_at,omitempty"`
	RuntimeEvidenceAt      *time.Time                     `json:"runtime_evidence_at,omitempty"`
	CooldownRemainingMs    int64                          `json:"cooldown_remaining_ms"`
	Candidates             []ChampionCandidateStatus      `json:"candidates"`
}

type ChampionCandidateStatus struct {
	Tag                     string                         `json:"tag"`
	State                   extension.OutboundQualityState `json:"state"`
	Known                   bool                           `json:"known"`
	ReliabilityRank         int                            `json:"reliability_rank"`
	Reason                  string                         `json:"reason,omitempty"`
	Generation              uint64                         `json:"generation,omitempty"`
	ScoreMs                 *float64                       `json:"score_ms"`
	RecentScoreMs           *float64                       `json:"recent_score_ms"`
	ProbeMs                 *float64                       `json:"probe_ms"`
	ProbeDeviationMs        *float64                       `json:"probe_deviation_ms"`
	TransportRTTMs          *float64                       `json:"transport_rtt_ms"`
	TransportRTTDeviationMs *float64                       `json:"transport_rtt_deviation_ms"`
	LossKnown               bool                           `json:"loss_known"`
	LossSent                uint64                         `json:"loss_sent"`
	LossEstimated           uint64                         `json:"loss_estimated"`
	LossBuckets             int                            `json:"loss_buckets"`
	LossCorrected           bool                           `json:"loss_corrected"`
	ClosedConnections       uint64                         `json:"closed_connections"`
	FailedConnections       uint64                         `json:"failed_connections"`
	EvidenceAt              *time.Time                     `json:"evidence_at,omitempty"`
	RuntimeEvidenceAt       *time.Time                     `json:"runtime_evidence_at,omitempty"`
	ProbeUpdated            *time.Time                     `json:"probe_updated,omitempty"`
	ProbeFreshUntil         *time.Time                     `json:"probe_fresh_until,omitempty"`
	TransportRTTUpdated     *time.Time                     `json:"transport_rtt_updated,omitempty"`
	LossUpdated             *time.Time                     `json:"loss_updated,omitempty"`
	Alive                   *bool                          `json:"alive,omitempty"`
	LegacyDelayMs           *int64                         `json:"legacy_delay_ms,omitempty"`
}

func championFloat(value float64) *float64 { return &value }

func championInt64(value int64) *int64 { return &value }

func championBool(value bool) *bool { return &value }

func championTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func championCandidateStatus(c championQualityCandidate, legacy *observatory.OutboundStatus, now time.Time) ChampionCandidateStatus {
	status := ChampionCandidateStatus{
		Tag:                 c.quality.Tag,
		State:               c.quality.State,
		Known:               c.known,
		ReliabilityRank:     c.rank,
		Reason:              c.quality.Reason,
		Generation:          c.quality.Generation,
		LossSent:            c.quality.Loss.Sent,
		LossEstimated:       c.quality.Loss.Lost,
		LossBuckets:         c.quality.Loss.Buckets,
		LossCorrected:       c.quality.Loss.Corrected,
		ClosedConnections:   c.quality.ClosedConnections,
		FailedConnections:   c.quality.FailedConnections,
		EvidenceAt:          championTime(c.evidence),
		RuntimeEvidenceAt:   championTime(c.runtimeEvidence),
		ProbeUpdated:        championTime(c.quality.Probe.Updated),
		ProbeFreshUntil:     championTime(c.quality.Probe.FreshUntil),
		TransportRTTUpdated: championTime(c.quality.RTT.Updated),
		LossUpdated:         championTime(c.quality.Loss.Updated),
	}
	if c.known {
		status.ScoreMs = championFloat(c.score)
		status.RecentScoreMs = championFloat(c.recentScore)
	}
	if qualityLatencyKnown(c.quality.Probe, now) {
		status.ProbeMs = championFloat(qualityMilliseconds(c.quality.Probe.Mean))
		status.ProbeDeviationMs = championFloat(qualityMilliseconds(c.quality.Probe.Deviation))
	}
	if qualityLatencyKnown(c.quality.RTT, now) {
		status.TransportRTTMs = championFloat(qualityMilliseconds(c.quality.RTT.Mean))
		status.TransportRTTDeviationMs = championFloat(qualityMilliseconds(c.quality.RTT.Deviation))
	}
	status.LossKnown = c.quality.Loss.Sent >= 50 && c.quality.Loss.Buckets >= 3
	if legacy != nil {
		status.Alive = championBool(legacy.Alive)
		status.LegacyDelayMs = championInt64(legacy.Delay)
	}
	return status
}

func (s *ChampionStrategy) championStatus(tags []string) ChampionStatus {
	settings := s.Settings.normalized()
	status := ChampionStatus{
		Mode: settings.QualityMode, Preferred: settings.PreferredTag,
		CandidateCount: len(tags), Candidates: make([]ChampionCandidateStatus, 0, len(tags)),
	}

	s.mu.Lock()
	actualCurrent := s.lastTag
	qualityState := s.qualityState
	quality := s.quality
	observatoryFeature := s.observatory
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	status.Current = actualCurrent
	status.QualityCurrent = qualityState.current
	status.Reason = qualityState.reason
	status.Challenger = qualityState.challenger
	status.ChallengeKind = qualityState.kind
	status.Wins = qualityState.wins
	status.Initialized = qualityState.initialized
	status.Tentative = qualityState.tentative
	status.SnapshotEpoch = qualityState.lastEpoch
	status.SnapshotVersion = qualityState.lastVersion
	status.LastEvaluationAt = championTime(qualityState.lastEvaluation)
	status.LastSwitchAt = championTime(qualityState.lastSwitch)
	status.ChallengeStartedAt = championTime(qualityState.started)
	status.CurrentEvidenceAt = championTime(qualityState.currentEvidence)
	status.CandidateEvidenceAt = championTime(qualityState.candidateEvidence)
	status.RuntimeEvidenceAt = championTime(qualityState.runtimeEvidence)
	if qualityState.challenger != "" {
		status.RequiredWins = settings.CandidateObservationCount
		if qualityState.kind == "preferred_near" {
			status.RequiredWins = settings.PreferredObservationCount
		}
	}
	if !qualityState.lastSwitch.IsZero() {
		remaining := championQualityCooldown - time.Since(qualityState.lastSwitch)
		if remaining > 0 {
			status.CooldownRemainingMs = remaining.Milliseconds()
		}
	}

	var qualitySnapshot extension.OutboundQualitySnapshot
	qualityEnabled := quality != nil && settings.QualityMode != ChampionQualityOff
	if qualityEnabled {
		qualitySnapshot = quality.GetOutboundQuality()
		status.QualitySnapshotEpoch = qualitySnapshot.Epoch
		status.QualitySnapshotVersion = qualitySnapshot.Version
	}
	now := qualitySnapshot.Time
	if now.IsZero() {
		now = time.Now()
	}
	observed := make(map[string]extension.OutboundQuality, len(qualitySnapshot.Outbounds))
	for _, item := range qualitySnapshot.Outbounds {
		observed[item.Tag] = item
	}
	legacy := make(map[string]*observatory.OutboundStatus)
	if observatoryFeature != nil {
		if report, err := observatoryFeature.GetObservation(ctx); err == nil {
			if result, ok := report.(*observatory.ObservationResult); ok {
				for _, item := range result.Status {
					legacy[item.OutboundTag] = item
				}
			}
		}
	}

	for _, tag := range tags {
		item := observed[tag]
		item.Tag = tag
		candidate := makeChampionQualityCandidate(item, now, settings)
		if !qualityEnabled {
			candidate = championLegacyCandidate(tag, legacy[tag])
		}
		if candidate.quality.State == "" {
			candidate.quality.State = extension.QualityUnknown
		}
		status.Candidates = append(status.Candidates, championCandidateStatus(candidate, legacy[tag], now))
		if tag == actualCurrent {
			status.State = candidate.quality.State
		}
	}
	if status.State == "" {
		status.State = extension.QualityUnknown
	}
	return status
}

func championLegacyCandidate(tag string, item *observatory.OutboundStatus) championQualityCandidate {
	candidate := championQualityCandidate{quality: extension.OutboundQuality{Tag: tag, State: extension.QualityUnknown}, rank: 2}
	if item == nil {
		return candidate
	}
	candidate.quality.State = extension.QualityAvailable
	if !item.Alive {
		candidate.quality.State = extension.QualityUnavailable
	}
	candidate.known = item.Alive
	candidate.rank = 0
	if item.Alive {
		candidate.score = float64(item.Delay)
		candidate.recentScore = candidate.score
		candidate.evidence = time.Unix(item.LastSeenTime, 0)
	}
	return candidate
}
