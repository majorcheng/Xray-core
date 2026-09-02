package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"sync"
	"text/template"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/bytespool"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/retry"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/common/utils"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
	"golang.org/x/net/http2"
)

type Client struct {
	server        *protocol.ServerSpec
	policyManager policy.Manager
	header        []*Header
}

type h2Conn struct {
	rawConn net.Conn
	h2Conn  *http2.ClientConn
}

const (
	connectHandshakeTimeout = 8 * time.Second
	h2RetireTimeout         = 5 * time.Second
)

var (
	cachedH2Mutex   sync.Mutex
	cachedH2Conns   map[net.Destination]h2Conn
	bufioReaderPool = sync.Pool{
		New: func() interface{} {
			return bufio.NewReaderSize(nil, 4096)
		},
	}
)

func loadCachedH2Conn(dest net.Destination) (h2Conn, bool) {
	cachedH2Mutex.Lock()
	conn, found := cachedH2Conns[dest]
	cachedH2Mutex.Unlock()
	return conn, found
}

func sameH2Conn(a, b h2Conn) bool {
	if a.h2Conn != nil || b.h2Conn != nil {
		return a.h2Conn == b.h2Conn
	}
	return a.rawConn == b.rawConn
}

func storeCachedH2Conn(dest net.Destination, conn h2Conn) {
	var oldConn h2Conn
	var hasOldConn bool

	cachedH2Mutex.Lock()
	if cachedH2Conns == nil {
		cachedH2Conns = make(map[net.Destination]h2Conn)
	}
	if old, found := cachedH2Conns[dest]; found && !sameH2Conn(old, conn) {
		oldConn = old
		hasOldConn = true
	}
	cachedH2Conns[dest] = conn
	cachedH2Mutex.Unlock()

	if hasOldConn {
		retireH2Conn(oldConn)
	}
}

func evictCachedH2Conn(dest net.Destination, expect h2Conn) bool {
	var staleConn h2Conn
	evicted := false

	cachedH2Mutex.Lock()
	if current, found := cachedH2Conns[dest]; found && sameH2Conn(current, expect) {
		delete(cachedH2Conns, dest)
		staleConn = current
		evicted = true
	}
	cachedH2Mutex.Unlock()

	if evicted {
		retireH2Conn(staleConn)
	}
	return evicted
}

func retireH2Conn(conn h2Conn) {
	if conn.h2Conn == nil {
		if conn.rawConn != nil {
			_ = conn.rawConn.Close()
		}
		return
	}

	go func() {
		conn.h2Conn.SetDoNotReuse()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), h2RetireTimeout)
		err := conn.h2Conn.Shutdown(shutdownCtx)
		cancel()
		if err != nil && conn.rawConn != nil {
			_ = conn.rawConn.Close()
		}
	}()
}

func withConnectTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, found := ctx.Deadline(); found {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, connectHandshakeTimeout)
}

func setConnectDeadline(conn net.Conn, ctx context.Context) func() {
	deadline := time.Now().Add(connectHandshakeTimeout)
	if ctxDeadline, found := ctx.Deadline(); found && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return func() {}
	}
	return func() {
		_ = conn.SetDeadline(time.Time{})
	}
}

// NewClient create a new http client based on the given config.
func NewClient(ctx context.Context, config *ClientConfig) (*Client, error) {
	if config.Server == nil {
		return nil, errors.New(`no target server found`)
	}
	server, err := protocol.NewServerSpecFromPB(config.Server)
	if err != nil {
		return nil, errors.New("failed to get server spec").Base(err)
	}

	v := core.MustFromContext(ctx)
	return &Client{
		server:        server,
		policyManager: v.GetFeature(policy.ManagerType()).(policy.Manager),
		header:        config.Header,
	}, nil
}

