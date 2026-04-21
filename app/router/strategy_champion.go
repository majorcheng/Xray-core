package router

import (
	"context"
	"sync"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
)

const (
	championDefaultDelay  int64 = 500
	championInfiniteDelay int64 = 1<<63 - 1
	championUnknownDelay  int64 = -1
)

func normalizeChampionDelay(delay int64) int64 {
	if delay == championInfiniteDelay {
		return championUnknownDelay
	}
	return delay
}

func championTagForLog(tag string) string {
	if tag == "" {
		return "none"
	}
	return tag
}

// ChampionStrategy keeps a stable "champion" outbound while avoiding frequent switches.
// 它优先保住当前擂主，并且只统计不同观测快照上的连续胜场，避免单次抖动被高并发请求放大。
type ChampionStrategy struct {
	FallbackTag string
	Settings    ChampionSettings

	ctx         context.Context
	observatory extension.Observatory

	mu                 sync.Mutex
	index              int
	lastTag            string
	duelLossTag        string
	duelLossStreak     int
	duelObservationKey championDuelKey
}

func (s *ChampionStrategy) InjectContext(ctx context.Context) {
	s.ctx = ctx
	s.Settings = s.Settings.normalized()
	// champion 可以在无 observatory 时退化为普通轮询，因此使用 OptionalFeatures。
	common.Must(core.OptionalFeatures(s.ctx, func(observatory extension.Observatory) error {
		s.observatory = observatory
		return nil
	}))
}

func (s *ChampionStrategy) GetPrincipleTarget(strings []string) []string {
	return strings
}

func (s *ChampionStrategy) logChampionSwitch(reason, oldTag, newTag string, oldDelayMs, newDelayMs int64) {
	if oldTag == newTag {
		return
	}
	if reason == "" {
		reason = "champion_changed"
	}
	errors.LogWarning(s.ctx,
		"champion switched",
		" reason=", reason,
		" old_tag=", championTagForLog(oldTag),
		" new_tag=", championTagForLog(newTag),
		" old_delay_ms=", oldDelayMs,
		" new_delay_ms=", newDelayMs,
	)
}

func (s *ChampionStrategy) PickOutbound(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	if obs, ok := s.loadObservation(tags); ok {
		return s.pickObserved(tags, obs)
	}
	return s.pickRoundRobinFallback(tags)
}

func (s *ChampionStrategy) loadObservation(tags []string) (*championObservation, bool) {
	if s.observatory == nil {
		return nil, false
	}
	observeReport, err := s.observatory.GetObservation(s.ctx)
	if err != nil {
		return nil, false
	}
	result, ok := observeReport.(*observatory.ObservationResult)
	if !ok {
		return nil, false
	}
	return newChampionObservation(tags, result, s.Settings.normalized()), true
}

func (s *ChampionStrategy) pickObserved(tags []string, obs *championObservation) string {
	decision := s.selectObserved(obs)
	if decision.allCandidatesDead {
		return s.commitAllCandidatesDead(obs, decision.reason)
	}
	if decision.selectedTag == "" {
		return s.pickRoundRobinFallback(tags)
	}
	return s.commitChampionSelection(obs, decision)
}

func (s *ChampionStrategy) selectObserved(obs *championObservation) championDecision {
	preferredTag, preferredDelay := obs.preferred()
	anchorTag, anchorDelay := obs.anchor(s.currentChampion())
	bestTag, bestDelay := obs.best()
	if bestDelay == championInfiniteDelay {
		return championDecision{allCandidatesDead: true, reason: "all_candidates_dead"}
	}
	if anchorDelay == championInfiniteDelay {
		s.resetDuel()
		return championDecision{
			selectedTag: bestTag,
			reason:      "best_selected_from_non_anchor",
			newDelayMs:  normalizeChampionDelay(bestDelay),
		}
	}
	if anchorTag == preferredTag {
		return s.decideChallenge(obs, anchorTag, anchorDelay, bestTag, bestDelay)
	}
	return s.decidePreferredReclaim(obs, preferredTag, preferredDelay, anchorTag, anchorDelay)
}

