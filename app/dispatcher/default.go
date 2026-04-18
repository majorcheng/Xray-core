package dispatcher

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/routing"
	routing_session "github.com/xtls/xray-core/features/routing/session"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

var errSniffingTimeout = errors.New("timeout on sniffing")

type cachedReader struct {
	sync.Mutex
	reader buf.TimeoutReader // *pipe.Reader or *buf.TimeoutWrapperReader
	cache  buf.MultiBuffer
}

func (r *cachedReader) Cache(b *buf.Buffer, deadline time.Duration) error {
	mb, err := r.reader.ReadMultiBufferTimeout(deadline)
	if err != nil {
		return err
	}
	r.Lock()
	if !mb.IsEmpty() {
		r.cache, _ = buf.MergeMulti(r.cache, mb)
	}
	b.Clear()
	rawBytes := b.Extend(min(r.cache.Len(), b.Cap()))
	n := r.cache.Copy(rawBytes)
	b.Resize(0, int32(n))
	r.Unlock()
	return nil
}

func (r *cachedReader) readInternal() buf.MultiBuffer {
	r.Lock()
	defer r.Unlock()

	if r.cache != nil && !r.cache.IsEmpty() {
		mb := r.cache
		r.cache = nil
		return mb
	}

	return nil
}

func (r *cachedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb := r.readInternal()
	if mb != nil {
		return mb, nil
	}

	return r.reader.ReadMultiBuffer()
}

func (r *cachedReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb := r.readInternal()
	if mb != nil {
		return mb, nil
	}

	return r.reader.ReadMultiBufferTimeout(timeout)
}

func (r *cachedReader) Interrupt() {
	r.Lock()
	if r.cache != nil {
		r.cache = buf.ReleaseMulti(r.cache)
	}
	r.Unlock()
	if p, ok := r.reader.(*pipe.Reader); ok {
		p.Interrupt()
	}
}

// DefaultDispatcher is a default implementation of Dispatcher.
type DefaultDispatcher struct {
	ohm         outbound.Manager
	router      routing.Router
	policy      policy.Manager
	stats       stats.Manager
	fdns        dns.FakeDNSEngine
	observatory extension.Observatory
}

func protocolMatchesOverride(configured string, sniffed string, result SniffResult) bool {
	configured = strings.ToLower(configured)
	sniffed = strings.ToLower(sniffed)

	if configured == sniffed {
		return true
	}

	if subsetResult, ok := result.(SnifferIsProtoSubsetOf); ok && subsetResult.IsProtoSubsetOf(configured) {
		return true
	}

	if configured == "http" {
		return sniffed == "http1" || sniffed == "http2"
	}

	return false
}

func sniffedProtocolForLog(result SniffResult) string {
	protocolString := result.Protocol()
	if resComp, ok := result.(SnifferResultComposite); ok {
		protocolString = resComp.ProtocolForDomainResult()
	}
	return protocolString
}

func setAccessMessageFromSniffResult(ctx context.Context, destination net.Destination, result SniffResult) {
	accessMessage := log.AccessMessageFromContext(ctx)
	if accessMessage == nil {
		return
	}

	protocolString := sniffedProtocolForLog(result)
	if protocolString != "" {
		accessMessage.Reason = "[" + protocolString + "]"
	}

	if domain := result.Domain(); domain != "" {
		target := destination
		target.Address = net.ParseAddress(domain)
		accessMessage.To = target
	}
}

