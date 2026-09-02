package observatory

import (
	"slices"
	"time"

	"github.com/xtls/xray-core/features/extension"
)

const deadDelayMs int64 = 99999999

const (
	runtimeFeedbackDownThreshold    = 2
	runtimeFeedbackRecoverThreshold = 7
)

type runtimeFeedbackState struct {
	Alive          bool
	EverAlive      bool
	Delay          int64
	Reason         string
	LastTryTime    int64
	LastSeenTime   int64
	SuccessStreak  int
	FailureStreak  int
	lastTryUnixNs  int64
	lastSeenUnixNs int64
}

type runtimeFeedbackOverlay struct {
	statusByTag map[string]*runtimeFeedbackState
}

// RuntimeFeedbackOverlayBridge 供 burst observatory 复用运行时业务覆盖层。
type RuntimeFeedbackOverlayBridge struct {
	overlay *runtimeFeedbackOverlay
}

func newRuntimeFeedbackOverlay() *runtimeFeedbackOverlay {
	return &runtimeFeedbackOverlay{statusByTag: make(map[string]*runtimeFeedbackState)}
}

func NewRuntimeFeedbackOverlayBridge() *RuntimeFeedbackOverlayBridge {
	return &RuntimeFeedbackOverlayBridge{overlay: newRuntimeFeedbackOverlay()}
}

func (b *RuntimeFeedbackOverlayBridge) Apply(signal *extension.OutboundSignal) {
	b.overlay.applyWithStatus(signal, nil, 0)
}

func (b *RuntimeFeedbackOverlayBridge) ApplyWithStatus(signal *extension.OutboundSignal, base *OutboundStatus) {
	b.overlay.applyWithStatus(signal, base, outboundStatusTimestamp(base, nil))
}

// ApplyWithStatusAt 允许调用方传入更精确的 base 时间戳，避免秒级时间戳掩盖新探测结果。
func (b *RuntimeFeedbackOverlayBridge) ApplyWithStatusAt(signal *extension.OutboundSignal, base *OutboundStatus, baseTimestamp int64) {
	b.overlay.applyWithStatus(signal, base, baseTimestamp)
}

func (b *RuntimeFeedbackOverlayBridge) Merge(base []*OutboundStatus) []*OutboundStatus {
	return b.overlay.merge(base, nil)
}

// MergeWithTimestamps 允许 burst observatory 用健康探测时间戳压过更旧的业务失败覆盖层。
func (b *RuntimeFeedbackOverlayBridge) MergeWithTimestamps(base []*OutboundStatus, timestamps map[string]int64) []*OutboundStatus {
	return b.overlay.merge(base, timestamps)
}

func (b *RuntimeFeedbackOverlayBridge) Prune(tags []string) {
	b.overlay.prune(tags)
}

func (o *runtimeFeedbackOverlay) prune(tags []string) {
	if len(o.statusByTag) == 0 {
		return
	}
	for tag := range o.statusByTag {
		if !slices.Contains(tags, tag) {
			delete(o.statusByTag, tag)
		}
	}
}

func (o *runtimeFeedbackOverlay) apply(signal *extension.OutboundSignal) {
	o.applyWithStatus(signal, nil, 0)
}

func (o *runtimeFeedbackOverlay) applyWithStatus(signal *extension.OutboundSignal, base *OutboundStatus, baseTimestamp int64) {
	if signal == nil || signal.OutboundTag == "" {
		return
	}
	state, found := o.statusByTag[signal.OutboundTag]
	if !found {
		state = &runtimeFeedbackState{}
		o.statusByTag[signal.OutboundTag] = state
		state.seedFromBase(base)
	} else {
		state.refreshFromBase(base, baseTimestamp)
	}
	now := time.Now()
	state.LastTryTime = now.Unix()
	state.lastTryUnixNs = now.UnixNano()
	switch signal.Kind {
	case extension.OutboundSignalDialFailure, extension.OutboundSignalMuxFailure, extension.OutboundSignalPreRelayProxyFailure:
		state.FailureStreak++
		state.SuccessStreak = 0
		if !state.EverAlive || state.FailureStreak >= runtimeFeedbackDownThreshold {
			state.Alive = false
			state.Delay = deadDelayMs
			state.Reason = signal.Reason
		}
	case extension.OutboundSignalDialSuccess, extension.OutboundSignalRelaySuccess:
		state.SuccessStreak++
		state.FailureStreak = 0
		if !state.EverAlive || state.Alive || state.SuccessStreak >= runtimeFeedbackRecoverThreshold {
			state.Alive = true
			state.EverAlive = true
			if signal.DelayMs > 0 {
				state.Delay = signal.DelayMs
			} else {
				state.Delay = 0
			}
			state.Reason = ""
			state.LastSeenTime = now.Unix()
			state.lastSeenUnixNs = now.UnixNano()
		}
	}
}

func (s *runtimeFeedbackState) seedFromBase(base *OutboundStatus) {
	if base == nil {
		return
	}
	s.resetFromBase(base, outboundStatusTimestamp(base, nil))
}

