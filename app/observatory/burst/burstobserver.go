package burst

import (
	"context"
	"sync"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	"google.golang.org/protobuf/proto"
)

type Observer struct {
	config *Config
	ctx    context.Context

	statusLock sync.Mutex
	hp         *HealthPing
	overlay    *observatory.RuntimeFeedbackOverlayBridge
	monitored  map[string]struct{}
	quality    *observatory.QualityStore

	finished *done.Instance

	ohm outbound.Manager
}

func (o *Observer) GetObservation(ctx context.Context) (proto.Message, error) {
	o.statusLock.Lock()
	defer o.statusLock.Unlock()
	return &observatory.ObservationResult{Status: o.createResultLocked()}, nil
}

func (o *Observer) ReportOutboundSignal(signal *extension.OutboundSignal) {
	if signal == nil || signal.OutboundTag == "" || !o.acceptsRuntimeFeedback(signal.OutboundTag) {
		return
	}
	o.statusLock.Lock()
	defer o.statusLock.Unlock()
	if !o.monitoredTagLocked(signal.OutboundTag) {
		return
	}
	base, baseTimestamp := o.baseStatusSnapshotForTagLocked(signal.OutboundTag)
	o.overlay.ApplyWithStatusAt(signal, base, baseTimestamp)
}

func (o *Observer) Check(tag []string) {
	o.hp.Check(tag)
}

func (o *Observer) createResultLocked() []*observatory.OutboundStatus {
	base, timestamps := o.createBaseResultLocked()
	return o.overlay.MergeWithTimestamps(base, timestamps)
}

func (o *Observer) createBaseResultLocked() ([]*observatory.OutboundStatus, map[string]int64) {
	var result []*observatory.OutboundStatus
	timestamps := make(map[string]int64)
	o.hp.access.Lock()
	defer o.hp.access.Unlock()
	for name, value := range o.hp.Results {
		stats := value.GetWithCache()
		status := observatory.OutboundStatus{
			Alive:           stats.All != stats.Fail,
			Delay:           stats.Average.Milliseconds(),
			LastErrorReason: "",
			OutboundTag:     name,
			LastSeenTime:    0,
			LastTryTime:     0,
			HealthPing: &observatory.HealthPingMeasurementResult{
				All:       int64(stats.All),
				Fail:      int64(stats.Fail),
				Deviation: int64(stats.Deviation),
				Average:   int64(stats.Average),
				Max:       int64(stats.Max),
				Min:       int64(stats.Min),
			},
		}
		timestamps[name] = value.LastUpdateUnixNano()
		result = append(result, &status)
	}
	return result, timestamps
}

func (o *Observer) baseStatusSnapshotForTagLocked(tag string) (*observatory.OutboundStatus, int64) {
	o.hp.access.Lock()
	defer o.hp.access.Unlock()
	if o.hp.Results == nil {
		return nil, 0
	}
	value, found := o.hp.Results[tag]
	if !found {
		return nil, 0
	}
	stats := value.GetWithCache()
	return &observatory.OutboundStatus{
		Alive:           stats.All != stats.Fail,
		Delay:           stats.Average.Milliseconds(),
		LastErrorReason: "",
		OutboundTag:     tag,
		LastSeenTime:    0,
		LastTryTime:     0,
		HealthPing: &observatory.HealthPingMeasurementResult{
			All:       int64(stats.All),
			Fail:      int64(stats.Fail),
			Deviation: int64(stats.Deviation),
			Average:   int64(stats.Average),
			Max:       int64(stats.Max),
			Min:       int64(stats.Min),
		},
	}, value.LastUpdateUnixNano()
}

// acceptsRuntimeFeedback 只接收当前 subject selector 命中的 tag，避免未监控出站进入 overlay。
func (o *Observer) acceptsRuntimeFeedback(tag string) bool {
	outbounds, ok := o.currentObservedOutbounds()
	if !ok {
		return false
	}
	o.statusLock.Lock()
	defer o.statusLock.Unlock()
	o.setMonitoredTagsLocked(outbounds)
	return o.monitoredTagLocked(tag)
}

func (o *Observer) currentObservedOutbounds() ([]string, bool) {
	if o.config == nil || len(o.config.SubjectSelector) == 0 {
		return nil, false
	}
	hs, ok := o.ohm.(outbound.HandlerSelector)
	if !ok {
		return nil, false
	}
	return hs.Select(o.config.SubjectSelector), true
}

func (o *Observer) setMonitoredTagsLocked(outbounds []string) {
	o.monitored = make(map[string]struct{}, len(outbounds))
	for _, tag := range outbounds {
		o.monitored[tag] = struct{}{}
	}
}

func (o *Observer) monitoredTagLocked(tag string) bool {
	_, ok := o.monitored[tag]
	return ok
}

func (o *Observer) Type() interface{} {
	return extension.ObservatoryType()
}

func (o *Observer) EnableOutboundQuality() {
	if o.quality != nil {
		o.quality.Enable()
	}
}
func (o *Observer) NewOutboundQualityReporter(tag string) extension.OutboundQualityReporter {
	if o.quality == nil {
		return nil
	}
	return o.quality.Reporter(tag)
}
func (o *Observer) GetOutboundQuality() extension.OutboundQualitySnapshot {
	if o.quality == nil {
		return extension.OutboundQualitySnapshot{}
	}
	return o.quality.Snapshot()
}

func (o *Observer) Start() error {
	if o.quality != nil {
		o.quality.Start()
	}
	if o.config != nil && len(o.config.SubjectSelector) != 0 {
		o.finished = done.New()
		o.hp.StartScheduler(func() ([]string, error) {
			hs, ok := o.ohm.(outbound.HandlerSelector)
			if !ok {
				return nil, errors.New("outbound.Manager is not a HandlerSelector")
			}

			outbounds := hs.Select(o.config.SubjectSelector)
			if o.quality != nil {
				o.quality.Prune(outbounds)
			}
			o.statusLock.Lock()
			o.setMonitoredTagsLocked(outbounds)
			o.overlay.Prune(outbounds)
			o.statusLock.Unlock()
			return outbounds, nil
		})
	}
	return nil
}

func (o *Observer) Close() error {
	if o.quality != nil {
		o.quality.Close()
	}
	if o.finished != nil {
		o.hp.StopScheduler()
		return o.finished.Close()
	}
	return nil
}

func New(ctx context.Context, config *Config) (*Observer, error) {
	var outboundManager outbound.Manager
	var dispatcher routing.Dispatcher
	err := core.RequireFeatures(ctx, func(om outbound.Manager, rd routing.Dispatcher) {
		outboundManager = om
		dispatcher = rd
	})
	if err != nil {
		return nil, errors.New("Cannot get depended features").Base(err)
	}
	hp := NewHealthPing(ctx, dispatcher, config.PingConfig)
	quality := observatory.NewQualityStore(config.SubjectSelector)
	hp.quality = quality
	return &Observer{
		config:  config,
		ctx:     ctx,
		ohm:     outboundManager,
		hp:      hp,
		overlay: observatory.NewRuntimeFeedbackOverlayBridge(),
		quality: quality,
	}, nil
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*Config))
	}))
}