func (s *ChampionStrategy) decideChallenge(obs *championObservation, anchorTag string, anchorDelay int64, bestTag string, bestDelay int64) championDecision {
	if !obs.challengerWins(anchorTag, anchorDelay, bestTag, bestDelay) {
		s.resetDuel()
		return championDecision{selectedTag: anchorTag}
	}
	key := obs.duelKey(anchorTag, anchorDelay, bestTag, bestDelay)
	if !s.recordDuelWin(bestTag, key, obs.settings.CandidateObservationCount) {
		return championDecision{selectedTag: anchorTag}
	}
	return championDecision{
		selectedTag: bestTag,
		reason:      "challenger_promoted",
		oldDelayMs:  normalizeChampionDelay(anchorDelay),
		newDelayMs:  normalizeChampionDelay(bestDelay),
	}
}

func (s *ChampionStrategy) decidePreferredReclaim(obs *championObservation, preferredTag string, preferredDelay int64, anchorTag string, anchorDelay int64) championDecision {
	if !obs.preferredCanReclaim(preferredTag, preferredDelay, anchorTag, anchorDelay) {
		s.resetDuel()
		return championDecision{selectedTag: anchorTag}
	}
	key := obs.duelKey(anchorTag, anchorDelay, preferredTag, preferredDelay)
	if !s.recordDuelWin(preferredTag, key, obs.settings.PreferredObservationCount) {
		return championDecision{selectedTag: anchorTag}
	}
	return championDecision{
		selectedTag: preferredTag,
		reason:      "preferred_reclaimed",
		oldDelayMs:  normalizeChampionDelay(anchorDelay),
		newDelayMs:  normalizeChampionDelay(preferredDelay),
	}
}

func (s *ChampionStrategy) currentChampion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTag
}

func (s *ChampionStrategy) pickRoundRobinFallback(tags []string) string {
	s.mu.Lock()
	oldTag := s.lastTag
	selectedTag := tags[s.index%len(tags)]
	s.index = (s.index + 1) % len(tags)
	s.lastTag = selectedTag
	s.clearDuelLocked()
	s.mu.Unlock()

	s.logChampionSwitch("round_robin_fallback", oldTag, selectedTag, championUnknownDelay, championUnknownDelay)
	return selectedTag
}

func (s *ChampionStrategy) commitAllCandidatesDead(obs *championObservation, reason string) string {
	s.mu.Lock()
	oldTag := s.lastTag
	s.lastTag = ""
	s.clearDuelLocked()
	s.mu.Unlock()

	oldDelayMs := championUnknownDelay
	if oldTag != "" {
		oldDelayMs = normalizeChampionDelay(obs.delayScore(oldTag))
	}
	s.logChampionSwitch(reason, oldTag, "", oldDelayMs, championUnknownDelay)
	return ""
}

func (s *ChampionStrategy) commitChampionSelection(obs *championObservation, decision championDecision) string {
	s.mu.Lock()
	oldTag := s.lastTag
	s.lastTag = decision.selectedTag
	s.mu.Unlock()

	oldDelayMs := decision.oldDelayMs
	if oldDelayMs == championUnknownDelay && oldTag != "" {
		oldDelayMs = normalizeChampionDelay(obs.delayScore(oldTag))
	}
	newDelayMs := decision.newDelayMs
	if newDelayMs == championUnknownDelay && decision.selectedTag != "" {
		newDelayMs = normalizeChampionDelay(obs.delayScore(decision.selectedTag))
	}
	s.logChampionSwitch(decision.reason, oldTag, decision.selectedTag, oldDelayMs, newDelayMs)
	return decision.selectedTag
}

func (s *ChampionStrategy) recordDuelWin(tag string, key championDuelKey, threshold int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.duelLossTag != tag {
		s.duelLossTag = tag
		s.duelLossStreak = 1
		s.duelObservationKey = key
		return threshold <= 1
	}
	if s.duelObservationKey == key {
		return s.duelLossStreak >= threshold
	}
	if s.duelLossStreak < threshold {
		s.duelLossStreak++
	}
	s.duelObservationKey = key
	return s.duelLossStreak >= threshold
}

func (s *ChampionStrategy) resetDuel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearDuelLocked()
}

func (s *ChampionStrategy) clearDuelLocked() {
	s.duelLossTag = ""
	s.duelLossStreak = 0
	s.duelObservationKey = championDuelKey{}
}