// Process implements proxy.Outbound.Process. We first create a socket tunnel via HTTP CONNECT method, then redirect all inbound traffic to that tunnel.
func (c *Client) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]
	if !ob.Target.IsValid() {
		return errors.New("target not specified.")
	}
	ob.Name = "http"
	ob.CanSpliceCopy = 2
	target := ob.Target
	targetAddr := target.NetAddr()

	if target.Network == net.Network_UDP {
		return errors.New("UDP is not supported by HTTP outbound")
	}

	server := c.server
	dest := server.Destination
	user := server.User
	var conn stat.Connection

	mbuf, _ := link.Reader.ReadMultiBuffer()
	len := mbuf.Len()
	firstPayload := bytespool.Alloc(len)
	mbuf, _ = buf.SplitBytes(mbuf, firstPayload)
	firstPayload = firstPayload[:len]

	buf.ReleaseMulti(mbuf)
	defer bytespool.Free(firstPayload)

	header, err := fillRequestHeader(ctx, c.header)
	if err != nil {
		return errors.New("failed to fill out header").Base(err)
	}

	if err := retry.ExponentialBackoff(5, 100).On(func() error {
		netConn, err := setUpHTTPTunnel(ctx, dest, targetAddr, user, dialer, header, firstPayload)
		if netConn != nil {
			if _, ok := netConn.(*http2Conn); !ok {
				if _, err := netConn.Write(firstPayload); err != nil {
					netConn.Close()
					return err
				}
			}
			conn = stat.Connection(netConn)
		}
		return err
	}); err != nil {
		return errors.New("failed to find an available destination").Base(err)
	}

	defer func() {
		if err := conn.Close(); err != nil {
			errors.LogInfoInner(ctx, err, "failed to closed connection")
		}
	}()

	p := c.policyManager.ForLevel(0)
	if user != nil {
		p = c.policyManager.ForLevel(user.Level)
	}

	var newCtx context.Context
	var newCancel context.CancelFunc
	if session.TimeoutOnlyFromContext(ctx) {
		newCtx, newCancel = context.WithCancel(context.Background())
	}

	ctx, cancel := context.WithCancel(ctx)
	timer := signal.CancelAfterInactivity(ctx, func() {
		cancel()
		if newCancel != nil {
			newCancel()
		}
	}, p.Timeouts.ConnectionIdle)

	requestFunc := func() error {
		defer timer.SetTimeout(p.Timeouts.DownlinkOnly)
		return buf.Copy(link.Reader, buf.NewWriter(conn), buf.UpdateActivity(timer))
	}
	responseFunc := func() error {
		ob.CanSpliceCopy = 1
		defer timer.SetTimeout(p.Timeouts.UplinkOnly)
		return buf.Copy(buf.NewReader(conn), link.Writer, buf.UpdateActivity(timer))
	}

	if newCtx != nil {
		ctx = newCtx
	}

	responseDonePost := task.OnSuccess(responseFunc, task.Close(link.Writer))
	if err := task.Run(ctx, requestFunc, responseDonePost); err != nil {
		return errors.New("connection ends").Base(err)
	}

	return nil
}

// fillRequestHeader will fill out the template of the headers
func fillRequestHeader(ctx context.Context, header []*Header) ([]*Header, error) {
	if len(header) == 0 {
		return header, nil
	}

	inbound := session.InboundFromContext(ctx)
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]

	var src net.Destination
	if inbound != nil {
		src = inbound.Source
	} else {
		src = net.TCPDestination(net.AnyIP, 0)
	}

	data := struct {
		Source net.Destination
		Target net.Destination
	}{
		Source: src,
		Target: ob.Target,
	}

	filled := make([]*Header, len(header))
	for i, h := range header {
		tmpl, err := template.New(h.Key).Parse(h.Value)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer

		if err = tmpl.Execute(&buf, data); err != nil {
			return nil, err
		}
		filled[i] = &Header{Key: h.Key, Value: buf.String()}
	}

	return filled, nil
}