// refreshFromBase 用更近的探测结果覆盖旧业务态，避免失败 streak 跨探测周期累积。
func (s *runtimeFeedbackState) refreshFromBase(base *OutboundStatus, baseTimestamp int64) {
	if base == nil || baseTimestamp <= 0 {
		return
	}
	if baseTimestamp < runtimeFeedbackTimestamp(s) {
		return
	}
	s.resetFromBase(base, baseTimestamp)
}

// resetFromBase 按最新探测结果重置 overlay 状态，并清空业务成功/失败 streak。
func (s *runtimeFeedbackState) resetFromBase(base *OutboundStatus, baseTimestamp int64) {
	if base == nil {
		return
	}
	if baseTimestamp <= 0 {
		baseTimestamp = outboundStatusTimestamp(base, nil)
	}
	baseTryTime := base.LastTryTime
	if baseTryTime == 0 && baseTimestamp > 0 {
		baseTryTime = baseTimestamp / int64(time.Second)
	}
	baseSeenTime := base.LastSeenTime
	if baseSeenTime == 0 && base.Alive && baseTimestamp > 0 {
		baseSeenTime = baseTimestamp / int64(time.Second)
	}
	s.Alive = base.Alive
	s.Delay = base.Delay
	s.Reason = base.LastErrorReason
	s.LastTryTime = baseTryTime
	s.LastSeenTime = baseSeenTime
	s.lastTryUnixNs = baseTimestamp
	s.lastSeenUnixNs = baseSeenTime * int64(time.Second)
	if base.Alive && baseTimestamp > s.lastSeenUnixNs {
		s.lastSeenUnixNs = baseTimestamp
	}
	s.SuccessStreak = 0
	s.FailureStreak = 0
	if base.Alive || base.LastSeenTime > 0 || base.HealthPing != nil {
		s.EverAlive = true
	}
}

func (o *runtimeFeedbackOverlay) merge(base []*OutboundStatus, timestamps map[string]int64) []*OutboundStatus {
	result := make([]*OutboundStatus, 0, len(base)+len(o.statusByTag))
	seen := make(map[string]struct{}, len(base))
	for _, status := range base {
		if status == nil {
			continue
		}
		merged := o.applyToStatus(status, timestamps)
		result = append(result, merged)
		seen[merged.OutboundTag] = struct{}{}
	}
	for tag := range o.statusByTag {
		if _, found := seen[tag]; found {
			continue
		}
		if synthetic := o.synthesize(tag); synthetic != nil {
			result = append(result, synthetic)
		}
	}
	return result
}

func (o *runtimeFeedbackOverlay) applyToStatus(base *OutboundStatus, timestamps map[string]int64) *OutboundStatus {
	if base == nil {
		return nil
	}
	state, found := o.statusByTag[base.OutboundTag]
	if !found {
		return cloneOutboundStatus(base)
	}
	baseTimestamp := outboundStatusTimestamp(base, timestamps)
	if baseTimestamp >= runtimeFeedbackTimestamp(state) {
		state.resetFromBase(base, baseTimestamp)
		return cloneOutboundStatus(base)
	}
	if base.Alive && state.FailureStreak > 0 && state.FailureStreak < runtimeFeedbackDownThreshold {
		cloned := cloneOutboundStatus(base)
		cloned.LastTryTime = maxInt64(cloned.LastTryTime, state.LastTryTime)
		return cloned
	}
	cloned := cloneOutboundStatus(base)
	cloned.LastTryTime = maxInt64(cloned.LastTryTime, state.LastTryTime)
	if state.LastSeenTime > 0 {
		cloned.LastSeenTime = maxInt64(cloned.LastSeenTime, state.LastSeenTime)
	}
	cloned.Alive = state.Alive
	if state.Delay > 0 {
		cloned.Delay = state.Delay
	} else if state.Alive && cloned.Delay <= 0 {
		cloned.Delay = 1
	}
	if !state.Alive && cloned.Delay <= 0 {
		cloned.Delay = deadDelayMs
	}
	cloned.LastErrorReason = state.Reason
	return cloned
}

func (o *runtimeFeedbackOverlay) synthesize(tag string) *OutboundStatus {
	if tag == "" {
		return nil
	}
	state, found := o.statusByTag[tag]
	if !found {
		return nil
	}
	status := &OutboundStatus{
		OutboundTag:     tag,
		Alive:           state.Alive,
		Delay:           state.Delay,
		LastTryTime:     state.LastTryTime,
		LastSeenTime:    state.LastSeenTime,
		LastErrorReason: state.Reason,
	}
	if status.Alive && status.Delay <= 0 {
		status.Delay = 1
	}
	if !status.Alive && status.Delay <= 0 {
		status.Delay = deadDelayMs
	}
	return status
}

func runtimeFeedbackTimestamp(state *runtimeFeedbackState) int64 {
	return maxInt64(state.lastTryUnixNs, state.lastSeenUnixNs)
}

func outboundStatusTimestamp(status *OutboundStatus, timestamps map[string]int64) int64 {
	if timestamps != nil {
		if ts, found := timestamps[status.OutboundTag]; found {
			return ts
		}
	}
	return maxInt64(status.LastTryTime, status.LastSeenTime) * int64(time.Second)
}

func cloneOutboundStatus(status *OutboundStatus) *OutboundStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	if status.HealthPing != nil {
		h := *status.HealthPing
		cloned.HealthPing = &h
	}
	return &cloned
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
