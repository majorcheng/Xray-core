package outbound

import (
	"context"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
)

// relayAwareWriter 在首个成功写回客户端的数据上标记请求已经进入真实转发阶段。
// 读取上行数据只代表客户端已发送请求，不能证明远端连接已经建立。
type relayAwareWriter struct {
	ctx    context.Context
	writer buf.Writer
}

func (w *relayAwareWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	nonEmpty := !mb.IsEmpty()
	err := w.writer.WriteMultiBuffer(mb)
	if err == nil && nonEmpty {
		session.NoteOutboundRelayEstablished(w.ctx)
	}
	return err
}

func (w *relayAwareWriter) Close() error {
	return common.Close(w.writer)
}

func (w *relayAwareWriter) Interrupt() {
	_ = common.Interrupt(w.writer)
}

// newRelayAwareLink 用轻量包装捕获首个成功下行写入，供业务成功恢复 observatory 状态。
func newRelayAwareLink(ctx context.Context, link *transport.Link) *transport.Link {
	if link == nil {
		return nil
	}
	return &transport.Link{
		Reader: link.Reader,
		Writer: &relayAwareWriter{ctx: ctx, writer: link.Writer},
	}
}