// setUpHTTPTunnel will create a socket tunnel via HTTP CONNECT method
func setUpHTTPTunnel(ctx context.Context, dest net.Destination, target string, user *protocol.MemoryUser, dialer internet.Dialer, header []*Header, firstPayload []byte) (net.Conn, error) {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: target},
		Header: make(http.Header),
		Host:   target,
	}

	if user != nil && user.Account != nil {
		account, ok := user.Account.(*Account)
		if !ok {
			return nil, errors.New("invalid HTTP account type")
		}
		auth := account.GetUsername() + ":" + account.GetPassword()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth)))
	}

	for _, h := range header {
		req.Header.Set(h.Key, h.Value)
	}
	utils.TryDefaultHeadersWith(req.Header, "nav")

	connectHTTP1 := func(rawConn net.Conn) (net.Conn, error) {
		req.Header.Set("Proxy-Connection", "Keep-Alive")
		clearDeadline := setConnectDeadline(rawConn, ctx)
		defer clearDeadline()

		if err := req.Write(rawConn); err != nil {
			rawConn.Close()
			return nil, err
		}

		br := bufioReaderPool.Get().(*bufio.Reader)
		br.Reset(rawConn)

		resp, err := http.ReadResponse(br, req)
		if err != nil {
			rawConn.Close()
			bufioReaderPool.Put(br)
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			rawConn.Close()
			bufioReaderPool.Put(br)
			return nil, errors.New("Proxy responded with non 200 code: " + resp.Status)
		}

		if br.Buffered() > 0 {
			return &BufferedConn{Conn: rawConn, r: br}, nil
		}

		bufioReaderPool.Put(br)
		return rawConn, nil
	}

	connectHTTP2 := func(rawConn net.Conn, h2clientConn *http2.ClientConn, sharedConn bool) (net.Conn, error) {
		if !sharedConn {
			clearDeadline := setConnectDeadline(rawConn, ctx)
			defer clearDeadline()
		}

		pr, pw := io.Pipe()
		h2Req := req.Clone(context.Background())
		h2Req.Body = pr

		payloadWriteErr := make(chan error, 1)
		if len(firstPayload) > 0 {
			go func() {
				_, err := pw.Write(firstPayload)
				payloadWriteErr <- err
			}()
		} else {
			payloadWriteErr <- nil
		}

		resp, err := h2clientConn.RoundTrip(h2Req)
		if err != nil {
			_ = pw.CloseWithError(err)
			<-payloadWriteErr
			return nil, err
		}

		if err := <-payloadWriteErr; err != nil {
			_ = pw.CloseWithError(err)
			_ = resp.Body.Close()
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			_ = pw.Close()
			_ = resp.Body.Close()
			return nil, errors.New("Proxy responded with non 200 code: " + resp.Status)
		}
		return newHTTP2Conn(rawConn, pw, resp.Body), nil
	}

	if cachedConn, found := loadCachedH2Conn(dest); found {
		rc, cc := cachedConn.rawConn, cachedConn.h2Conn
		if cc != nil && cc.CanTakeNewRequest() {
			proxyConn, err := connectHTTP2(rc, cc, true)
			if err != nil {
				evictCachedH2Conn(dest, cachedConn)
			} else {
				return proxyConn, nil
			}
		} else if cc == nil || cc.State().Closed || cc.State().Closing {
			evictCachedH2Conn(dest, cachedConn)
		}
	}

	connectCtx, cancel := withConnectTimeout(ctx)
	defer cancel()

	rawConn, err := dialer.Dial(connectCtx, dest)
	if err != nil {
		return nil, err
	}

	iConn := stat.TryUnwrapStatsConn(rawConn)

	nextProto := ""
	if tlsConn, ok := iConn.(*tls.Conn); ok {
		if err := tlsConn.HandshakeContext(connectCtx); err != nil {
			rawConn.Close()
			return nil, err
		}
		nextProto = tlsConn.ConnectionState().NegotiatedProtocol
	} else if tlsConn, ok := iConn.(*tls.UConn); ok {
		if err := tlsConn.HandshakeContext(connectCtx); err != nil {
			rawConn.Close()
			return nil, err
		}
		nextProto = tlsConn.ConnectionState().NegotiatedProtocol
	}

	switch nextProto {
	case "", "http/1.1":
		return connectHTTP1(rawConn)
	case "h2":
		t := http2.Transport{}
		h2clientConn, err := t.NewClientConn(rawConn)
		if err != nil {
			rawConn.Close()
			return nil, err
		}

		proxyConn, err := connectHTTP2(rawConn, h2clientConn, false)
		if err != nil {
			retireH2Conn(h2Conn{rawConn: rawConn, h2Conn: h2clientConn})
			return nil, err
		}

		storeCachedH2Conn(dest, h2Conn{
			rawConn: rawConn,
			h2Conn:  h2clientConn,
		})

		return proxyConn, err
	default:
		return nil, errors.New("negotiated unsupported application layer protocol: " + nextProto)
	}
}

func newHTTP2Conn(c net.Conn, pipedReqBody *io.PipeWriter, respBody io.ReadCloser) net.Conn {
	return &http2Conn{Conn: c, in: pipedReqBody, out: respBody}
}

type http2Conn struct {
	net.Conn
	in  *io.PipeWriter
	out io.ReadCloser
}

func (h *http2Conn) Read(p []byte) (n int, err error) {
	return h.out.Read(p)
}

func (h *http2Conn) Write(p []byte) (n int, err error) {
	return h.in.Write(p)
}

func (h *http2Conn) Close() error {
	h.in.Close()
	return h.out.Close()
}

// BufferedConn preserves already-read bytes from CONNECT response parsing.
type BufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *BufferedConn) releaseReader() {
	if c.r != nil {
		bufioReaderPool.Put(c.r)
		c.r = nil
	}
}

func (c *BufferedConn) Read(p []byte) (int, error) {
	if c.r == nil {
		return c.Conn.Read(p)
	}

	n, err := c.r.Read(p)
	if c.r.Buffered() == 0 {
		c.releaseReader()
	}
	if err == io.EOF {
		if n > 0 {
			return n, nil
		}
		return c.Conn.Read(p)
	}
	return n, err
}

func (c *BufferedConn) Close() error {
	c.releaseReader()
	return c.Conn.Close()
}

func init() {
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewClient(ctx, config.(*ClientConfig))
	}))
}