func (d *DefaultDispatcher) getObservedDelay(ctx context.Context, outboundTag string) (int64, bool) {
	if d.observatory == nil || outboundTag == "" {
		return 0, false
	}
	report, err := d.observatory.GetObservation(ctx)
	if err != nil {
		return 0, false
	}
	result, ok := report.(*observatory.ObservationResult)
	if !ok {
		return 0, false
	}
	for _, status := range result.Status {
		if status == nil || status.OutboundTag != outboundTag {
			continue
		}
		if status.Alive && status.Delay > 0 {
			return status.Delay, true
		}
		return 0, false
	}
	return 0, false
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		d := new(DefaultDispatcher)
		if err := core.RequireFeatures(ctx, func(om outbound.Manager, router routing.Router, pm policy.Manager, sm stats.Manager, dc dns.Client) error {
			core.OptionalFeatures(ctx, func(fdns dns.FakeDNSEngine) {
				d.fdns = fdns
			})
			core.OptionalFeatures(ctx, func(observatory extension.Observatory) {
				d.observatory = observatory
			})
			return d.Init(config.(*Config), om, router, pm, sm)
		}); err != nil {
			return nil, err
		}
		return d, nil
	}))
}

// Init initializes DefaultDispatcher.
func (d *DefaultDispatcher) Init(config *Config, om outbound.Manager, router routing.Router, pm policy.Manager, sm stats.Manager) error {
	d.ohm = om
	d.router = router
	d.policy = pm
	d.stats = sm
	return nil
}

// Type implements common.HasType.
func (*DefaultDispatcher) Type() interface{} {
	return routing.DispatcherType()
}

// Start implements common.Runnable.
func (*DefaultDispatcher) Start() error {
	return nil
}

// Close implements common.Closable.
func (*DefaultDispatcher) Close() error { return nil }

func (d *DefaultDispatcher) getLink(ctx context.Context) (*transport.Link, *transport.Link) {
	opt := pipe.OptionsFromContext(ctx)
	uplinkReader, uplinkWriter := pipe.New(opt...)
	downlinkReader, downlinkWriter := pipe.New(opt...)

	inboundLink := &transport.Link{
		Reader: downlinkReader,
		Writer: uplinkWriter,
	}

	outboundLink := &transport.Link{
		Reader: uplinkReader,
		Writer: downlinkWriter,
	}

	sessionInbound := session.InboundFromContext(ctx)
	var user *protocol.MemoryUser
	if sessionInbound != nil {
		user = sessionInbound.User
	}

	if user != nil && len(user.Email) > 0 {
		p := d.policy.ForLevel(user.Level)
		if p.Stats.UserUplink {
			name := "user>>>" + user.Email + ">>>traffic>>>uplink"
			if c, _ := stats.GetOrRegisterCounter(d.stats, name); c != nil {
				inboundLink.Writer = &SizeStatWriter{
					Counter: c,
					Writer:  inboundLink.Writer,
				}
			}
		}
		if p.Stats.UserDownlink {
			name := "user>>>" + user.Email + ">>>traffic>>>downlink"
			if c, _ := stats.GetOrRegisterCounter(d.stats, name); c != nil {
				outboundLink.Writer = &SizeStatWriter{
					Counter: c,
					Writer:  outboundLink.Writer,
				}
			}
		}

		if p.Stats.UserOnline {
			trackOnlineIP(ctx, d.stats, user.Email, sessionInbound.Source.Address.String())
		}
	}

	return inboundLink, outboundLink
}

func WrapLink(ctx context.Context, policyManager policy.Manager, statsManager stats.Manager, link *transport.Link) *transport.Link {
	sessionInbound := session.InboundFromContext(ctx)
	var user *protocol.MemoryUser
	if sessionInbound != nil {
		user = sessionInbound.User
	}

	link.Reader = &buf.TimeoutWrapperReader{Reader: link.Reader}

	if user != nil && len(user.Email) > 0 {
		p := policyManager.ForLevel(user.Level)
		if p.Stats.UserUplink {
			name := "user>>>" + user.Email + ">>>traffic>>>uplink"
			if c, _ := stats.GetOrRegisterCounter(statsManager, name); c != nil {
				link.Reader.(*buf.TimeoutWrapperReader).Counter = c
			}
		}
		if p.Stats.UserDownlink {
			name := "user>>>" + user.Email + ">>>traffic>>>downlink"
			if c, _ := stats.GetOrRegisterCounter(statsManager, name); c != nil {
				link.Writer = &SizeStatWriter{
					Counter: c,
					Writer:  link.Writer,
				}
			}
		}
		if p.Stats.UserOnline {
			trackOnlineIP(ctx, statsManager, user.Email, sessionInbound.Source.Address.String())
		}
	}

	return link
}

