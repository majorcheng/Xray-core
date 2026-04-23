package observatory

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	v2net "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/common/utils"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport/internet/tagged"
	"google.golang.org/protobuf/proto"
)

type Observer struct {
	config *Config
	ctx    context.Context

	statusLock sync.Mutex
	status     []*OutboundStatus
	overlay    *runtimeFeedbackOverlay
	monitored  map[string]struct{}

	finished *done.Instance

	ohm        outbound.Manager
	dispatcher routing.Dispatcher
}

func (o *Observer) GetObservation(ctx context.Context) (proto.Message, error) {
	o.statusLock.Lock()
	defer o.statusLock.Unlock()
	return &ObservationResult{Status: o.snapshotObservationStatusLocked()}, nil
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
	o.overlay.applyWithStatus(signal, o.statusForTagLocked(signal.OutboundTag), 0)
}

func (o *Observer) Type() interface{} {
	return extension.ObservatoryType()
}

func (o *Observer) Start() error {
	if o.config != nil && len(o.config.SubjectSelector) != 0 {
		o.finished = done.New()
		go o.background()
	}
	return nil
}

func (o *Observer) Close() error {
	if o.finished != nil {
		return o.finished.Close()
	}
	return nil
}

func (o *Observer) background() {
	for !o.finished.Done() {
		hs, ok := o.ohm.(outbound.HandlerSelector)
		if !ok {
			errors.LogInfo(o.ctx, "outbound.Manager is not a HandlerSelector")
			return
		}

		outbounds := hs.Select(o.config.SubjectSelector)

		o.clearRemovedOutbounds(outbounds)

		sleepTime := time.Second * 10
		if o.config.ProbeInterval != 0 {
			sleepTime = time.Duration(o.config.ProbeInterval)
		}

		if !o.config.EnableConcurrency {
			sort.Strings(outbounds)
			for _, v := range outbounds {
				result := o.probe(v)
				o.updateStatusForResult(v, &result)
				if o.finished.Done() {
					return
				}
				time.Sleep(sleepTime)
			}
			continue
		}

		ch := make(chan struct{}, len(outbounds))

		for _, v := range outbounds {
			go func(v string) {
				result := o.probe(v)
				o.updateStatusForResult(v, &result)
				ch <- struct{}{}
			}(v)
		}

		for range outbounds {
			select {
			case <-ch:
			case <-o.finished.Wait():
				return
			}
		}
		time.Sleep(sleepTime)
	}
}

func (o *Observer) clearRemovedOutbounds(outbounds []string) {
	o.statusLock.Lock()
	defer o.statusLock.Unlock()
	o.setMonitoredTagsLocked(outbounds)
	if len(o.status) == 0 && len(o.overlay.statusByTag) == 0 {
		return
	}
	var pruned []*OutboundStatus
	for _, status := range o.status {
		if slices.Contains(outbounds, status.OutboundTag) {
			pruned = append(pruned, status)
		}
	}
	o.status = pruned
	o.overlay.prune(outbounds)
}

// acceptsRuntimeFeedback 只接收当前 subject selector 命中的 tag，避免未监控出站污染观测结果。
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

func (o *Observer) probe(outbound string) ProbeResult {
	errorCollectorForRequest := newErrorCollector()

	httpTransport := http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return nil, nil
		},
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var connection net.Conn
			taskErr := task.Run(ctx, func() error {
				// MUST use Xray's built in context system
				dest, err := v2net.ParseDestination(network + ":" + addr)
				if err != nil {
					return errors.New("cannot understand address").Base(err)
				}
				trackedCtx := session.TrackedConnectionError(o.ctx, errorCollectorForRequest)
				conn, err := tagged.Dialer(trackedCtx, o.dispatcher, dest, outbound)
				if err != nil {
					return errors.New("cannot dial remote address ", dest).Base(err)
				}
				connection = conn
				return nil
			})
			if taskErr != nil {
				return nil, errors.New("cannot finish connection").Base(taskErr)
			}
			return connection, nil
		},
		TLSHandshakeTimeout: time.Second * 5,
	}
	httpClient := &http.Client{
		Transport: &httpTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Jar:     nil,
		Timeout: time.Second * 5,
	}
	var GETTime time.Duration
	err := task.Run(o.ctx, func() error {
		startTime := time.Now()
		probeURL := "https://www.google.com/generate_204"
		if o.config.ProbeUrl != "" {
			probeURL = o.config.ProbeUrl
		}
		req, _ := http.NewRequest(http.MethodGet, probeURL, nil)
		utils.TryDefaultHeadersWith(req.Header, "nav")
		response, err := httpClient.Do(req)
		if err != nil {
			return errors.New("outbound failed to relay connection").Base(err)
		}
		if response.Body != nil {
			response.Body.Close()
		}
		endTime := time.Now()
		GETTime = endTime.Sub(startTime)
		return nil
	})
	if err != nil {
		var errorMessage = "the outbound " + outbound + " is dead: GET request failed:" + err.Error() + "with outbound handler report underlying connection failed"
		errors.LogInfoInner(o.ctx, errorCollectorForRequest.UnderlyingError(), errorMessage)
		return ProbeResult{Alive: false, LastErrorReason: errorMessage}
	}
	errors.LogInfo(o.ctx, "the outbound ", outbound, " is alive:", GETTime.Seconds())
	return ProbeResult{Alive: true, Delay: GETTime.Milliseconds()}
}

func (o *Observer) updateStatusForResult(outbound string, result *ProbeResult) {
	o.statusLock.Lock()
	defer o.statusLock.Unlock()
	var status *OutboundStatus
	if location := o.findStatusLocationLockHolderOnly(outbound); location != -1 {
		status = o.status[location]
	} else {
		status = &OutboundStatus{}
		o.status = append(o.status, status)
	}

	status.LastTryTime = time.Now().Unix()
	status.OutboundTag = outbound
	status.Alive = result.Alive
	if result.Alive {
		status.Delay = result.Delay
		status.LastSeenTime = status.LastTryTime
		status.LastErrorReason = ""
	} else {
		status.LastErrorReason = result.LastErrorReason
		status.Delay = deadDelayMs
	}
}

func (o *Observer) snapshotObservationStatusLocked() []*OutboundStatus {
	result := make([]*OutboundStatus, 0, len(o.status)+len(o.overlay.statusByTag))
	seen := make(map[string]struct{}, len(o.status))
	for _, status := range o.status {
		if status == nil {
			continue
		}
		merged := o.overlay.applyToStatus(status, nil)
		result = append(result, merged)
		seen[merged.OutboundTag] = struct{}{}
	}
	for tag := range o.overlay.statusByTag {
		if _, found := seen[tag]; found {
			continue
		}
		if synthetic := o.overlay.synthesize(tag); synthetic != nil {
			result = append(result, synthetic)
		}
	}
	return result
}

func (o *Observer) findStatusLocationLockHolderOnly(outbound string) int {
	for i, v := range o.status {
		if v.OutboundTag == outbound {
			return i
		}
	}
	return -1
}

func (o *Observer) statusForTagLocked(outbound string) *OutboundStatus {
	location := o.findStatusLocationLockHolderOnly(outbound)
	if location == -1 {
		return nil
	}
	return o.status[location]
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
	return &Observer{
		config:     config,
		ctx:        ctx,
		ohm:        outboundManager,
		dispatcher: dispatcher,
		overlay:    newRuntimeFeedbackOverlay(),
	}, nil
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*Config))
	}))
}
