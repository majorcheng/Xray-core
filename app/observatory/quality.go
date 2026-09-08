package observatory

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/features/extension"
)

const (
	qualityRecentBuckets  = 12
	qualityHistoryBuckets = 132
	qualityProbeSamples   = 20
)

type qualityMoments struct {
	samples, failures       uint64
	sum, squares, variation float64
	latest                  time.Duration
	lastFailed              bool
	updated                 time.Time
}

func (m *qualityMoments) add(now time.Time, duration time.Duration, failed bool) {
	m.samples++
	if !now.Before(m.updated) {
		m.updated, m.latest, m.lastFailed = now, duration, failed
	}
	if failed {
		m.failures++
		return
	}
	x := float64(duration)
	m.sum += x
	m.squares += x * x
}

func (m *qualityMoments) merge(other qualityMoments) {
	m.samples += other.samples
	m.failures += other.failures
	m.sum += other.sum
	m.squares += other.squares
	m.variation += other.variation
	if other.updated.After(m.updated) {
		m.updated, m.latest, m.lastFailed = other.updated, other.latest, other.lastFailed
	}
}

func (m qualityMoments) snapshot(ttl time.Duration) extension.QualityLatency {
	result := extension.QualityLatency{Samples: m.samples, Failures: m.failures, Updated: m.updated, Latest: m.latest, LastFailed: m.lastFailed}
	if !m.updated.IsZero() {
		result.FreshUntil = m.updated.Add(ttl)
	}
	if n := m.samples - m.failures; n > 0 {
		mean := m.sum / float64(n)
		result.Mean = time.Duration(mean)
		result.Deviation = time.Duration(math.Max(m.variation/float64(n), math.Sqrt(math.Max(0, m.squares/float64(n)-mean*mean))))
	}
	return result
}

type qualityBucket struct {
	epoch                    int64
	used                     bool
	rtt, connect             qualityMoments
	sent, lost               uint64
	closed, failed           uint64
	corrected                bool
	lossUpdated              time.Time
	connectionFailureUpdated time.Time
}

type qualityHealth struct {
	failures      int
	firstFailure  time.Time
	failureEpoch  int64
	down          bool
	recovery      int
	recoveryEpoch int64
}

func (h *qualityHealth) record(now time.Time, epoch int64, failed bool, consecutiveWindow time.Duration) {
	if failed {
		if consecutiveWindow == 0 && h.failures > 0 && h.failureEpoch == epoch {
			return
		}
		if h.failures == 0 || consecutiveWindow > 0 && now.Sub(h.firstFailure) > consecutiveWindow {
			h.failures, h.firstFailure = 0, now
		}
		h.failures++
		h.failureEpoch = epoch
		h.recovery = 0
		if h.failures >= 2 {
			h.down = true
		}
		return
	}
	h.failures = 0
	if h.down && (h.recovery == 0 || epoch != h.recoveryEpoch) {
		h.recovery++
		h.recoveryEpoch = epoch
		if h.recovery >= 3 {
			h.down, h.recovery = false, 0
		}
	}
}

type qualityConnection struct {
	stats     func() extension.TransportQualitySample
	previous  extension.TransportQualitySample
	handshake bool
	peer      string
}

type qualityProbeSample struct {
	at       time.Time
	duration time.Duration
	failed   bool
}

type outboundQualityState struct {
	generation                                  uint64
	reporter                                    bool
	version                                     uint64
	updated                                     time.Time
	reason                                      string
	probeHealth, connectHealth, transportHealth qualityHealth
	probes                                      [qualityProbeSamples]qualityProbeSample
	probeIndex                                  int
	probeTTL                                    time.Duration
	buckets                                     []qualityBucket
	connections                                 map[uint64]*qualityConnection
}

// QualityProbe 将探测开始时的出站代次传到完成点，旧探测不能污染替换后的出站。
type QualityProbe struct {
	tag             string
	generation      uint64
	FreshConnection bool
}

// QualityStore 由 standard/burst 共用；只在新 Champion 显式启用后采样。
type QualityStore struct {
	mu                                sync.Mutex
	selectors                         []string
	states                            map[string]*outboundQualityState
	sequence                          uint64
	started, enabled, running, closed bool
	done                              chan struct{}
	now                               func() time.Time
	cached                            extension.OutboundQualitySnapshot
	cacheDirty                        bool
}

func NewQualityStore(selectors []string) *QualityStore {
	return &QualityStore{selectors: append([]string(nil), selectors...), states: make(map[string]*outboundQualityState), done: make(chan struct{}), now: time.Now}
}

func (s *QualityStore) matches(tag string) bool {
	for _, prefix := range s.selectors {
		if strings.HasPrefix(tag, prefix) {
			return true
		}
	}
	return false
}

