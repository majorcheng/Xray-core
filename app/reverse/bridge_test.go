package reverse

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

const reverseForwardTestPort = net.Port(443)

type captureDispatcher struct {
	ctx context.Context
}

func (*captureDispatcher) Type() interface{} {
	return routing.DispatcherType()
}

func (*captureDispatcher) Start() error { return nil }

func (*captureDispatcher) Close() error { return nil }

func (d *captureDispatcher) Dispatch(ctx context.Context, dest net.Destination) (*transport.Link, error) {
	d.ctx = ctx
	return &transport.Link{}, nil
}

func (d *captureDispatcher) DispatchLink(ctx context.Context, dest net.Destination, link *transport.Link) error {
	d.ctx = ctx
	return nil
}

func TestBridgeWorkerDispatchFillsEmptyInboundTag(t *testing.T) {
	dispatcher := &captureDispatcher{}
	worker := &BridgeWorker{
		Tag:        "bridge-tag",
		Dispatcher: dispatcher,
	}

	inbound := &session.Inbound{}
	ctx := session.ContextWithInbound(context.Background(), inbound)
	dest := net.TCPDestination(net.DomainAddress("example.com"), reverseForwardTestPort)

	if _, err := worker.Dispatch(ctx, dest); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	got := session.InboundFromContext(dispatcher.ctx)
	if got == nil {
		t.Fatal("Dispatch() did not forward inbound context")
	}
	if got.Tag != worker.Tag {
		t.Fatalf("Dispatch() inbound tag = %q, want %q", got.Tag, worker.Tag)
	}
	if inbound.Tag != worker.Tag {
		t.Fatalf("original inbound tag = %q, want %q", inbound.Tag, worker.Tag)
	}
}

func TestBridgeWorkerDispatchLinkPreservesExistingInboundTag(t *testing.T) {
	dispatcher := &captureDispatcher{}
	worker := &BridgeWorker{
		Tag:        "bridge-tag",
		Dispatcher: dispatcher,
	}

	inbound := &session.Inbound{Tag: "existing-tag"}
	ctx := session.ContextWithInbound(context.Background(), inbound)
	dest := net.TCPDestination(net.DomainAddress("example.com"), reverseForwardTestPort)

	if err := worker.DispatchLink(ctx, dest, &transport.Link{}); err != nil {
		t.Fatalf("DispatchLink() error = %v", err)
	}
	got := session.InboundFromContext(dispatcher.ctx)
	if got == nil {
		t.Fatal("DispatchLink() did not forward inbound context")
	}
	if got.Tag != inbound.Tag {
		t.Fatalf("DispatchLink() inbound tag = %q, want %q", got.Tag, inbound.Tag)
	}
}
