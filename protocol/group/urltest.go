package group

import (
	"context"
	"errors"
	"maps"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

const (
	urlTestBreakerThreshold = 3
	urlTestBreakerCooldown  = 30 * time.Second
)

// outboundBreaker is a per-outbound, per-network circuit breaker driven by real
// connection results. It never participates in delay-based ranking; it only
// temporarily excludes an in-use outbound that keeps failing real dials.
type outboundBreaker struct {
	access       sync.Mutex
	failures     int32
	trippedUntil int64 // unix-nano cooldown deadline; 0 = healthy
}

func (b *outboundBreaker) tripped(now int64) bool {
	b.access.Lock()
	defer b.access.Unlock()
	return b.trippedUntil > now
}

func (b *outboundBreaker) until() int64 {
	b.access.Lock()
	defer b.access.Unlock()
	return b.trippedUntil
}

func (b *outboundBreaker) recordSuccess() {
	b.access.Lock()
	defer b.access.Unlock()
	b.failures = 0
	b.trippedUntil = 0
}

func (b *outboundBreaker) recordFailure(now int64) bool {
	b.access.Lock()
	defer b.access.Unlock()
	if b.trippedUntil > now {
		return false
	}
	b.failures++
	if b.failures < urlTestBreakerThreshold {
		return false
	}
	b.failures = 0
	b.trippedUntil = time.Unix(0, now).Add(urlTestBreakerCooldown).UnixNano()
	return true
}

func RegisterURLTest(registry *outbound.Registry) {
	outbound.Register[option.URLTestOutboundOptions](registry, C.TypeURLTest, NewURLTest)
}

var (
	_ adapter.OutboundGroup           = (*URLTest)(nil)
	_ adapter.InterfaceUpdateListener = (*URLTest)(nil)
	_ adapter.Referrer                = (*URLTest)(nil)
)

type URLTest struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	connection                   adapter.ConnectionManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	group                        *URLTestGroup
	checkAccess                  sync.Mutex
	interruptExternalConnections bool
}

func NewURLTest(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.URLTestOutboundOptions) (adapter.Outbound, error) {
	outbound := &URLTest{
		Adapter:                      outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		tolerance:                    options.Tolerance,
		idleTimeout:                  time.Duration(options.IdleTimeout),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if len(outbound.tags) == 0 {
		return nil, E.New("missing tags")
	}
	return outbound, nil
}

func (s *URLTest) Start() error {
	outbounds := make([]adapter.Outbound, 0, len(s.tags))
	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return E.New("outbound ", i, " not found: ", tag)
		}
		outbounds = append(outbounds, detour)
	}
	group, err := NewURLTestGroup(s.ctx, s.outbound, s.logger, outbounds, s.link, s.interval, s.tolerance, s.idleTimeout, s.interruptExternalConnections)
	if err != nil {
		return err
	}
	s.group = group
	return nil
}

func (s *URLTest) PostStart() error {
	s.group.PostStart()
	return nil
}

func (s *URLTest) Close() error {
	return common.Close(
		common.PtrOrNil(s.group),
	)
}

func (s *URLTest) Now() string {
	if outbound := s.group.selectedOutboundTCP.Load(); outbound != nil {
		return outbound.Tag()
	} else if outbound := s.group.selectedOutboundUDP.Load(); outbound != nil {
		return outbound.Tag()
	}
	return ""
}

func (s *URLTest) All() []string {
	return s.tags
}

func (s *URLTest) References() []string {
	group := s.group
	if group == nil {
		return nil
	}
	var references []string
	selectedTCP := group.selectedOutboundTCP.Load()
	selectedUDP := group.selectedOutboundUDP.Load()
	if selectedTCP != nil {
		references = append(references, selectedTCP.Tag())
	}
	if selectedUDP != nil && selectedUDP != selectedTCP {
		references = append(references, selectedUDP.Tag())
	}
	return references
}

func (s *URLTest) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *URLTest) CheckOutbounds() {
	s.group.CheckOutbounds(s.ctx, true)
}

func (s *URLTest) PerformUpdateCheck() {
	s.group.performUpdateCheck()
}

