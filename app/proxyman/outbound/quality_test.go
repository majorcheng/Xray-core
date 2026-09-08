package outbound

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/transport/internet"
)

type handlerQualityObserver struct{ store *observatory.QualityStore }

func (o handlerQualityObserver) EnableOutboundQuality() { o.store.Enable() }
func (o handlerQualityObserver) NewOutboundQualityReporter(tag string) extension.OutboundQualityReporter {
	return o.store.Reporter(tag)
}
func (o handlerQualityObserver) GetOutboundQuality() extension.OutboundQualitySnapshot {
	return o.store.Snapshot()
}

func TestOutboundQualityManagerLifecycle(t *testing.T) {
	ctx := context.Background()
	m, err := New(ctx, &proxyman.OutboundConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Start(); err != nil {
		t.Fatal(err)
	}
	store := observatory.NewQualityStore([]string{"line"})
	store.Enable()
	defer store.Close()
	newHandler := func() *Handler {
		return &Handler{tag: "line-a", streamSettings: &internet.MemoryStreamConfig{}, quality: handlerQualityObserver{store}}
	}
	first := newHandler()
	if err = m.AddHandler(ctx, first); err != nil {
		t.Fatal(err)
	}
	reporter := first.streamSettings.OutboundQuality
	store.RecordProbe(store.BeginProbe("line-a", time.Minute), 40*time.Millisecond, false, "")
	generation := store.Snapshot().Outbounds[0].Generation
	if err = m.AddHandler(ctx, newHandler()); err == nil {
		t.Fatal("duplicate add accepted")
	}
	store.Prune(nil)
	if !reporter.Enabled() || store.Snapshot().Outbounds[0].Generation != generation {
		t.Fatal("failed add or stale selector invalidated accepted handler")
	}
	if err = m.RemoveHandler(ctx, "line-a"); err != nil {
		t.Fatal(err)
	}
	if reporter.Enabled() || len(store.Snapshot().Outbounds) != 0 {
		t.Fatal("removed handler retained quality ownership")
	}
	second := newHandler()
	if err = m.AddHandler(ctx, second); err != nil {
		t.Fatal(err)
	}
	first.closeQuality()
	if !second.streamSettings.OutboundQuality.Enabled() {
		t.Fatal("late old close invalidated replacement")
	}
	second.closeQuality()
}
