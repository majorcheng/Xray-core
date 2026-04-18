package outbound

import (
	"context"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
)

// relayAwareWriter 在首个有效写入时标记请求已经进入真实转发阶段。
type relayAwareWriter struct {
	ctx    context.Context
	writer buf.Writer
}

func (w *relayAwareWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if !mb.IsEmpty() {
		session.NoteOutboundRelayEstablished(w.ctx)
	}
	return w.writer.WriteMultiBuffer(mb)
}

func (w *relayAwareWriter) Close() error {
	return common.Close(w.writer)
}

func (w *relayAwareWriter) Interrupt() {
	_ = common.Interrupt(w.writer)
}

// relayAwareReader 在首个有效读取时标记请求已经进入真实转发阶段。
type relayAwareReader struct {
	ctx    context.Context
	reader buf.Reader
}

func (r *relayAwareReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBuffer()
	if !mb.IsEmpty() {
		session.NoteOutboundRelayEstablished(r.ctx)
	}
	return mb, err
}

func (r *relayAwareReader) Close() error {
	return common.Close(r.reader)
}

func (r *relayAwareReader) Interrupt() {
	_ = common.Interrupt(r.reader)
}

func (r *relayAwareReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	if timeoutReader, ok := r.reader.(buf.TimeoutReader); ok {
		mb, err := timeoutReader.ReadMultiBufferTimeout(timeout)
		if !mb.IsEmpty() {
			session.NoteOutboundRelayEstablished(r.ctx)
		}
		return mb, err
	}
	return nil, buf.ErrNotTimeoutReader
}

// newRelayAwareLink 用轻量包装捕获首个真实读写事件，供业务成功恢复 observatory 状态。
func newRelayAwareLink(ctx context.Context, link *transport.Link) *transport.Link {
	if link == nil {
		return nil
	}
	return &transport.Link{
		Reader: &relayAwareReader{ctx: ctx, reader: link.Reader},
		Writer: &relayAwareWriter{ctx: ctx, writer: link.Writer},
	}
}