func (s *URLTest) InterfaceUpdated(ctx context.Context) {
	group := s.group
	if group == nil {
		return
	}
	if group.pause.IsDevicePaused() || group.pause.IsNetworkPaused() {
		return
	}
	go func() {
		s.checkAccess.Lock()
		defer s.checkAccess.Unlock()
		if ctx.Err() != nil {
			return
		}
		group.CheckOutbounds(ctx, true)
	}()
}

func (s *URLTest) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	netName := N.NetworkName(network)
	var outbound adapter.Outbound
	switch netName {
	case N.NetworkTCP:
		outbound = s.group.selectedOutboundTCP.Load()
	case N.NetworkUDP:
		outbound = s.group.selectedOutboundUDP.Load()
	default:
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	if outbound == nil || s.group.isTripped(netName, outbound.Tag()) {
		outbound, _ = s.group.Select(netName)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		s.group.recordSuccess(netName, outbound.Tag())
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.recordFailure(ctx, netName, outbound.Tag(), err)
	return nil, err
}

func (s *URLTest) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	outbound := s.group.selectedOutboundUDP.Load()
	if outbound == nil || s.group.isTripped(N.NetworkUDP, outbound.Tag()) {
		outbound, _ = s.group.Select(N.NetworkUDP)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.ListenPacket(ctx, destination)
	if err == nil {
		s.group.recordSuccess(N.NetworkUDP, outbound.Tag())
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.recordFailure(ctx, N.NetworkUDP, outbound.Tag(), err)
	return nil, err
}

func (s *URLTest) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *URLTest) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

type URLTestGroup struct {
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	selectedOutboundTCP          common.TypedValue[adapter.Outbound]
	selectedOutboundUDP          common.TypedValue[adapter.Outbound]
	breakersTCP                  map[string]*outboundBreaker
	breakersUDP                  map[string]*outboundBreaker
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	updateAccess                 sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
}

func NewURLTestGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, tolerance uint16, idleTimeout time.Duration, interruptExternalConnections bool) (*URLTestGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if tolerance == 0 {
		tolerance = 50
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
	}
	breakersTCP := make(map[string]*outboundBreaker, len(outbounds))
	breakersUDP := make(map[string]*outboundBreaker, len(outbounds))
	for _, detour := range outbounds {
		breakersTCP[detour.Tag()] = &outboundBreaker{}
		breakersUDP[detour.Tag()] = &outboundBreaker{}
	}
	return &URLTestGroup{
		ctx:                          ctx,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		breakersTCP:                  breakersTCP,
		breakersUDP:                  breakersUDP,
		link:                         link,
		interval:                     interval,
		tolerance:                    tolerance,
		idleTimeout:                  idleTimeout,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}, nil
}

func (g *URLTestGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(g.ctx, false)
}