func trackOnlineIP(ctx context.Context, sm stats.Manager, email, ip string) {
	name := "user>>>" + email + ">>>online"
	if om, _ := stats.GetOrRegisterOnlineMap(sm, name); om != nil {
		om.AddIP(ip)
		context.AfterFunc(ctx, func() { om.RemoveIP(ip) })
	}
}

func (d *DefaultDispatcher) shouldOverride(ctx context.Context, result SniffResult, request session.SniffingRequest, destination net.Destination) bool {
	domain := result.Domain()
	if domain == "" {
		return false
	}
	if request.ExcludeForDomain != nil && request.ExcludeForDomain.MatchAny(strings.ToLower(domain)) {
		return false
	}
	if request.ExcludeForIP != nil && destination.Address.Family().IsIP() && request.ExcludeForIP.Match(destination.Address.IP()) {
		return false
	}
	protocolString := sniffedProtocolForLog(result)
	for _, p := range request.OverrideDestinationForProtocol {
		if protocolMatchesOverride(p, protocolString, result) {
			return true
		}
		if fkr0, ok := d.fdns.(dns.FakeDNSEngineRev0); ok && protocolString != "bittorrent" && p == "fakedns" &&
			fkr0.IsIPInIPPool(destination.Address) {
			errors.LogInfo(ctx, "Using sniffer ", protocolString, " since the fake DNS missed")
			return true
		}
	}

	return false
}

// Dispatch implements routing.Dispatcher.
func (d *DefaultDispatcher) Dispatch(ctx context.Context, destination net.Destination) (*transport.Link, error) {
	if !destination.IsValid() {
		panic("Dispatcher: Invalid destination.")
	}
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		outbounds = []*session.Outbound{{}}
		ctx = session.ContextWithOutbounds(ctx, outbounds)
	}
	ob := outbounds[len(outbounds)-1]
	ob.OriginalTarget = destination
	ob.Target = destination
	content := session.ContentFromContext(ctx)
	if content == nil {
		content = new(session.Content)
		ctx = session.ContextWithContent(ctx, content)
	}

	sniffingRequest := content.SniffingRequest
	inbound, outbound := d.getLink(ctx)
	if !sniffingRequest.Enabled {
		go d.routedDispatch(ctx, outbound, destination)
	} else {
		go func() {
			cReader := &cachedReader{
				reader: outbound.Reader.(*pipe.Reader),
			}
			outbound.Reader = cReader
			result, err := sniffer(ctx, cReader, sniffingRequest.MetadataOnly, destination.Network)
			if err == nil {
				content.Protocol = result.Protocol()
				setAccessMessageFromSniffResult(ctx, destination, result)
			}
			if err == nil && d.shouldOverride(ctx, result, sniffingRequest, destination) {
				domain := result.Domain()
				errors.LogInfo(ctx, "sniffed domain: ", domain)
				destination.Address = net.ParseAddress(domain)
				protocol := sniffedProtocolForLog(result)
				isFakeIP := false
				if fkr0, ok := d.fdns.(dns.FakeDNSEngineRev0); ok && fkr0.IsIPInIPPool(ob.Target.Address) {
					isFakeIP = true
				}
				if sniffingRequest.RouteOnly && protocol != "fakedns" && protocol != "fakedns+others" && !isFakeIP {
					ob.RouteTarget = destination
				} else {
					ob.Target = destination
				}
			}
			d.routedDispatch(ctx, outbound, destination)
		}()
	}
	return inbound, nil
}