func (s *QualityStore) newStateLocked(tag string) *outboundQualityState {
	if old := s.states[tag]; old != nil {
		clear(old.connections)
	}
	s.sequence++
	v := &outboundQualityState{generation: s.sequence, connections: make(map[uint64]*qualityConnection)}
	s.states[tag] = v
	s.cacheDirty = true
	return v
}

func (s *QualityStore) runLocked() {
	if !s.started || !s.enabled || s.running || s.closed {
		return
	}
	s.running = true
	go func() {
		ticker := time.NewTicker(extension.QualityInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.sample()
			case <-s.done:
				return
			}
		}
	}()
}

func (s *QualityStore) Start() { s.mu.Lock(); defer s.mu.Unlock(); s.started = true; s.runLocked() }

// ponytail: 启用状态随 observatory 实例生存；若需热关闭采样，再增加消费者计数。
func (s *QualityStore) Enable() { s.mu.Lock(); defer s.mu.Unlock(); s.enabled = true; s.runLocked() }
func (s *QualityStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
		clear(s.states)
		s.cached = extension.OutboundQualitySnapshot{}
		s.cacheDirty = true
	}
}

type qualityReporter struct {
	store      *QualityStore
	tag        string
	generation uint64
}

func (s *QualityStore) Reporter(tag string) extension.OutboundQualityReporter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || tag == "" || !s.matches(tag) {
		return nil
	}
	v := s.newStateLocked(tag)
	v.reporter = true
	return &qualityReporter{store: s, tag: tag, generation: v.generation}
}

func (r *qualityReporter) Enabled() bool {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	v := r.store.states[r.tag]
	return r.store.enabled && !r.store.closed && v != nil && v.generation == r.generation
}

func (r *qualityReporter) Close() {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if v := r.store.states[r.tag]; v != nil && v.generation == r.generation {
		clear(v.connections)
		delete(r.store.states, r.tag)
		r.store.cacheDirty = true
	}
}

func qualityEpoch(now time.Time) int64 { return now.UnixNano() / int64(extension.QualityInterval) }

func (v *outboundQualityState) bucket(now time.Time) *qualityBucket {
	if v.buckets == nil {
		v.buckets = make([]qualityBucket, qualityHistoryBuckets)
	}
	epoch := qualityEpoch(now)
	b := &v.buckets[uint64(epoch)%qualityHistoryBuckets]
	if !b.used || b.epoch != epoch {
		*b = qualityBucket{epoch: epoch, used: true}
	}
	return b
}

func (s *QualityStore) Prune(tags []string) {
	current := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		current[tag] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for tag, v := range s.states {
		// 有 reporter 的状态由 handler 注销，旧 selector 快照不能删除刚加入的出站。
		if _, ok := current[tag]; !ok && !v.reporter {
			clear(v.connections)
			delete(s.states, tag)
			s.cacheDirty = true
		}
	}
}

func (s *QualityStore) changed(v *outboundQualityState, now time.Time) {
	s.sequence++
	v.version, v.updated = s.sequence, now
}

func (r *qualityReporter) ReportTransport(event extension.TransportQualityEvent) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.states[r.tag]
	if !s.enabled || s.closed || v == nil || v.generation != r.generation {
		return
	}
	now := s.now()
	if event.Kind == extension.TransportQualityStarted {
		v.connections[event.ID] = &qualityConnection{}
		return
	}
	c := v.connections[event.ID]
	if c == nil {
		return
	}
	switch event.Kind {
	case extension.TransportQualityAttached:
		c.stats, c.peer = event.Stats, event.Peer
	case extension.TransportQualityHandshake:
		if c.handshake {
			return
		}
		c.handshake = true
		v.bucket(now).connect.add(now, event.Duration, false)
		if v.connectHealth.down || v.connectHealth.failures > 0 {
			s.cacheDirty = true
		}
		v.connectHealth.record(now, qualityEpoch(now), false, 10*time.Second)
		s.changed(v, now)
	case extension.TransportQualityClosed:
		if event.Sample != nil {
			s.addSampleLocked(v, c, *event.Sample, now)
		}
		delete(v.connections, event.ID)
		if c.handshake {
			b := v.bucket(now)
			b.closed++
			if event.Failed {
				b.failed++
				b.connectionFailureUpdated = now
				v.transportHealth.record(now, qualityEpoch(now), true, 10*time.Second)
			}
		} else if event.Failed {
			v.bucket(now).connect.add(now, 0, true)
			v.connectHealth.record(now, qualityEpoch(now), true, 10*time.Second)
		}
		if event.Failed {
			s.cacheDirty = true
			v.reason = event.Reason
		}
		if c.handshake || event.Failed {
			s.changed(v, now)
		}
	}
}