func (g *URLTestGroup) Touch() {
	if !g.started {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	ticker := time.NewTicker(g.interval)
	g.ticker = ticker
	g.pauseCallback = pause.RegisterTicker(g.pause, ticker, g.interval, nil)
	go g.loopCheck(ticker, g.close)
}

func (g *URLTestGroup) Close() error {
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker == nil {
		return nil
	}
	g.ticker.Stop()
	g.ticker = nil
	g.pause.UnregisterCallback(g.pauseCallback)
	g.pauseCallback = nil
	close(g.close)
	return nil
}

func (g *URLTestGroup) Select(network string) (adapter.Outbound, bool) {
	now := time.Now().UnixNano()
	var minDelay uint16
	var minOutbound adapter.Outbound
	switch network {
	case N.NetworkTCP:
		if current := g.selectedOutboundTCP.Load(); current != nil && !g.isTrippedAt(network, current.Tag(), now) {
			if history := g.history.LoadURLTestHistory(RealTag(g.outbound, current)); history != nil {
				minOutbound = current
				minDelay = history.Delay
			}
		}
	case N.NetworkUDP:
		if current := g.selectedOutboundUDP.Load(); current != nil && !g.isTrippedAt(network, current.Tag(), now) {
			if history := g.history.LoadURLTestHistory(RealTag(g.outbound, current)); history != nil {
				minOutbound = current
				minDelay = history.Delay
			}
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		if g.isTrippedAt(network, detour.Tag(), now) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		if history == nil {
			continue
		}
		if minDelay == 0 || minDelay > history.Delay+g.tolerance {
			minDelay = history.Delay
			minOutbound = detour
		}
	}
	if minOutbound == nil {
		// fail-open: prefer any untripped supported outbound even without history;
		// only if every candidate is tripped, fall back to the one whose cooldown
		// expires soonest, so we never leave the group with nothing to dial.
		var earliest adapter.Outbound
		var earliestUntil int64
		for _, detour := range g.outbounds {
			if !common.Contains(detour.Network(), network) {
				continue
			}
			if !g.isTrippedAt(network, detour.Tag(), now) {
				return detour, false
			}
			if until := g.breakerUntil(network, detour.Tag()); earliest == nil || until < earliestUntil {
				earliest = detour
				earliestUntil = until
			}
		}
		return earliest, false
	}
	return minOutbound, true
}

func (g *URLTestGroup) breaker(network, tag string) *outboundBreaker {
	if network == N.NetworkUDP {
		return g.breakersUDP[tag]
	}
	return g.breakersTCP[tag]
}

func (g *URLTestGroup) isTripped(network, tag string) bool {
	return g.isTrippedAt(network, tag, time.Now().UnixNano())
}

func (g *URLTestGroup) isTrippedAt(network, tag string, now int64) bool {
	b := g.breaker(network, tag)
	return b != nil && b.tripped(now)
}

func (g *URLTestGroup) breakerUntil(network, tag string) int64 {
	if b := g.breaker(network, tag); b != nil {
		return b.until()
	}
	return 0
}

func (g *URLTestGroup) recordSuccess(network, tag string) {
	if b := g.breaker(network, tag); b != nil {
		b.recordSuccess()
	}
}

func (g *URLTestGroup) recordFailure(ctx context.Context, network, tag string, err error) {
	// Caller cancellation is not an outbound-health signal. An outbound's own
	// timeout still counts when the caller context remains active.
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	b := g.breaker(network, tag)
	if b == nil || !b.recordFailure(time.Now().UnixNano()) {
		return
	}
	g.logger.Debug("outbound ", tag, " tripped breaker on ", network, ", cooling down for ", urlTestBreakerCooldown)
	// Treat a trip as a selection-changing event: drop the tripped outbound from
	// the cache now instead of waiting for the next periodic probe.
	g.reselect(network)
}

// reselect re-runs selection for one network and, if it changes the cached
// outbound, interrupts existing connections so they move to the new pick.
func (g *URLTestGroup) reselect(network string) {
	g.updateAccess.Lock()
	defer g.updateAccess.Unlock()
	outbound, _ := g.Select(network)
	if outbound == nil {
		return
	}
	var changed bool
	switch network {
	case N.NetworkTCP:
		if current := g.selectedOutboundTCP.Load(); current != outbound {
			changed = current != nil
			g.selectedOutboundTCP.Store(outbound)
		}
	case N.NetworkUDP:
		if current := g.selectedOutboundUDP.Load(); current != outbound {
			changed = current != nil
			g.selectedOutboundUDP.Store(outbound)
		}
	}
	if changed {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
}

func (g *URLTestGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(g.ctx, false)
	}
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			if g.ticker == ticker {
				g.ticker.Stop()
				g.ticker = nil
				g.pause.UnregisterCallback(g.pauseCallback)
				g.pauseCallback = nil
			}
			g.access.Unlock()
			return
		}
		g.CheckOutbounds(g.ctx, false)
	}
}

func (g *URLTestGroup) CheckOutbounds(ctx context.Context, force bool) {
	_, _ = g.urlTest(ctx, force)
}

func (g *URLTestGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, true)
}

func (g *URLTestGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	if g.checking.Swap(true) {
		return make(map[string]uint16), nil
	}
	defer g.checking.Store(false)
	result := URLTestOutbounds(ctx, g.outbound, g.history, g.logger, g.outbounds, g.link, g.interval, force)
	g.performUpdateCheck()
	return result, nil
}

