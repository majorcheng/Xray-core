package dispatcher

import (
	"context"
	"sync"

	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/extension"
)

// outboundSignalReporter 抽象 observatory 的运行时事件上报能力。
type outboundSignalReporter interface {
	ReportOutboundSignal(signal *extension.OutboundSignal)
}

// outboundDispatchTracker 绑定单次请求与目标 outbound tag，负责把链路事件回写到 observatory。
// dialerProxy 会追加新的 outbound hop，因此转发状态必须按 tag 隔离。
type outboundDispatchTracker struct {
	mu               sync.Mutex
	reporter         outboundSignalReporter
	outboundTag      string
	relayEstablished map[string]bool
}

// newOutboundDispatchContext 为普通业务请求挂接 outbound 运行时事件 tracker。
func newOutboundDispatchContext(ctx context.Context, observatory extension.Observatory, outboundTag string) context.Context {
	reporter, ok := observatory.(outboundSignalReporter)
	if !ok || outboundTag == "" {
		return ctx
	}
	tracker := &outboundDispatchTracker{
		reporter:         reporter,
		outboundTag:      outboundTag,
		relayEstablished: make(map[string]bool),
	}
	return session.TrackedOutboundSignal(ctx, tracker)
}

func (t *outboundDispatchTracker) SubmitOutboundSignal(signal *extension.OutboundSignal) {
	if signal == nil || t.reporter == nil {
		return
	}
	copySignal := *signal
	if copySignal.OutboundTag == "" {
		copySignal.OutboundTag = t.outboundTag
	}
	if copySignal.OutboundTag == "" {
		return
	}
	t.reporter.ReportOutboundSignal(&copySignal)
}

func (t *outboundDispatchTracker) MarkRelayEstablished(tag string) bool {
	if tag == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.relayEstablished[tag] {
		return false
	}
	t.relayEstablished[tag] = true
	return true
}

func (t *outboundDispatchTracker) RelayEstablished(tag string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.relayEstablished[tag]
}
