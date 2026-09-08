package extension

import (
	"context"
	"time"
)

// QualityInterval 既是运行时采样周期，也是每个挑战周期的最小粒度。
const QualityInterval = 5 * time.Second

type OutboundQualityState string

const (
	QualityUnknown     OutboundQualityState = "unknown"
	QualityAvailable   OutboundQualityState = "available"
	QualitySuspect     OutboundQualityState = "suspect"
	QualityUnavailable OutboundQualityState = "unavailable"
	QualityRecovering  OutboundQualityState = "recovering"
)

type QualityLatency struct {
	Mean              time.Duration
	Deviation         time.Duration
	Baseline          time.Duration
	BaselineDeviation time.Duration
	Latest            time.Duration
	RecentMean        time.Duration
	RecentDeviation   time.Duration
	LastFailed        bool
	Samples           uint64
	Failures          uint64
	RecentSamples     uint64
	RecentFailures    uint64
	Buckets           int
	Updated           time.Time
	FreshUntil        time.Time
}

type QualityLoss struct {
	Sent       uint64
	Lost       uint64
	Buckets    int
	Updated    time.Time
	Corrected  bool
	RecentSent uint64
	RecentLost uint64
}

// OutboundQuality 是分来源的值快照；零样本不代表测得零延迟或零丢包。
type OutboundQuality struct {
	Tag                      string
	Generation               uint64
	State                    OutboundQualityState
	Reason                   string
	Version                  uint64
	Updated                  time.Time
	Probe                    QualityLatency
	RTT                      QualityLatency
	Connect                  QualityLatency
	Loss                     QualityLoss
	ClosedConnections        uint64
	FailedConnections        uint64
	ConnectionFailureUpdated time.Time
}

type OutboundQualitySnapshot struct {
	Time      time.Time
	Epoch     int64
	Version   uint64
	Outbounds []OutboundQuality
}

// TransportQualitySample 保持本端发送统计口径；不包含对端的发送判失。
type TransportQualitySample struct {
	SmoothedRTT  time.Duration
	LatestRTT    time.Duration
	RTTDeviation time.Duration
	PacketsSent  uint64
	PacketsLost  uint64
	PacketsRead  uint64
	BytesSent    uint64
	BytesLost    uint64
}

type TransportQualityEventKind uint8

const (
	TransportQualityStarted TransportQualityEventKind = iota
	TransportQualityAttached
	TransportQualityHandshake
	TransportQualityClosed
)

// Stats 只用于 Attached，回调必须是无网络 I/O 的连接快照读取。
type TransportQualityEvent struct {
	ID       uint64
	Kind     TransportQualityEventKind
	Duration time.Duration
	Failed   bool
	Reason   string
	Peer     string
	Stats    func() TransportQualitySample
	Sample   *TransportQualitySample
}

// OutboundQualityReporter 随一个 outbound handler 生存，Close 后的迟到事件被忽略。
type OutboundQualityReporter interface {
	Enabled() bool
	ReportTransport(TransportQualityEvent)
	Close()
}

// OutboundQualityObservatory 是两种 observatory 与新 Champion 共用的可选边界。
// 旧 Observatory 及其 overlay 的契约不变。
type OutboundQualityObservatory interface {
	EnableOutboundQuality()
	NewOutboundQualityReporter(tag string) OutboundQualityReporter
	GetOutboundQuality() OutboundQualitySnapshot
}

type freshQualityProbeKey struct{}

// FreshQualityProbe 只用于验证曾失败的新建连接阶段，不改变普通业务复用。
func FreshQualityProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshQualityProbeKey{}, true)
}

func IsFreshQualityProbe(ctx context.Context) bool {
	fresh, _ := ctx.Value(freshQualityProbeKey{}).(bool)
	return fresh
}
