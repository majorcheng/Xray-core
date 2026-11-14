package router

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
)

const (
	championDefaultDelay  int64 = 500
	championInfiniteDelay int64 = 1<<63 - 1
)

// championTagForLog 输出可按 key=value 解析的 tag。缺失时输出 none；tag 本身是 none，
// 或含空白、等号、引号、不可打印字符时加引号，避免与缺失值混淆或破坏字段边界。
func championTagForLog(tag string) string {
	if tag == "" {
		return "none"
	}
	if tag == "none" || strings.ContainsFunc(tag, func(r rune) bool {
		return r == '=' || r == '"' || unicode.IsSpace(r) || !unicode.IsPrint(r)
	}) {
		return strconv.Quote(tag)
	}
	return tag
}

// ChampionStrategy keeps a stable "champion" outbound while avoiding frequent switches.
// 它优先保住当前擂主，并且只统计不同观测上的连续胜场，避免单次抖动被高并发请求放大。
type ChampionStrategy struct {
	FallbackTag string
	// BalancerTag 只用于日志归属，便于区分多个 champion balancer。
	BalancerTag string
	Settings    ChampionSettings

	ctx          context.Context
	observatory  extension.Observatory
	quality      extension.OutboundQualityObservatory
	qualityState championQualityState

	mu      sync.Mutex
	index   int
	lastTag string
	// duelAnchor 是 challenge/reclaim 胜场针对的擂主；擂主变化时胜场清零。
	duelAnchor string
	challenge  championDuel
	reclaim    championDuel
	// fallbackCause 非空表示正处于轮询回退，只在进入和退出时记日志。
	fallbackCause string
}

type championSwitchEvent struct {
	reason             string
	oldTag, newTag     string
	oldScore, newScore string
}

func (s *ChampionStrategy) InjectContext(ctx context.Context) {
	s.ctx = ctx
	s.Settings = s.Settings.normalized()
	// champion 可以在无 observatory 时退化为普通轮询，因此使用 OptionalFeatures。
	common.Must(core.OptionalFeatures(s.ctx, func(observatory extension.Observatory) error {
		s.attachObservatory(observatory)
		return nil
	}))
}

// attachObservatory 绑定 observatory；quality 模式缺少质量能力时退化为 off 并告警。
func (s *ChampionStrategy) attachObservatory(observatory extension.Observatory) {
	s.observatory = observatory
	if s.Settings.QualityMode == ChampionQualityOff {
		return
	}
	quality, ok := observatory.(extension.OutboundQualityObservatory)
	if !ok {
		errors.LogWarning(s.ctx, "champion quality mode unavailable",
			" balancer=", championTagForLog(s.BalancerTag),
			" quality_mode=", s.Settings.QualityMode,
			" effective_mode=", ChampionQualityOff,
			" cause=observatory_without_quality")
		return
	}
	s.quality = quality
	quality.EnableOutboundQuality()
}

func (s *ChampionStrategy) GetPrincipleTarget(strings []string) []string {
	return strings
}

func (s *ChampionStrategy) logChampionSwitch(event championSwitchEvent) {
	if event.oldTag == event.newTag {
		return
	}
	log := errors.LogWarning
	if event.oldTag == "" {
		// 启动、规则重载或全死恢复后的首次选择不是故障切换。
		log = errors.LogInfo
	}
	log(s.ctx, "champion switched",
		" balancer=", championTagForLog(s.BalancerTag),
		" reason=", event.reason,
		" old_tag=", championTagForLog(event.oldTag),
		" new_tag=", championTagForLog(event.newTag),
		" old_score_ms=", event.oldScore,
		" new_score_ms=", event.newScore,
	)
}

func (s *ChampionStrategy) logFallbackStarted(cause string, err error) {
	errText := "none"
	if err != nil {
		errText = strconv.Quote(err.Error())
	}
	errors.LogWarning(s.ctx, "champion fallback to round robin",
		" balancer=", championTagForLog(s.BalancerTag),
		" cause=", cause,
		" quality_mode=", s.Settings.normalized().QualityMode,
		" error=", errText,
	)
}

