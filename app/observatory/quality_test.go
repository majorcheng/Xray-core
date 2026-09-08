package observatory

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v2net "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport/internet/tagged"
)

func qualityTestStore(t *testing.T) (*QualityStore, *time.Time, extension.OutboundQualityReporter) {
	t.Helper()
	now := time.Unix(1000, 0)
	s := NewQualityStore([]string{"line"})
	s.now = func() time.Time { return now }
	s.Enable()
	t.Cleanup(s.Close)
	return s, &now, s.Reporter("line-a")
}

func TestQualityRawHealthcheckIgnoresLegacyOverlay(t *testing.T) {
	// 只替换 tagged 拨号到本机 HTTP 服务，验证 raw 记录与 overlay 隔离；不验证路由分派。
	oldDialer := tagged.Dialer
	defer func() { tagged.Dialer = oldDialer }()
	tagged.Dialer = func(ctx context.Context, _ routing.Dispatcher, dest v2net.Destination, _ string) (v2net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", dest.NetAddr())
	}
	for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			o := newMonitoredObserver()
			o.ctx = context.Background()
			o.config.ProbeUrl = server.URL
			o.quality = NewQualityStore([]string{"proxy-"})
			o.quality.Enable()
			defer o.quality.Close()
			result := o.probe("proxy-a")
			if !result.Alive {
				t.Fatal("legacy HTTP response contract changed")
			}
			before := qualityTestSnapshot(t, o.quality)
			if before.Probe.Samples != 1 || (before.Probe.Failures != 0) != (status != http.StatusNoContent) {
				t.Fatalf("raw HTTP outcome=%+v", before.Probe)
			}
			o.ReportOutboundSignal(&extension.OutboundSignal{OutboundTag: "proxy-a", Kind: extension.OutboundSignalRelaySuccess, DelayMs: 1})
			after := qualityTestSnapshot(t, o.quality)
			if after.Probe != before.Probe {
				t.Fatal("legacy business overlay modified raw healthcheck")
			}
		})
	}
}

func qualityTestSnapshot(t *testing.T, s *QualityStore) extension.OutboundQuality {
	t.Helper()
	values := s.Snapshot().Outbounds
	if len(values) != 1 {
		t.Fatalf("expected one outbound, got %v", values)
	}
	return values[0]
}

func TestQualityProbeFreshnessAndGeneration(t *testing.T) {
	s, now, reporter := qualityTestStore(t)
	for i := 0; i < 25; i++ {
		*now = now.Add(extension.QualityInterval)
		s.RecordProbe(s.BeginProbe("line-a", time.Minute), time.Duration(i+1)*time.Millisecond, false, "")
	}
	q := qualityTestSnapshot(t, s)
	if q.State != extension.QualityAvailable || !q.Probe.Updated.Equal(*now) || q.Probe.Latest != 25*time.Millisecond {
		t.Fatalf("ring wrap lost newest probe: %+v", q)
	}
	old := s.BeginProbe("line-a", time.Minute)
	s.Prune(nil) // selector 取样可能早于 reporter 加入，不能注销仍归 handler 所有的状态。
	if !reporter.Enabled() {
		t.Fatal("stale selector removed live reporter")
	}
	replacement := s.Reporter("line-a")
	reporter.Close()
	s.RecordProbe(old, time.Millisecond, false, "")
	if reporter.Enabled() || !replacement.Enabled() || qualityTestSnapshot(t, s).Probe.Samples != 0 {
		t.Fatal("old generation affected replacement")
	}
	s.RecordProbe(s.BeginProbe("line-a", time.Minute), 80*time.Millisecond, false, "")
	*now = now.Add(61 * time.Second)
	if q := qualityTestSnapshot(t, s); q.State != extension.QualityUnknown || q.Probe.Samples != 0 {
		t.Fatalf("expired probe treated as healthy: %+v", q)
	}
	replacement.Close()
	if len(s.Snapshot().Outbounds) != 0 {
		t.Fatal("closed reporter retained state")
	}
}