// DispatchLink implements routing.Dispatcher.
func (d *DefaultDispatcher) DispatchLink(ctx context.Context, destination net.Destination, outbound *transport.Link) error {
	if !destination.IsValid() {
		return errors.New("Dispatcher: Invalid destination.")
	}
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		outbounds = []*session.Outbound{{}}
		ctx = session.ContextWithOutbounds(ctx, outbounds)
	}
	ob := outbounds[len(outbounds)-1]
	ob.OriginalTarget = destination
	ob.Target = destination
	content := session.ContentFromContext(ctx)
	if content == nil {
		content = new(session.Content)
		ctx = session.ContextWithContent(ctx, content)
	}
	outbound = WrapLink(ctx, d.policy, d.stats, outbound)
	sniffingRequest := content.SniffingRequest
	if !sniffingRequest.Enabled {
		d.routedDispatch(ctx, outbound, destination)
	} else {
		cReader := &cachedReader{
			reader: outbound.Reader.(buf.TimeoutReader),
		}
		outbound.Reader = cReader
		result, err := sniffer(ctx, cReader, sniffingRequest.MetadataOnly, destination.Network)
		if err == nil {
			content.Protocol = result.Protocol()
			setAccessMessageFromSniffResult(ctx, destination, result)
		}
		if err == nil && d.shouldOverride(ctx, result, sniffingRequest, destination) {
			domain := result.Domain()
			errors.LogInfo(ctx, "sniffed domain: ", domain)
			destination.Address = net.ParseAddress(domain)
			protocol := sniffedProtocolForLog(result)
			isFakeIP := false
			if fkr0, ok := d.fdns.(dns.FakeDNSEngineRev0); ok && fkr0.IsIPInIPPool(ob.Target.Address) {
				isFakeIP = true
			}
			if sniffingRequest.RouteOnly && protocol != "fakedns" && protocol != "fakedns+others" && !isFakeIP {
				ob.RouteTarget = destination
			} else {
				ob.Target = destination
			}
		}
		d.routedDispatch(ctx, outbound, destination)
	}

	return nil
}

func sniffer(ctx context.Context, cReader *cachedReader, metadataOnly bool, network net.Network) (SniffResult, error) {
	payload := buf.NewWithSize(32767)
	defer payload.Release()

	sniffer := NewSniffer(ctx)

	metaresult, metadataErr := sniffer.SniffMetadata(ctx)

	if metadataOnly {
		return metaresult, metadataErr
	}

	contentResult, contentErr := func() (SniffResult, error) {
		totalBudget := 350 * time.Millisecond
		readBudget := 120 * time.Millisecond
		maxNoClueAttempts := 3
		maxEmptyReads := 3
		if network == net.Network_UDP {
			totalBudget = 180 * time.Millisecond
			readBudget = 80 * time.Millisecond
			maxNoClueAttempts = 2
			maxEmptyReads = 2
		}

		remainingBudget := totalBudget
		noClueAttempts := 0
		emptyReads := 0
		for {
			if remainingBudget <= 0 {
				return nil, errSniffingTimeout
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				currentReadBudget := readBudget
				if currentReadBudget > remainingBudget {
					currentReadBudget = remainingBudget
				}

				cachingStartingTimeStamp := time.Now()
				err := cReader.Cache(payload, currentReadBudget)
				if err != nil {
					return nil, err
				}
				cachingTimeElapsed := time.Since(cachingStartingTimeStamp)
				remainingBudget -= cachingTimeElapsed

				if !payload.IsEmpty() {
					result, err := sniffer.Sniff(ctx, payload.Bytes(), network)
					switch err {
					case common.ErrNoClue: // No Clue: protocol not matches, and sniffer cannot determine whether there will be a match or not
						noClueAttempts++
					case protocol.ErrProtoNeedMoreData: // Protocol Need More Data: protocol matches, but need more data to complete sniffing
						// keep waiting for more payload until budget is exhausted
					default:
						return result, err
					}
				} else {
					emptyReads++
				}
				if noClueAttempts >= maxNoClueAttempts || emptyReads >= maxEmptyReads {
					return nil, errSniffingTimeout
				}
			}
		}
	}()
	if contentErr != nil && metadataErr == nil {
		return metaresult, nil
	}
	if contentErr == nil && metadataErr == nil {
		return CompositeResult(metaresult, contentResult), nil
	}
	return contentResult, contentErr
}