func (s *ChampionStrategy) PickOutbound(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	if s.quality != nil && (s.Settings.QualityMode == ChampionQualityShadow || s.Settings.QualityMode == ChampionQualitySelect) {
		tag := s.pickQuality(tags, s.quality.GetOutboundQuality())
		if s.Settings.QualityMode == ChampionQualitySelect {
			return tag
		}
	}
	return s.pickObserved(tags)
}

// loadObservation 必须持有 s.mu；读取失败时返回回退原因。
func (s *ChampionStrategy) loadObservation(tags []string) (*championObservation, string, error) {
	if s.observatory == nil {
		return nil, "observatory_unavailable", nil
	}
	observeReport, err := s.observatory.GetObservation(s.ctx)
	if err != nil {
		return nil, "observation_error", err
	}
	result, ok := observeReport.(*observatory.ObservationResult)
	if !ok {
		return nil, "observation_unsupported", nil
	}
	return newChampionObservation(tags, result, s.Settings), "", nil
}

// pickObserved 在同一临界区内读取观测、决策并提交。若在锁外读取，先读到的旧快照可能晚于新快照提交，
// 同一轮测量会被重复计票，刚完成的晋级也会被旧快照撤销。内置 observatory 的 GetObservation
// 只在自身锁内复制内存状态，不做网络 I/O，因此可以在 s.mu 内调用。
func (s *ChampionStrategy) pickObserved(tags []string) string {
	s.mu.Lock()
	obs, cause, err := s.loadObservation(tags)
	if obs == nil {
		selectedTag, started := s.pickRoundRobinFallbackLocked(tags, cause)
		s.mu.Unlock()
		if started {
			s.logFallbackStarted(cause, err)
		}
		return selectedTag
	}
	endedCause := s.fallbackCause
	s.fallbackCause = ""
	oldTag := s.lastTag
	decision := s.decideLocked(obs, oldTag)
	s.lastTag = decision.selectedTag
	s.mu.Unlock()

	if endedCause != "" {
		// 回退期间 lastTag 是最后一次轮询结果；新擂主可能与它相同而不产生切换日志，因此在此一并输出。
		errors.LogInfo(s.ctx, "champion fallback ended",
			" balancer=", championTagForLog(s.BalancerTag),
			" cause=", endedCause,
			" champion=", championTagForLog(decision.selectedTag))
	}
	s.logChampionSwitch(championSwitchEvent{
		reason:   decision.reason,
		oldTag:   oldTag,
		newTag:   decision.selectedTag,
		oldScore: obs.scoreText(oldTag),
		newScore: obs.scoreText(decision.selectedTag),
	})
	return decision.selectedTag
}

// decideLocked 必须持有 s.mu；它更新胜场并返回本次应提交的擂主。
func (s *ChampionStrategy) decideLocked(obs *championObservation, lastTag string) championDecision {
	bestTag, bestScore := obs.best()
	if bestScore == championInfiniteDelay {
		s.clearDuelLocked()
		return championDecision{reason: "all_candidates_dead"}
	}
	anchorTag, anchorScore := obs.anchor(lastTag)
	if anchorScore == championInfiniteDelay {
		s.clearDuelLocked()
		return championDecision{selectedTag: bestTag, reason: "best_selected_from_non_anchor"}
	}
	if anchorTag != s.duelAnchor {
		s.clearDuelLocked()
		s.duelAnchor = anchorTag
	}
	if decision, promoted := s.challengeLocked(obs, anchorTag, anchorScore, bestTag, bestScore); promoted {
		// 晋级后清零；否则新擂主立即失效、anchor 回到旧擂主时，残留胜场会让下一次挑战直接晋级。
		s.clearDuelLocked()
		return decision
	}
	return championDecision{selectedTag: anchorTag, reason: championStayReason(obs, lastTag, anchorTag)}
}