func (s *QualityStore) BeginProbe(tag string, ttl time.Duration) QualityProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.closed || !s.matches(tag) {
		return QualityProbe{}
	}
	v := s.states[tag]
	if v == nil {
		v = s.newStateLocked(tag)
	}
	v.probeTTL = ttl
	return QualityProbe{tag: tag, generation: v.generation, FreshConnection: v.connectHealth.down}
}

func (s *QualityStore) RecordProbe(probe QualityProbe, elapsed time.Duration, failed bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.states[probe.tag]
	if s.closed || v == nil || v.generation != probe.generation {
		return
	}
	now := s.now()
	v.probes[v.probeIndex%qualityProbeSamples] = qualityProbeSample{at: now, duration: elapsed, failed: failed}
	v.probeIndex = (v.probeIndex + 1) % qualityProbeSamples
	v.probeHealth.record(now, qualityEpoch(now), failed, 0)
	if !failed {
		// 本地 healthcheck 往返可验证已建立的传输路径，不能验证新握手能力。
		v.transportHealth.record(now, qualityEpoch(now), false, 10*time.Second)
	}
	s.cacheDirty = true
	if failed {
		v.reason = reason
	}
	s.changed(v, now)
}

func (s *QualityStore) sample() {
	type observation struct {
		state      *outboundQualityState
		connection *qualityConnection
		id         uint64
		read       func() extension.TransportQualitySample
	}
	s.mu.Lock()
	var pending []observation
	for _, v := range s.states {
		for id, c := range v.connections {
			if c.stats != nil {
				pending = append(pending, observation{v, c, id, c.stats})
			}
		}
	}
	s.mu.Unlock()
	for _, p := range pending {
		stats := p.read()
		s.mu.Lock()
		if !s.closed && p.state.connections[p.id] == p.connection {
			s.addSampleLocked(p.state, p.connection, stats, s.now())
		}
		s.mu.Unlock()
	}
}

func (s *QualityStore) addSampleLocked(v *outboundQualityState, c *qualityConnection, next extension.TransportQualitySample, now time.Time) {
	previous := c.previous
	c.previous = next
	b := v.bucket(now)
	if next.PacketsSent < previous.PacketsSent || next.PacketsRead < previous.PacketsRead {
		b.corrected = true
		return
	}
	sent := next.PacketsSent - previous.PacketsSent
	read := next.PacketsRead - previous.PacketsRead
	if sent == 0 && read == 0 {
		if next.PacketsLost != previous.PacketsLost || next.BytesLost != previous.BytesLost {
			b.corrected = true
			s.changed(v, now)
		}
		return
	}
	if next.SmoothedRTT > 0 && (next.SmoothedRTT != previous.SmoothedRTT || next.LatestRTT != previous.LatestRTT || next.RTTDeviation != previous.RTTDeviation) {
		b.rtt.add(now, next.SmoothedRTT, false)
		b.rtt.variation += float64(next.RTTDeviation)
	}
	if next.PacketsLost < previous.PacketsLost || next.BytesLost < previous.BytesLost || next.BytesSent < previous.BytesSent {
		b.corrected = true
	} else if lost := next.PacketsLost - previous.PacketsLost; sent > 0 && lost <= sent {
		b.sent += sent
		b.lost += lost
		b.lossUpdated = now
	} else if lost > 0 {
		b.corrected = true
	}
	s.changed(v, now)
}

