package session

import (
	"context"

	"github.com/xtls/xray-core/features/extension"
)

type outboundSignalContextKey struct{}

var trackedOutboundSignalContextKey outboundSignalContextKey

// TrackedRequestSignalFeedback 接收业务路径上报的 outbound 成功/失败事件。
type TrackedRequestSignalFeedback interface {
	SubmitOutboundSignal(signal *extension.OutboundSignal)
}

// TrackedRequestRelayState 记录当前请求是否已经进入真实转发阶段。
type TrackedRequestRelayState interface {
	MarkRelayEstablished() bool
	RelayEstablished() bool
}

// TrackedOutboundSignal 把业务事件 tracker 绑定到当前请求上下文。
func TrackedOutboundSignal(ctx context.Context, tracker TrackedRequestSignalFeedback) context.Context {
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, trackedOutboundSignalContextKey, tracker)
}

// CurrentOutboundTag 返回当前请求上下文里最内层 outbound tag。
func CurrentOutboundTag(ctx context.Context) string {
	outbounds := OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		return ""
	}
	return outbounds[len(outbounds)-1].Tag
}

func outboundSignalTrackerFromContext(ctx context.Context) TrackedRequestSignalFeedback {
	tracker, _ := ctx.Value(trackedOutboundSignalContextKey).(TrackedRequestSignalFeedback)
	return tracker
}

func outboundRelayStateFromContext(ctx context.Context) TrackedRequestRelayState {
	tracker, _ := ctx.Value(trackedOutboundSignalContextKey).(TrackedRequestRelayState)
	return tracker
}

func submitOutboundErrorTracker(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if errorTracker := ctx.Value(trackedConnectionErrorKey); errorTracker != nil {
		errorTracker := errorTracker.(TrackedRequestErrorFeedback)
		errorTracker.SubmitError(err)
	}
}

// SubmitOutboundSignalToOriginator 把业务事件回灌给请求发起方挂载的 tracker。
func SubmitOutboundSignalToOriginator(ctx context.Context, signal *extension.OutboundSignal) {
	tracker := outboundSignalTrackerFromContext(ctx)
	if tracker == nil || signal == nil {
		return
	}
	copySignal := *signal
	if copySignal.OutboundTag == "" {
		copySignal.OutboundTag = CurrentOutboundTag(ctx)
	}
	if copySignal.OutboundTag == "" {
		return
	}
	tracker.SubmitOutboundSignal(&copySignal)
}

func submitOutboundFailureSignal(ctx context.Context, kind extension.OutboundSignalKind, err error) {
	if err == nil {
		return
	}
	submitOutboundErrorTracker(ctx, err)
	SubmitOutboundSignalToOriginator(ctx, &extension.OutboundSignal{
		Kind:   kind,
		Reason: err.Error(),
	})
}

// SubmitOutboundDialFailureToOriginator 上报真实拨号失败。
func SubmitOutboundDialFailureToOriginator(ctx context.Context, err error) {
	submitOutboundFailureSignal(ctx, extension.OutboundSignalDialFailure, err)
}

// SubmitOutboundMuxFailureToOriginator 上报 mux 建立失败。
func SubmitOutboundMuxFailureToOriginator(ctx context.Context, err error) {
	submitOutboundFailureSignal(ctx, extension.OutboundSignalMuxFailure, err)
}

// SubmitOutboundPreRelayProxyFailureToOriginator 上报进入真实转发前的代理处理失败。
func SubmitOutboundPreRelayProxyFailureToOriginator(ctx context.Context, err error) {
	if OutboundRelayEstablished(ctx) {
		return
	}
	submitOutboundFailureSignal(ctx, extension.OutboundSignalPreRelayProxyFailure, err)
}

// SubmitOutboundDialSuccessToOriginator 上报底层真实拨号成功。
func SubmitOutboundDialSuccessToOriginator(ctx context.Context) {
	SubmitOutboundSignalToOriginator(ctx, &extension.OutboundSignal{Kind: extension.OutboundSignalDialSuccess})
}

// NoteOutboundRelayEstablished 在请求首次进入真实转发时上报成功。
func NoteOutboundRelayEstablished(ctx context.Context) {
	state := outboundRelayStateFromContext(ctx)
	if state == nil {
		return
	}
	if state.MarkRelayEstablished() {
		SubmitOutboundSignalToOriginator(ctx, &extension.OutboundSignal{Kind: extension.OutboundSignalRelaySuccess})
	}
}

// OutboundRelayEstablished 返回当前请求是否已经进入真实转发阶段。
func OutboundRelayEstablished(ctx context.Context) bool {
	state := outboundRelayStateFromContext(ctx)
	if state == nil {
		return false
	}
	return state.RelayEstablished()
}