func TestQualityIndependentFailuresAndStageRecovery(t *testing.T) {
	s, now, reporter := qualityTestStore(t)
	s.RecordProbe(s.BeginProbe("line-a", time.Minute), 50*time.Millisecond, false, "")
	id := uint64(0)
	attempt := func(failed bool) {
		id++
		reporter.ReportTransport(extension.TransportQualityEvent{ID: id, Kind: extension.TransportQualityStarted})
		if !failed {
			reporter.ReportTransport(extension.TransportQualityEvent{ID: id, Kind: extension.TransportQualityHandshake, Duration: 30 * time.Millisecond})
		}
		reporter.ReportTransport(extension.TransportQualityEvent{ID: id, Kind: extension.TransportQualityClosed, Failed: failed, Reason: "test failure"})
	}
	for i := 0; i < 100; i++ {
		attempt(i == 20 || i == 70)
	}
	if qualityTestSnapshot(t, s).State != extension.QualityAvailable {
		t.Fatal("interleaved successes must break failure streak")
	}
	attempt(true)
	for i := 0; i < 100; i++ {
		reporter.ReportTransport(extension.TransportQualityEvent{ID: id, Kind: extension.TransportQualityClosed, Failed: true})
	}
	if q := qualityTestSnapshot(t, s); q.State != extension.QualitySuspect || q.Connect.Failures != 3 {
		t.Fatalf("duplicate close amplified failure: %+v", q)
	}
	attempt(true)
	if qualityTestSnapshot(t, s).State != extension.QualityUnavailable {
		t.Fatal("two independent consecutive failures did not fail fast")
	}
	for i := 0; i < 3; i++ {
		*now = now.Add(extension.QualityInterval)
		probe := s.BeginProbe("line-a", time.Minute)
		if !probe.FreshConnection {
			t.Fatal("recovery must test a new physical connection")
		}
		s.RecordProbe(probe, 50*time.Millisecond, false, "")
	}
	if qualityTestSnapshot(t, s).State != extension.QualityUnavailable {
		t.Fatal("reused healthcheck restored failed handshake stage")
	}
	for i := 0; i < 10; i++ {
		attempt(false)
	}
	if qualityTestSnapshot(t, s).State != extension.QualityRecovering {
		t.Fatal("same epoch successes recovered too early")
	}
	for i := 0; i < 2; i++ {
		*now = now.Add(extension.QualityInterval)
		attempt(false)
	}
	if q := qualityTestSnapshot(t, s); q.State != extension.QualityAvailable || q.Connect.Failures != 4 {
		t.Fatalf("stage recovery lost rolling history or remained down: %+v", q)
	}
}

func TestQualityLossCorrectionsAndRTTFreshness(t *testing.T) {
	s, now, reporter := qualityTestStore(t)
	sample := extension.TransportQualitySample{SmoothedRTT: 40 * time.Millisecond, LatestRTT: 40 * time.Millisecond, RTTDeviation: 5 * time.Millisecond}
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityStarted})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityAttached, Stats: func() extension.TransportQualitySample { return sample }})
	for i := 0; i < 3; i++ {
		*now = now.Add(extension.QualityInterval)
		sample.PacketsSent += 100
		sample.PacketsRead += 90
		sample.PacketsLost++
		s.sample()
	}
	q := qualityTestSnapshot(t, s)
	if q.Loss.Sent != 300 || q.Loss.Lost != 3 || q.Loss.Buckets != 3 || q.RTT.Samples != 1 {
		t.Fatalf("loss or static RTT incorrectly counted: %+v", q)
	}
	rttUpdated := q.RTT.Updated
	*now = now.Add(extension.QualityInterval)
	sample.PacketsSent += 100
	sample.PacketsRead += 90
	sample.PacketsLost-- // 迟到 ACK 修正。
	s.sample()
	q = qualityTestSnapshot(t, s)
	if !q.Loss.Corrected || q.Loss.Sent != 300 || q.Loss.Lost != 3 || !q.RTT.Updated.Equal(rttUpdated) {
		t.Fatalf("counter rollback fabricated zero loss or fresh RTT: %+v", q)
	}
	*now = now.Add(extension.QualityInterval)
	sample.PacketsLost += 5 // 判失可以晚于发送，零发送分母不可当成无丢包。
	s.sample()
	if q = qualityTestSnapshot(t, s); !q.Loss.Corrected || q.Loss.Lost != 3 {
		t.Fatalf("invalid denominator used: %+v", q.Loss)
	}
	*now = now.Add(time.Minute)
	s.sample()
	if q = qualityTestSnapshot(t, s); q.RTT.Samples != 0 || q.Loss.Sent != 0 {
		t.Fatalf("idle stats kept old evidence fresh: %+v", q)
	}
}

