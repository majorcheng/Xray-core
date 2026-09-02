package log

import (
	"context"
	stdnet "net"
	"strings"
	"sync/atomic"

	"github.com/xtls/xray-core/common/serial"
)

type logKey int

const (
	accessMessageKey logKey = iota
)

type AccessStatus string

const (
	AccessAccepted = AccessStatus("accepted")
	AccessRejected = AccessStatus("rejected")
)

type AccessMessage struct {
	From   interface{}
	To     interface{}
	Status AccessStatus
	Reason interface{}
	Email  string
	Detour string
	Egress string
	// recorded 用来保证同一条 access message 只写一次，避免真实拨号成功日志
	// 与 dispatcher 兜底日志重复落盘。
	recorded atomic.Bool
}

func (m *AccessMessage) String() string {
	builder := strings.Builder{}
	builder.WriteString("from")
	builder.WriteByte(' ')
	builder.WriteString(serial.ToString(m.From))
	builder.WriteByte(' ')
	builder.WriteString(string(m.Status))
	builder.WriteByte(' ')
	builder.WriteString(serial.ToString(m.To))

	if len(m.Detour) > 0 {
		builder.WriteString(" [")
		builder.WriteString(m.Detour)
		builder.WriteByte(']')
	}

	if reason := serial.ToString(m.Reason); len(reason) > 0 {
		builder.WriteString(" ")
		builder.WriteString(reason)
	}

	if len(m.Email) > 0 {
		builder.WriteString(" email: ")
		builder.WriteString(m.Email)
	}

	if len(m.Egress) > 0 {
		builder.WriteString(" egress: ")
		builder.WriteString(m.Egress)
	}

	return builder.String()
}

func accessFamilyLabel(ip stdnet.IP) string {
	if ip == nil {
		return ""
	}
	if ip4 := ip.To4(); ip4 != nil {
		return "v4"
	}
	return "v6"
}

func accessFamilyFromAddr(addr stdnet.Addr) string {
	if addr == nil {
		return ""
	}
	switch v := addr.(type) {
	case *stdnet.TCPAddr:
		return accessFamilyLabel(v.IP)
	case *stdnet.UDPAddr:
		return accessFamilyLabel(v.IP)
	case *stdnet.IPAddr:
		return accessFamilyLabel(v.IP)
	}

	host, _, err := stdnet.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	host = strings.Trim(host, "[]")
	if zoneIdx := strings.LastIndex(host, "%"); zoneIdx >= 0 {
		host = host[:zoneIdx]
	}
	return accessFamilyLabel(stdnet.ParseIP(host))
}

func (m *AccessMessage) SetEgressFromAddr(addr stdnet.Addr) bool {
	if m == nil {
		return false
	}
	egress := accessFamilyFromAddr(addr)
	if egress == "" {
		return false
	}
	m.Egress = egress
	return true
}

func (m *AccessMessage) MarkLogged() bool {
	if m == nil {
		return false
	}
	return m.recorded.CompareAndSwap(false, true)
}

func RecordAccessMessageFromContext(ctx context.Context) bool {
	accessMessage := AccessMessageFromContext(ctx)
	if accessMessage == nil || !accessMessage.MarkLogged() {
		return false
	}
	Record(accessMessage)
	return true
}

func RecordAccessMessageFromContextWithEgress(ctx context.Context, addr stdnet.Addr) bool {
	accessMessage := AccessMessageFromContext(ctx)
	if accessMessage == nil {
		return false
	}
	accessMessage.SetEgressFromAddr(addr)
	if !accessMessage.MarkLogged() {
		return false
	}
	Record(accessMessage)
	return true
}

func ContextWithAccessMessage(ctx context.Context, accessMessage *AccessMessage) context.Context {
	return context.WithValue(ctx, accessMessageKey, accessMessage)
}

func AccessMessageFromContext(ctx context.Context) *AccessMessage {
	if accessMessage, ok := ctx.Value(accessMessageKey).(*AccessMessage); ok {
		return accessMessage
	}
	return nil
}