type urlTestResult struct {
	delay uint16
	err   error
}

type urlTestBatch struct {
	ctx      context.Context
	outbound adapter.OutboundManager
	history  *urltest.HistoryStorage
	logger   log.Logger
	batch    *batch.Batch[any]
	checked  map[string]bool
	groups   []adapter.OutboundGroup
	access   sync.Mutex
	result   map[string]uint16
}

func URLTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool) map[string]uint16 {
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	testBatch := &urlTestBatch{
		ctx:      ctx,
		outbound: outboundManager,
		history:  history,
		logger:   logger,
		batch:    b,
		checked:  make(map[string]bool),
		result:   make(map[string]uint16),
	}
	testBatch.test(outbounds, link, interval, force)
	b.Wait()
	for _, outboundGroup := range testBatch.groups {
		groupHistory := history.LoadURLTestHistory(RealTag(outboundManager, outboundGroup))
		if groupHistory != nil {
			testBatch.result[outboundGroup.Tag()] = groupHistory.Delay
		}
	}
	return testBatch.result
}

func (b *urlTestBatch) test(outbounds []adapter.Outbound, link string, interval time.Duration, force bool) {
	for _, detour := range outbounds {
		tag := detour.Tag()
		if b.checked[tag] {
			continue
		}
		switch nested := detour.(type) {
		case *URLTest:
			b.checked[tag] = true
			b.groups = append(b.groups, nested)
			b.batch.Go(tag, func() (any, error) {
				nestedResult, _ := nested.group.urlTest(b.ctx, force)
				b.access.Lock()
				maps.Copy(b.result, nestedResult)
				b.access.Unlock()
				return nil, nil
			})
		case adapter.OutboundGroup:
			b.checked[tag] = true
			b.groups = append(b.groups, nested)
			b.test(common.FilterNotNil(common.Map(nested.All(), func(it string) adapter.Outbound {
				member, _ := b.outbound.Outbound(it)
				return member
			})), link, interval, force)
		default:
			history := b.history.LoadURLTestHistory(tag)
			if !force && history != nil && time.Since(history.Time) < interval {
				continue
			}
			b.checked[tag] = true
			b.batch.Go(tag, func() (any, error) {
				testCtx, cancel := context.WithTimeout(b.ctx, C.TCPTimeout)
				defer cancel()
				testChan := make(chan urlTestResult, 1)
				go func() {
					delay, testErr := urltest.URLTest(testCtx, link, detour)
					testChan <- urlTestResult{delay, testErr}
				}()
				var testResult urlTestResult
				select {
				case testResult = <-testChan:
				case <-testCtx.Done():
					testResult.err = testCtx.Err()
				}
				if testResult.err != nil {
					b.logger.Debug("outbound ", tag, " unavailable: ", testResult.err)
					b.history.DeleteURLTestHistory(tag)
				} else {
					b.logger.Debug("outbound ", tag, " available: ", testResult.delay, "ms")
					b.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{
						Time:  time.Now(),
						Delay: testResult.delay,
					})
					b.access.Lock()
					b.result[tag] = testResult.delay
					b.access.Unlock()
				}
				return nil, nil
			})
		}
	}
}

func (g *URLTestGroup) performUpdateCheck() {
	g.updateAccess.Lock()
	defer g.updateAccess.Unlock()
	var (
		updated  bool
		selected bool
	)
	if outbound, exists := g.Select(N.NetworkTCP); outbound != nil {
		current := g.selectedOutboundTCP.Load()
		if current == nil || (exists && outbound != current) {
			if current != nil {
				updated = true
			}
			g.selectedOutboundTCP.Store(outbound)
			selected = true
		}
	}
	if outbound, exists := g.Select(N.NetworkUDP); outbound != nil {
		current := g.selectedOutboundUDP.Load()
		if current == nil || (exists && outbound != current) {
			if current != nil {
				updated = true
			}
			g.selectedOutboundUDP.Store(outbound)
			selected = true
		}
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
	if selected {
		g.history.NotifyUpdated()
	}
}
