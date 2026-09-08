package splithttp

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/apernet/quic-go"
	"github.com/xtls/xray-core/features/extension"
)

var qualityConnectionSequence atomic.Uint64

type quicQuality struct {
	reporter extension.OutboundQualityReporter
	id       uint64
	started  time.Time
}

func beginQUICQuality(reporter extension.OutboundQualityReporter) *quicQuality {
	if reporter == nil || !reporter.Enabled() {
		return nil
	}
	q := &quicQuality{reporter: reporter, id: qualityConnectionSequence.Add(1), started: time.Now()}
	reporter.ReportTransport(extension.TransportQualityEvent{ID: q.id, Kind: extension.TransportQualityStarted})
	return q
}

func quicQualityFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var applicationError *quic.ApplicationError
	if errors.As(err, &applicationError) {
		// H3_NO_ERROR=0x100；本地关闭 transport 不应成为线路故障。
		return applicationError.ErrorCode != 0 && applicationError.ErrorCode != 0x100
	}
	var transportError *quic.TransportError
	if errors.As(err, &transportError) {
		return transportError.ErrorCode != quic.NoError
	}
	// QUIC 的超时、重置和协商失败也包装 net.ErrClosed，须先识别具体故障。
	var timeout net.Error
	var reset *quic.StatelessResetError
	var version *quic.VersionNegotiationError
	if errors.As(err, &timeout) && timeout.Timeout() || errors.As(err, &reset) || errors.As(err, &version) {
		return true
	}
	return !errors.Is(err, net.ErrClosed)
}

func (q *quicQuality) failed(err error) {
	if q == nil {
		return
	}
	q.reporter.ReportTransport(extension.TransportQualityEvent{ID: q.id, Kind: extension.TransportQualityClosed, Failed: quicQualityFailure(err), Reason: err.Error()})
}

func quicQualitySample(conn *quic.Conn) extension.TransportQualitySample {
	s := conn.ConnectionStats()
	return extension.TransportQualitySample{
		SmoothedRTT: s.SmoothedRTT, LatestRTT: s.LatestRTT, RTTDeviation: s.MeanDeviation,
		PacketsSent: s.PacketsSent, PacketsLost: s.PacketsLost, PacketsRead: s.PacketsReceived,
		BytesSent: s.BytesSent, BytesLost: s.BytesLost,
	}
}

func (q *quicQuality) track(conn *quic.Conn) {
	if q == nil {
		return
	}
	q.reporter.ReportTransport(extension.TransportQualityEvent{
		ID: q.id, Kind: extension.TransportQualityAttached, Peer: conn.RemoteAddr().String(),
		Stats: func() extension.TransportQualitySample { return quicQualitySample(conn) },
	})
	// 每个物理连接一个生命周期观察者，和复用它的逻辑流数无关。
	go func() {
		handshake := false
		select {
		case <-conn.HandshakeComplete():
			handshake = true
		case <-conn.Context().Done():
			select {
			case <-conn.HandshakeComplete():
				handshake = true
			default:
			}
		}
		if handshake {
			q.reporter.ReportTransport(extension.TransportQualityEvent{ID: q.id, Kind: extension.TransportQualityHandshake, Duration: time.Since(q.started)})
			<-conn.Context().Done()
		}
		cause := context.Cause(conn.Context())
		reason := ""
		if cause != nil {
			reason = cause.Error()
		}
		sample := quicQualitySample(conn)
		q.reporter.ReportTransport(extension.TransportQualityEvent{ID: q.id, Kind: extension.TransportQualityClosed, Failed: quicQualityFailure(cause), Reason: reason, Sample: &sample})
	}()
}
