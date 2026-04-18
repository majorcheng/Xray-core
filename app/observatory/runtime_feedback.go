package observatory

import (
	"slices"
	"time"

	"github.com/xtls/xray-core/features/extension"
)

const deadDelayMs int64 = 99999999

type runtimeFeedbackState struct {
	Alive          bool
	Delay          int64
	Reason         string
	LastTryTime    int64
	LastSeenTime   int64
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
	b.overlay.apply(signal)
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
	if signal == nil || signal.OutboundTag == "" {
		return
	}
	state, found := o.statusByTag[signal.OutboundTag]
	if !found {
		state = &runtimeFeedbackState{}
		o.statusByTag[signal.OutboundTag] = state
	}
	now := time.Now()
	state.LastTryTime = now.Unix()
	state.lastTryUnixNs = now.UnixNano()
	switch signal.Kind {
	case extension.OutboundSignalDialFailure, extension.OutboundSignalMuxFailure, extension.OutboundSignalPreRelayProxyFailure:
		state.Alive = false
		state.Delay = deadDelayMs
		state.Reason = signal.Reason
	case extension.OutboundSignalDialSuccess, extension.OutboundSignalRelaySuccess:
		state.Alive = true
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
		return cloneOutboundStatus(base)
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