// challengeLocked 必须持有 s.mu。preferred 回切与普通挑战分别累计胜场，互不清零；
// 非首选擂主同样接受普通挑战，避免它劣化后只能等待 preferred 回切。
func (s *ChampionStrategy) challengeLocked(obs *championObservation, anchorTag string, anchorScore int64, bestTag string, bestScore int64) (championDecision, bool) {
	settings := obs.settings
	preferredTag, preferredScore := obs.preferred()
	if anchorTag != preferredTag && obs.preferredCanReclaim(preferredTag, preferredScore, anchorTag, anchorScore) {
		key := championDuelKey{anchorScore: anchorScore, challengerTag: preferredTag, challengerScore: preferredScore}
		if s.reclaim.observe(key, preferredTag, settings.PreferredObservationCount) {
			return championDecision{selectedTag: preferredTag, reason: "preferred_reclaimed"}, true
		}
	} else {
		s.reclaim = championDuel{}
	}
	if !obs.challengerWins(anchorTag, anchorScore, bestTag, bestScore) {
		s.challenge = championDuel{}
		return championDecision{}, false
	}
	// preferred 与 best 足够接近时，本轮票投给 preferred，避免先换到 best、随后又被 preferred 回收。
	// 只有本轮目标可以晋级，且它自己要有足够票数；只在最后一轮出现的候选不会被直接选中。
	target, reason := bestTag, "challenger_promoted"
	if anchorTag != preferredTag && (bestTag == preferredTag || obs.preferredCanReclaim(preferredTag, preferredScore, bestTag, bestScore)) {
		target, reason = preferredTag, "preferred_reclaimed"
	}
	key := championDuelKey{anchorScore: anchorScore, challengerTag: bestTag, challengerScore: bestScore}
	if !s.challenge.observe(key, target, settings.CandidateObservationCount) {
		return championDecision{}, false
	}
	return championDecision{selectedTag: target, reason: reason}, true
}

// championStayReason 说明保持 anchor 时它为何不同于上一任擂主；相同则不会记日志。
func championStayReason(obs *championObservation, lastTag, anchorTag string) string {
	switch {
	case lastTag == "":
		return "no_previous_champion"
	case !obs.isCandidate(lastTag):
		return "champion_removed"
	case lastTag != anchorTag:
		return "champion_unavailable"
	}
	return ""
}

// preferredRoundRobinStart 在无 observatory 的首轮回退时优先命中显式首选擂主。
func (s *ChampionStrategy) preferredRoundRobinStart(tags []string) (string, int, bool) {
	preferredTag := s.Settings.normalized().PreferredTag
	if preferredTag == "" {
		return "", 0, false
	}
	for idx, tag := range tags {
		if tag == preferredTag {
			return preferredTag, idx, true
		}
	}
	return "", 0, false
}

// hasCurrentCandidate 判断当前记录的 champion 是否仍属于本轮候选集。
func hasCurrentCandidate(tags []string, tag string) bool {
	if tag == "" {
		return false
	}
	for _, candidate := range tags {
		if candidate == tag {
			return true
		}
	}
	return false
}

// pickRoundRobinFallbackLocked 必须持有 s.mu。每次请求都会轮转 tag，因此只在回退原因变化时返回 started，
// 由调用方在解锁后记一次日志。
func (s *ChampionStrategy) pickRoundRobinFallbackLocked(tags []string, cause string) (string, bool) {
	selectedTag := tags[s.index%len(tags)]
	// 旧 champion 为空，或已不在本轮候选集时，都视作需要从当前集合重新启动 fallback。
	if !hasCurrentCandidate(tags, s.lastTag) {
		if preferredTag, preferredIdx, ok := s.preferredRoundRobinStart(tags); ok {
			selectedTag = preferredTag
			s.index = (preferredIdx + 1) % len(tags)
		} else {
			s.index = (s.index + 1) % len(tags)
		}
	} else {
		s.index = (s.index + 1) % len(tags)
	}
	s.lastTag = selectedTag
	s.clearDuelLocked()
	started := s.fallbackCause != cause
	s.fallbackCause = cause
	return selectedTag, started
}

func (s *ChampionStrategy) clearDuelLocked() {
	s.duelAnchor = ""
	s.challenge = championDuel{}
	s.reclaim = championDuel{}
}
