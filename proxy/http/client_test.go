package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type stubDialer struct {
	dial func(context.Context, xnet.Destination) (stat.Connection, error)
}

func (d stubDialer) Dial(ctx context.Context, destination xnet.Destination) (stat.Connection, error) {
	if d.dial == nil {
		return nil, errors.New("dial not configured")
	}
	return d.dial(ctx, destination)
}

func (d stubDialer) DestIpAddress() xnet.IP {
	return nil
}

func (d stubDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

type dummyAddr string

func (a dummyAddr) Network() string { return "tcp" }
func (a dummyAddr) String() string  { return string(a) }

type trackedConn struct {
	closed chan struct{}
}

func newTrackedConn() *trackedConn {
	return &trackedConn{closed: make(chan struct{})}
}

func (c *trackedConn) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *trackedConn) Write(b []byte) (int, error) {
	return len(b), nil
}

func (c *trackedConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *trackedConn) LocalAddr() stdnet.Addr  { return dummyAddr("local") }
func (c *trackedConn) RemoteAddr() stdnet.Addr { return dummyAddr("remote") }
func (c *trackedConn) SetDeadline(time.Time) error {
	return nil
}

func (c *trackedConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *trackedConn) SetWriteDeadline(time.Time) error {
	return nil
}

func resetCachedH2ConnsForTest() {
	cachedH2Mutex.Lock()
	conns := make([]h2Conn, 0, len(cachedH2Conns))
	for _, conn := range cachedH2Conns {
		conns = append(conns, conn)
	}
	cachedH2Conns = nil
	cachedH2Mutex.Unlock()

	for _, conn := range conns {
		retireH2Conn(conn)
	}
}

func TestSetUpHTTPTunnelHTTP1PreservesBufferedPayload(t *testing.T) {
	resetCachedH2ConnsForTest()

	clientConn, serverConn := stdnet.Pipe()
	defer serverConn.Close()

	dest := xnet.TCPDestination(xnet.DomainAddress("proxy.test"), xnet.Port(443))
	target := "example.com:443"
	dialer := stubDialer{
		dial: func(context.Context, xnet.Destination) (stat.Connection, error) {
			return clientConn, nil
		},
	}

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		req, err := http.ReadRequest(bufio.NewReader(serverConn))
		if err != nil {
			serverErr <- err
			return
		}
		if req.Method != http.MethodConnect {
			serverErr <- errors.New("unexpected method")
			return
		}
		if _, err := io.WriteString(serverConn, "HTTP/1.1 200 Connection established\r\n\r\nHELLO"); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	conn, err := setUpHTTPTunnel(context.Background(), dest, target, nil, dialer, nil, nil)
	if err != nil {
		t.Fatalf("setUpHTTPTunnel() error = %v", err)
	}
	defer conn.Close()

	if _, ok := conn.(*BufferedConn); !ok {
		t.Fatalf("expected BufferedConn, got %T", conn)
	}

	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("ReadFull() error = %v", err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("unexpected buffered payload: %q", string(got))
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

func TestSetUpHTTPTunnelHTTP1HandshakeTimeout(t *testing.T) {
	resetCachedH2ConnsForTest()

	clientConn, serverConn := stdnet.Pipe()
	defer serverConn.Close()

	dest := xnet.TCPDestination(xnet.DomainAddress("proxy.test"), xnet.Port(443))
	target := "example.com:443"
	dialer := stubDialer{
		dial: func(context.Context, xnet.Destination) (stat.Connection, error) {
			return clientConn, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := setUpHTTPTunnel(ctx, dest, target, nil, dialer, nil, nil)
	if err == nil {
		t.Fatal("setUpHTTPTunnel() expected timeout error, got nil")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout handling too slow: %v", time.Since(start))
	}
}

func TestStoreCachedH2ConnRetiresPreviousConn(t *testing.T) {
	resetCachedH2ConnsForTest()
	defer resetCachedH2ConnsForTest()

	dest := xnet.TCPDestination(xnet.DomainAddress("proxy.test"), xnet.Port(443))
	oldConn := newTrackedConn()
	newConn := newTrackedConn()

	storeCachedH2Conn(dest, h2Conn{rawConn: oldConn})
	storeCachedH2Conn(dest, h2Conn{rawConn: newConn})

	select {
	case <-oldConn.closed:
	case <-time.After(time.Second):
		t.Fatal("old cached connection was not retired")
	}

	cached, ok := loadCachedH2Conn(dest)
	if !ok {
		t.Fatal("cached connection not found")
	}
	if cached.rawConn != newConn {
		t.Fatalf("unexpected cached rawConn: %T", cached.rawConn)
	}
}