func (d *DefaultDispatcher) routedDispatch(ctx context.Context, link *transport.Link, destination net.Destination) {
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]

	var handler outbound.Handler
	var ruleTag string

	routingLink := routing_session.AsRoutingContext(ctx)
	inTag := routingLink.GetInboundTag()
	isPickRoute := 0
	if forcedOutboundTag := session.GetForcedOutboundTagFromContext(ctx); forcedOutboundTag != "" {
		ctx = session.SetForcedOutboundTagToContext(ctx, "")
		if h := d.ohm.GetHandler(forcedOutboundTag); h != nil {
			isPickRoute = 1
			errors.LogInfo(ctx, "taking platform initialized detour [", forcedOutboundTag, "] for [", destination, "]")
			handler = h
		} else {
			errors.LogError(ctx, "non existing tag for platform initialized detour: ", forcedOutboundTag)
			common.Close(link.Writer)
			common.Interrupt(link.Reader)
			return
		}
	} else if d.router != nil {
		if route, err := d.router.PickRoute(routingLink); err == nil {
			outTag := route.GetOutboundTag()
			if h := d.ohm.GetHandler(outTag); h != nil {
				isPickRoute = 2
				if route.GetRuleTag() == "" {
					errors.LogInfo(ctx, "taking detour [", outTag, "] for [", destination, "]")
				} else {
					ruleTag = route.GetRuleTag()
					errors.LogInfo(ctx, "Hit route rule: [", ruleTag, "] so taking detour [", outTag, "] for [", destination, "]")
				}
				handler = h
			} else {
				errors.LogWarning(ctx, "non existing outTag: ", outTag)
				common.Close(link.Writer)
				common.Interrupt(link.Reader)
				return // DO NOT CHANGE: the traffic shouldn't be processed by default outbound if the specified outbound tag doesn't exist (yet), e.g., VLESS Reverse Proxy
			}
		} else {
			errors.LogInfo(ctx, "default route for ", destination)
		}
	}

	if handler == nil {
		handler = d.ohm.GetDefaultHandler()
	}

	if handler == nil {
		errors.LogInfo(ctx, "default outbound handler not exist")
		common.Close(link.Writer)
		common.Interrupt(link.Reader)
		return
	}

	ob.Tag = handler.Tag()
	if accessMessage := log.AccessMessageFromContext(ctx); accessMessage != nil {
		if tag := handler.Tag(); tag != "" {
			if inTag == "" {
				accessMessage.Detour = tag
			} else if isPickRoute == 1 {
				accessMessage.Detour = inTag + " ==> " + tag
			} else if isPickRoute == 2 {
				if ruleTag == "" {
					accessMessage.Detour = inTag + " >[NONE]> " + tag
				} else {
					accessMessage.Detour = inTag + " >[" + ruleTag + "]> " + tag
				}
			} else {
				accessMessage.Detour = inTag + " >> " + tag
			}
			if delay, ok := d.getObservedDelay(ctx, tag); ok {
				accessMessage.Delay = delay
			}
		}
		if accessMessage.To == nil {
			accessMessage.To = destination
		}
	}

	dispatchCtx := newOutboundDispatchContext(ctx, d.observatory, handler.Tag())
	handler.Dispatch(dispatchCtx, link)
	// 没有真实系统拨号成功时（如 blackhole 或提前失败），这里兜底补打一条 access log。
	log.RecordAccessMessageFromContext(dispatchCtx)
}
