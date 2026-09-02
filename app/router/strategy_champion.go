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

// ChampionStrategy keeps a "champion" outbound while avoiding frequent switches.
// 它优先保住当前擂主，只有在观测结果连续满足阈值时才允许挑战者上位或 preferred 夺回。
type ChampionStrategy struct {
	FallbackTag string

	ctx         context.Context
	observatory extension.Observatory

	mu             sync.Mutex
	index          int
	lastTag        string
	duelLossTag    string
	duelLossStreak int
}

func (s *ChampionStrategy) InjectContext(ctx context.Context) {
	s.ctx = ctx
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

	var selectedTag string
	switchReason := ""
	oldDelayMs := championUnknownDelay
	newDelayMs := championUnknownDelay
	allCandidatesDead := false
	getDelay := func(string) int64 {
		return championUnknownDelay
	}

	if s.observatory != nil {
		observeReport, err := s.observatory.GetObservation(s.ctx)
		if err == nil {
			if result, ok := observeReport.(*observatory.ObservationResult); ok {
				statusMap := make(map[string]*observatory.OutboundStatus, len(result.Status))
				for _, outboundStatus := range result.Status {
					statusMap[outboundStatus.OutboundTag] = outboundStatus
				}

				candidateSet := make(map[string]struct{}, len(tags))
				for _, candidate := range tags {
					candidateSet[candidate] = struct{}{}
				}

				getDelay = func(tag string) int64 {
					stat, found := statusMap[tag]
					if !found {
						return championDefaultDelay
					}
					if stat.Alive {
						return stat.Delay
					}
					return championInfiniteDelay
				}

				preferredTag := tags[0]
				preferredDelay := getDelay(preferredTag)

				anchorTag := preferredTag
				anchorDelay := preferredDelay

				s.mu.Lock()
				last := s.lastTag
				s.mu.Unlock()

				// 只允许使用当前候选集合中的 lastTag，避免返回失效 tag。
				if _, ok := candidateSet[last]; ok {
					lastDelay := getDelay(last)
					if lastDelay != championInfiniteDelay {
						anchorTag = last
						anchorDelay = lastDelay
					}
				}

				bestTag := ""
				bestDelay := championInfiniteDelay
				for _, candidate := range tags {
					delay := getDelay(candidate)
					if delay < bestDelay {
						bestDelay = delay
						bestTag = candidate
					}
				}

				// 全部候选都被观测为 dead，返回空让 Balancer 走 fallbackTag。
				if bestDelay == championInfiniteDelay {
					allCandidatesDead = true
					switchReason = "all_candidates_dead"
				} else if anchorDelay != championInfiniteDelay {
					selectedTag = anchorTag

					if anchorTag == preferredTag {
						// preferred 仍是擂主时，只有足够明显更优且差距足够大，挑战者才允许连续抢位。
						challengerWins := bestTag != "" && bestTag != anchorTag &&
							bestDelay < (anchorDelay*4/7) &&
							bestDelay < (anchorDelay-100)

						if challengerWins {
							s.mu.Lock()
							if s.duelLossTag == bestTag {
								if s.duelLossStreak < 3 {
									s.duelLossStreak++
								}
							} else {
								s.duelLossTag = bestTag
								s.duelLossStreak = 1
							}
							shouldSwitch := s.duelLossTag == bestTag && s.duelLossStreak >= 3
							s.mu.Unlock()

							if shouldSwitch {
								selectedTag = bestTag
								switchReason = "challenger_promoted"
								oldDelayMs = normalizeChampionDelay(anchorDelay)
								newDelayMs = normalizeChampionDelay(bestDelay)
							}
						} else {
							s.mu.Lock()
							s.duelLossTag = ""
							s.duelLossStreak = 0
							s.mu.Unlock()
						}
					} else {
						// preferred 夺回更保守：既要保持明显更优，也要与当前锚点足够接近，避免远距离抖动回切。
						reclaimWins := preferredTag != "" &&
							preferredDelay != championInfiniteDelay &&
							preferredTag != anchorTag
						if reclaimWins {
							delta := anchorDelay - preferredDelay
							if delta < 0 {
								delta = -delta
							}
							reclaimWins = anchorDelay > (preferredDelay*4/7) && delta < 100
						}

						if reclaimWins {
							s.mu.Lock()
							if s.duelLossTag == preferredTag {
								if s.duelLossStreak < 3 {
									s.duelLossStreak++
								}
							} else {
								s.duelLossTag = preferredTag
								s.duelLossStreak = 1
							}
							shouldReclaim := s.duelLossTag == preferredTag && s.duelLossStreak >= 3
							s.mu.Unlock()

							if shouldReclaim {
								selectedTag = preferredTag
								switchReason = "preferred_reclaimed"
								oldDelayMs = normalizeChampionDelay(anchorDelay)
								newDelayMs = normalizeChampionDelay(preferredDelay)
							}
						} else {
							s.mu.Lock()
							s.duelLossTag = ""
							s.duelLossStreak = 0
							s.mu.Unlock()
						}
					}
				} else {
					s.mu.Lock()
					s.duelLossTag = ""
					s.duelLossStreak = 0
					s.mu.Unlock()
					selectedTag = bestTag
					switchReason = "best_selected_from_non_anchor"
					newDelayMs = normalizeChampionDelay(bestDelay)
				}
			}
		}
	}

	if allCandidatesDead {
		s.mu.Lock()
		oldTag := s.lastTag
		s.lastTag = ""
		s.duelLossTag = ""
		s.duelLossStreak = 0
		s.mu.Unlock()

		if oldTag != "" {
			oldDelayMs = normalizeChampionDelay(getDelay(oldTag))
		}
		newDelayMs = championUnknownDelay
		s.logChampionSwitch(switchReason, oldTag, "", oldDelayMs, newDelayMs)
		return ""
	}

	if selectedTag == "" {
		s.mu.Lock()
		oldTag := s.lastTag
		selectedTag = tags[s.index%len(tags)]
		s.index = (s.index + 1) % len(tags)
		s.lastTag = selectedTag
		s.mu.Unlock()

		s.logChampionSwitch("round_robin_fallback", oldTag, selectedTag, championUnknownDelay, championUnknownDelay)
		return selectedTag
	}

	s.mu.Lock()
	oldTag := s.lastTag
	s.lastTag = selectedTag
	s.mu.Unlock()

	if oldDelayMs == championUnknownDelay && oldTag != "" {
		oldDelayMs = normalizeChampionDelay(getDelay(oldTag))
	}
	if newDelayMs == championUnknownDelay && selectedTag != "" {
		newDelayMs = normalizeChampionDelay(getDelay(selectedTag))
	}

	s.logChampionSwitch(switchReason, oldTag, selectedTag, oldDelayMs, newDelayMs)
	return selectedTag
}