func TestQualityShortConnectionAndProbeFailureEpochs(t *testing.T) {
	s, now, reporter := qualityTestStore(t)
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityStarted})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityHandshake, Duration: 20 * time.Millisecond})
	sample := extension.TransportQualitySample{SmoothedRTT: 10 * time.Millisecond, PacketsSent: 50, PacketsRead: 40, PacketsLost: 1}
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityClosed, Sample: &sample})
	if q := qualityTestSnapshot(t, s); q.Loss.Sent != 50 || q.RTT.Samples != 1 || q.ClosedConnections != 1 || q.FailedConnections != 0 {
		t.Fatalf("short connection final stats missing: %+v", q)
	}
	s.RecordProbe(s.BeginProbe("line-a", time.Minute), 40*time.Millisecond, false, "")
	for i := 0; i < 20; i++ {
		s.RecordProbe(s.BeginProbe("line-a", time.Minute), 0, true, "HTTP 500")
	}
	if q := qualityTestSnapshot(t, s); q.State == extension.QualityUnavailable {
		t.Fatal("one probe epoch counted as multiple failures")
	}
	*now = now.Add(extension.QualityInterval)
	s.RecordProbe(s.BeginProbe("line-a", time.Minute), 0, true, "HTTP 500")
	if qualityTestSnapshot(t, s).State != extension.QualityUnavailable {
		t.Fatal("distinct failed epochs did not mark unavailable")
	}
	for i := 0; i < 3; i++ {
		*now = now.Add(extension.QualityInterval)
		s.RecordProbe(s.BeginProbe("line-a", time.Minute), 40*time.Millisecond, false, "")
	}
	if qualityTestSnapshot(t, s).State != extension.QualityAvailable {
		t.Fatal("probe stage did not recover")
	}
}

func TestQualityFailureStagesDoNotCombine(t *testing.T) {
	s, now, reporter := qualityTestStore(t)
	s.RecordProbe(s.BeginProbe("line-a", time.Minute), 40*time.Millisecond, false, "")
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityStarted})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityHandshake, Duration: 20 * time.Millisecond})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 1, Kind: extension.TransportQualityClosed, Failed: true})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 2, Kind: extension.TransportQualityStarted})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 2, Kind: extension.TransportQualityClosed, Failed: true})
	if qualityTestSnapshot(t, s).State == extension.QualityUnavailable {
		t.Fatal("one established failure plus one handshake failure counted as two same-stage failures")
	}
	*now = now.Add(extension.QualityInterval)
	s.RecordProbe(s.BeginProbe("line-a", time.Minute), 40*time.Millisecond, false, "")
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 3, Kind: extension.TransportQualityStarted})
	reporter.ReportTransport(extension.TransportQualityEvent{ID: 3, Kind: extension.TransportQualityClosed, Failed: true})
	if qualityTestSnapshot(t, s).State != extension.QualityUnavailable {
		t.Fatal("reused probe interrupted handshake failure streak")
	}
}
