package router

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/xtls/xray-core/app/observatory"
)

const (
	championMinRelativeImprovement = 4
	championRelativeImprovementDiv = 7
	championMinAbsoluteImprovement = 100
	championMinFailPenaltyMs       = 50
)

const (
	ChampionQualityOff    = "off"
	ChampionQualityShadow = "shadow"
	ChampionQualitySelect = "select"
)

func ParseChampionQualityMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", ChampionQualityOff:
		return ChampionQualityOff, nil
	case ChampionQualityShadow, ChampionQualitySelect:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown champion qualityMode %q (want off, shadow or select)", mode)
	}
}

// ChampionSettings 保存 champion 抗抖动参数。
type ChampionSettings struct {
	CandidateObservationCount int
	PreferredObservationCount int
	HealthPingJitterScale     float64
	PreferredMaxDelayGap      time.Duration
	PreferredTag              string
	QualityMode               string
}

type championDecision struct {
	selectedTag       string
	reason            string
	oldDelayMs        int64
	newDelayMs        int64
	allCandidatesDead bool
}

type championDuelKey struct {
	anchorTimestamp     int64
	anchorScore         int64
	challengerTimestamp int64
	challengerScore     int64
}

type championObservation struct {
	tags         []string
	settings     ChampionSettings
	statusMap    map[string]*observatory.OutboundStatus
	candidateSet map[string]struct{}
}

func defaultChampionSettings() ChampionSettings {
	return ChampionSettings{
		CandidateObservationCount: 4,
		PreferredObservationCount: 6,
		HealthPingJitterScale:     1,
		PreferredMaxDelayGap:      80 * time.Millisecond,
		QualityMode:               ChampionQualityOff,
	}
}

func (s ChampionSettings) normalized() ChampionSettings {
	defaults := defaultChampionSettings()
	if s.CandidateObservationCount <= 0 {
		s.CandidateObservationCount = defaults.CandidateObservationCount
	}
	if s.PreferredObservationCount <= 0 {
		s.PreferredObservationCount = defaults.PreferredObservationCount
	}
	// proto3 标量缺少 presence，partial settings 下的 0 语义按“沿用默认值”收口。
	if s.HealthPingJitterScale <= 0 {
		s.HealthPingJitterScale = defaults.HealthPingJitterScale
	}
	if s.PreferredMaxDelayGap <= 0 {
		s.PreferredMaxDelayGap = defaults.PreferredMaxDelayGap
	}
	s.PreferredTag = strings.TrimSpace(s.PreferredTag)
	if s.QualityMode == "" {
		s.QualityMode = ChampionQualityOff
	}
	return s
}

func newChampionObservation(tags []string, result *observatory.ObservationResult, settings ChampionSettings) *championObservation {
	statusMap := make(map[string]*observatory.OutboundStatus, len(result.Status))
	for _, status := range result.Status {
		statusMap[status.OutboundTag] = status
	}
	candidateSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		candidateSet[tag] = struct{}{}
	}
	return &championObservation{
		tags:         tags,
		settings:     settings.normalized(),
		statusMap:    statusMap,
		candidateSet: candidateSet,
	}
}

func (o *championObservation) preferred() (string, int64) {
	tag := o.preferredTag()
	return tag, o.delayScore(tag)
}

// preferredTag 返回当前观测下的默认首选擂主。
// 显式 preferredTag 命中候选集时优先使用，否则退回候选数组首项。
func (o *championObservation) preferredTag() string {
	if tag := o.settings.PreferredTag; tag != "" {
		if _, ok := o.candidateSet[tag]; ok {
			return tag
		}
	}
	return o.tags[0]
}

func (o *championObservation) anchor(lastTag string) (string, int64) {
	preferredTag, preferredDelay := o.preferred()
	if _, ok := o.candidateSet[lastTag]; ok {
		lastDelay := o.delayScore(lastTag)
		if lastDelay != championInfiniteDelay {
			return lastTag, lastDelay
		}
	}
	return preferredTag, preferredDelay
}

func (o *championObservation) best() (string, int64) {
	bestTag := ""
	bestDelay := championInfiniteDelay
	for _, tag := range o.tags {
		delay := o.delayScore(tag)
		if delay < bestDelay {
			bestDelay = delay
			bestTag = tag
		}
	}
	return bestTag, bestDelay
}

func (o *championObservation) challengerWins(anchorTag string, anchorDelay int64, bestTag string, bestDelay int64) bool {
	return bestTag != "" &&
		bestTag != anchorTag &&
		bestDelay < (anchorDelay*championMinRelativeImprovement/championRelativeImprovementDiv) &&
		bestDelay < (anchorDelay-championMinAbsoluteImprovement)
}

func (o *championObservation) preferredCanReclaim(preferredTag string, preferredDelay int64, anchorTag string, anchorDelay int64) bool {
	if preferredTag == "" || preferredTag == anchorTag || preferredDelay == championInfiniteDelay {
		return false
	}
	if preferredDelay > anchorDelay &&
		(preferredDelay-anchorDelay) >= o.settings.PreferredMaxDelayGap.Milliseconds() {
		return false
	}
	return anchorDelay > (preferredDelay * championMinRelativeImprovement / championRelativeImprovementDiv)
}

func (o *championObservation) duelKey(anchorTag string, anchorDelay int64, challengerTag string, challengerDelay int64) championDuelKey {
	return championDuelKey{
		anchorTimestamp:     o.timestamp(anchorTag),
		anchorScore:         anchorDelay,
		challengerTimestamp: o.timestamp(challengerTag),
		challengerScore:     challengerDelay,
	}
}

func (o *championObservation) timestamp(tag string) int64 {
	status, found := o.statusMap[tag]
	if !found {
		return 0
	}
	return championMaxInt64(status.LastTryTime, status.LastSeenTime)
}

func (o *championObservation) delayScore(tag string) int64 {
	status, found := o.statusMap[tag]
	if !found {
		return championDefaultDelay
	}
	if !status.Alive {
		return championInfiniteDelay
	}
	score := status.Delay
	if score <= 0 {
		score = championDefaultDelay
	}
	if status.HealthPing != nil {
		score = championHealthPingScore(status, o.settings)
	}
	return score
}

func championHealthPingScore(status *observatory.OutboundStatus, settings ChampionSettings) int64 {
	score := status.Delay
	if score <= 0 {
		score = championDefaultDelay
	}
	averageMs := time.Duration(status.HealthPing.Average).Milliseconds()
	if averageMs > 0 {
		score = averageMs
	}
	deviationMs := time.Duration(status.HealthPing.Deviation).Milliseconds()
	if deviationMs > 0 {
		jitterPenalty := int64(math.Round(float64(deviationMs) * settings.HealthPingJitterScale))
		score += jitterPenalty
	}
	if status.HealthPing.All > 0 && status.HealthPing.Fail > 0 {
		failPenalty := score * status.HealthPing.Fail / status.HealthPing.All
		if failPenalty < championMinFailPenaltyMs {
			failPenalty = championMinFailPenaltyMs
		}
		score += failPenalty
	}
	if score <= 0 {
		return championDefaultDelay
	}
	return score
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func championMaxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
