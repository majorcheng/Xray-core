package splithttp

import (
	"context"
	gotls "crypto/tls"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	v2net "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tls"
)

func TestQUICQualityTimeout(t *testing.T) {
	if !quicQualityFailure(&quic.HandshakeTimeoutError{}) {
		t.Fatal("QUIC handshake timeout was classified as normal close")
	}
}

func TestQUICQualityCloseClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		failed bool
	}{
		{nil, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{net.ErrClosed, false},
		{&quic.ApplicationError{ErrorCode: 0}, false},
		{&quic.ApplicationError{ErrorCode: 0x100}, false},
		{&quic.ApplicationError{ErrorCode: 0x101}, true},
		{&quic.TransportError{ErrorCode: quic.NoError}, false},
		{&quic.TransportError{ErrorCode: quic.ConnectionRefused}, true},
		{&quic.StatelessResetError{}, true},
		{&quic.IdleTimeoutError{}, true},
		{fmt.Errorf("wrapped: %w", &quic.HandshakeTimeoutError{}), true},
	} {
		if got := quicQualityFailure(tc.err); got != tc.failed {
			t.Errorf("%v failed=%v want %v", tc.err, got, tc.failed)
		}
	}
}

type quicQualityEvents chan extension.TransportQualityEvent

func (quicQualityEvents) Enabled() bool                                           { return true }
func (quicQualityEvents) Close()                                                  {}
func (e quicQualityEvents) ReportTransport(event extension.TransportQualityEvent) { e <- event }

func TestQUICQualityH3Lifecycle(t *testing.T) {
	// 真实 loopback H3 握手/HTTP 往返；不验证广域网丢包或 server 发送端统计。
	certificate, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
	certPEM, keyPEM := certificate.ToPEM()
	pair, err := gotls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := quic.ListenAddrEarly("127.0.0.1:0", &gotls.Config{Certificates: []gotls.Certificate{pair}, NextProtos: []string{"h3"}}, &quic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	server := &http3.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	served := make(chan error, 1)
	go func() { served <- server.ServeListener(listener) }()
	t.Cleanup(func() { server.Close(); <-served })
	events := make(quicQualityEvents, 16)
	settings := &internet.MemoryStreamConfig{
		ProtocolName: "splithttp", ProtocolSettings: &Config{Path: "/health"}, SecurityType: "tls",
		SecuritySettings: &tls.Config{NextProtocol: []string{"h3"}, ServerName: "localhost", PinnedPeerCertSha256: [][]byte{hash[:]}},
		QuicParams:       &internet.QuicParams{DisableChromeParrot: true, Congestion: "reno"}, OutboundQuality: events,
	}
	dest := v2net.UDPDestination(v2net.LocalHostIP, v2net.Port(listener.Addr().(*net.UDPAddr).Port))
	client := createHTTPClient(dest, settings).(*DefaultDialerClient)
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	client.Close()
	var id uint64
	var started, attached, handshake int
	for {
		select {
		case e := <-events:
			if id == 0 {
				id = e.ID
			}
			if e.ID != id {
				t.Fatal("one physical connection reported multiple identities")
			}
			switch e.Kind {
			case extension.TransportQualityStarted:
				started++
			case extension.TransportQualityAttached:
				attached++
				if e.Peer == "" || e.Stats == nil {
					t.Fatal("connection ownership/stats absent")
				}
			case extension.TransportQualityHandshake:
				handshake++
				if e.Duration <= 0 {
					t.Fatal("missing handshake duration")
				}
			case extension.TransportQualityClosed:
				if started != 1 || attached != 1 || handshake != 1 || e.Failed || e.Sample == nil || e.Sample.PacketsSent == 0 || e.Sample.SmoothedRTT <= 0 {
					t.Fatalf("invalid H3 lifecycle: started=%d attached=%d handshake=%d final=%+v", started, attached, handshake, e)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("H3 quality lifecycle did not finish", ctx.Err())
		}
	}
}

func TestQUICQualityFreshProbeBypassesPool(t *testing.T) {
	settings := &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: &Config{}}
	dest := v2net.TCPDestination(v2net.LocalHostIP, 443)
	regular, pool := getHTTPClient(context.Background(), dest, settings)
	defer func() {
		globalDialerAccess.Lock()
		delete(globalDialerMap, dialerConf{dest, settings})
		globalDialerAccess.Unlock()
	}()
	fresh, privatePool := getHTTPClient(extension.FreshQualityProbe(context.Background()), dest, settings)
	if pool == nil || privatePool != nil || regular == fresh {
		t.Fatal("recovery probe reused ordinary pool")
	}
	regular.(*DefaultDialerClient).Close()
	fresh.(*DefaultDialerClient).Close()
}
