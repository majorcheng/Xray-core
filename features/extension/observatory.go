package extension

import (
	"context"

	"github.com/xtls/xray-core/features"
	"google.golang.org/protobuf/proto"
)

type Observatory interface {
	features.Feature

	GetObservation(ctx context.Context) (proto.Message, error)
}

type BurstObservatory interface {
	Observatory
	Check(tag []string)
}

// OutboundSignalKind 描述真实业务链路里可直接映射成健康状态的事件类型。
type OutboundSignalKind string

const (
	OutboundSignalDialFailure          OutboundSignalKind = "dial_failure"
	OutboundSignalMuxFailure           OutboundSignalKind = "mux_failure"
	OutboundSignalPreRelayProxyFailure OutboundSignalKind = "pre_relay_proxy_failure"
	OutboundSignalDialSuccess          OutboundSignalKind = "dial_success"
	OutboundSignalRelaySuccess         OutboundSignalKind = "relay_success"
)

// OutboundSignal 是业务路径回灌到 observatory 的最小事件单元。
type OutboundSignal struct {
	OutboundTag string
	Kind        OutboundSignalKind
	Reason      string
	DelayMs     int64
}

// OutboundSignalReporter 是 observatory 的可选扩展接口。
// dispatcher 直接对现有 observatory 做 type assertion，避免新增 feature 注册类型。
type OutboundSignalReporter interface {
	ReportOutboundSignal(signal *OutboundSignal)
}

func ObservatoryType() interface{} {
	return (*Observatory)(nil)
}
