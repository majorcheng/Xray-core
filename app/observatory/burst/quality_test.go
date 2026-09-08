package burst

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	v2net "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport/internet/tagged"
)

func TestQualityBurstRawHealthcheck(t *testing.T) {
	// 隔离 tagged 拨号到 loopback；真实 HTTP 响应，不访问外部连通性 URL。
	oldDialer := tagged.Dialer
	defer func() { tagged.Dialer = oldDialer }()
	tagged.Dialer = func(ctx context.Context, _ routing.Dispatcher, dest v2net.Destination, _ string) (v2net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", dest.NetAddr())
	}
	for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			h := NewHealthPing(context.Background(), nil, &HealthPingConfig{Destination: server.URL, Interval: int64(10 * time.Second), SamplingCount: 1})
			defer h.cancelCtx()
			h.quality = observatory.NewQualityStore([]string{"proxy-"})
			h.quality.Enable()
			defer h.quality.Close()
			if err := h.Check([]string{"proxy-a"}); err != nil {
				t.Fatal(err)
			}
			values := h.quality.Snapshot().Outbounds
			if len(values) != 1 || values[0].Probe.Samples != 1 || (values[0].Probe.Failures != 0) != (status != http.StatusNoContent) {
				t.Fatalf("raw burst outcome=%+v", values)
			}
			if status == http.StatusNoContent && values[0].State != extension.QualityAvailable {
				t.Fatal("successful raw healthcheck unavailable")
			}
		})
	}
}