func (s *QualityStore) Snapshot() extension.OutboundQualitySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	epoch := qualityEpoch(now)
	if !s.cacheDirty && s.cached.Epoch == epoch && !s.cached.Time.IsZero() {
		return s.cached
	}
	result := extension.OutboundQualitySnapshot{Time: now, Epoch: epoch, Version: s.sequence}
	for tag, v := range s.states {
		q := extension.OutboundQuality{Tag: tag, Generation: v.generation, Version: v.version, Updated: v.updated, Reason: v.reason, State: extension.QualityUnknown}
		var probes, rtt, connect, baselineRTT, baselineConnect qualityMoments
		var recentProbes []qualityProbeSample
		for _, p := range v.probes {
			if !p.at.IsZero() && now.Sub(p.at) <= v.probeTTL {
				probes.add(p.at, p.duration, p.failed)
				recentProbes = append(recentProbes, p)
			}
		}
		q.Probe = probes.snapshot(v.probeTTL)
		sort.Slice(recentProbes, func(i, j int) bool { return recentProbes[i].at.After(recentProbes[j].at) })
		var recentProbeMoments qualityMoments
		for _, p := range recentProbes[:min(3, len(recentProbes))] {
			recentProbeMoments.add(p.at, p.duration, p.failed)
		}
		recentProbe := recentProbeMoments.snapshot(0)
		q.Probe.RecentMean, q.Probe.RecentDeviation = recentProbe.Mean, recentProbe.Deviation
		probeEpochs := make(map[int64]struct{})
		for _, p := range v.probes {
			if !p.at.IsZero() && now.Sub(p.at) <= v.probeTTL {
				probeEpochs[qualityEpoch(p.at)] = struct{}{}
			}
		}
		q.Probe.Buckets = len(probeEpochs)
		for _, p := range v.probes {
			if !p.at.IsZero() && qualityEpoch(p.at) == qualityEpoch(q.Probe.Updated) {
				q.Probe.RecentSamples++
				if p.failed {
					q.Probe.RecentFailures++
				}
			}
		}
		latestLossEpoch := int64(-1)
		for _, b := range v.buckets {
			age := epoch - b.epoch
			if !b.used || age < 0 || age >= qualityHistoryBuckets {
				continue
			}
			if age >= qualityRecentBuckets {
				if b.lost == 0 && !b.corrected && b.connect.failures == 0 && b.failed == 0 {
					baselineRTT.merge(b.rtt)
					baselineConnect.merge(b.connect)
				}
				continue
			}
			rtt.merge(b.rtt)
			connect.merge(b.connect)
			if b.rtt.samples > 0 {
				q.RTT.Buckets++
			}
			if b.connect.samples > 0 {
				q.Connect.Buckets++
			}
			q.ClosedConnections += b.closed
			q.FailedConnections += b.failed
			if b.connectionFailureUpdated.After(q.ConnectionFailureUpdated) {
				q.ConnectionFailureUpdated = b.connectionFailureUpdated
			}
			q.Loss.Sent += b.sent
			q.Loss.Lost += b.lost
			q.Loss.Corrected = q.Loss.Corrected || b.corrected
			if b.sent > 0 {
				q.Loss.Buckets++
				if b.epoch > latestLossEpoch {
					latestLossEpoch = b.epoch
					q.Loss.RecentSent, q.Loss.RecentLost = b.sent, b.lost
				}
			}
			if b.lossUpdated.After(q.Loss.Updated) {
				q.Loss.Updated = b.lossUpdated
			}
		}
		rttBuckets, connectBuckets := q.RTT.Buckets, q.Connect.Buckets
		q.RTT = rtt.snapshot(qualityRecentBuckets * extension.QualityInterval)
		q.RTT.Buckets = rttBuckets
		q.Connect = connect.snapshot(qualityRecentBuckets * extension.QualityInterval)
		q.Connect.Buckets = connectBuckets
		for _, b := range v.buckets {
			if !b.used {
				continue
			}
			if b.epoch == qualityEpoch(q.RTT.Updated) {
				recent := b.rtt.snapshot(0)
				q.RTT.RecentSamples = b.rtt.samples
				q.RTT.Latest = recent.Mean
				q.RTT.RecentMean, q.RTT.RecentDeviation = recent.Mean, recent.Deviation
			}
			if b.epoch == qualityEpoch(q.Connect.Updated) {
				q.Connect.RecentSamples, q.Connect.RecentFailures = b.connect.samples, b.connect.failures
				if b.connect.samples > b.connect.failures {
					recent := b.connect.snapshot(0)
					q.Connect.Latest = recent.Mean
					q.Connect.RecentMean, q.Connect.RecentDeviation = recent.Mean, recent.Deviation
				}
			}
		}
		rttBaseline, connectBaseline := baselineRTT.snapshot(0), baselineConnect.snapshot(0)
		q.RTT.Baseline, q.RTT.BaselineDeviation = rttBaseline.Mean, rttBaseline.Deviation
		q.Connect.Baseline, q.Connect.BaselineDeviation = connectBaseline.Mean, connectBaseline.Deviation
		if v.probeHealth.down || v.connectHealth.down || v.transportHealth.down {
			q.State = extension.QualityRecovering
			if v.probeHealth.down && v.probeHealth.recovery == 0 || v.connectHealth.down && v.connectHealth.recovery == 0 || v.transportHealth.down && v.transportHealth.recovery == 0 {
				q.State = extension.QualityUnavailable
			}
		} else if q.Probe.Samples > q.Probe.Failures && now.Before(q.Probe.FreshUntil) {
			q.State = extension.QualityAvailable
			if v.probeHealth.failures > 0 || v.connectHealth.failures > 0 && now.Sub(v.connectHealth.firstFailure) <= 10*time.Second || v.transportHealth.failures > 0 && now.Sub(v.transportHealth.firstFailure) <= 10*time.Second {
				q.State = extension.QualitySuspect
			}
		}
		result.Outbounds = append(result.Outbounds, q)
	}
	s.cached, s.cacheDirty = result, false
	return result
}
