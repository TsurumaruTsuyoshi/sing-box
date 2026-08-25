package traffictelemetry

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	C "github.com/sagernet/sing-box/constant"
	boxLog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/service"

	"go.opentelemetry.io/otel/attribute"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otelLog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	connectionClosedEventName = "sing-box.connection.closed"
	connectionClosedBody      = "connection closed"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.TrafficTelemetryServiceOptions](registry, C.TypeTrafficTelemetry, NewService)
}

var _ adapter.Service = (*Service)(nil)

type Service struct {
	boxService.Adapter
	trafficManager *trafficcontrol.Manager
	provider       *sdklog.LoggerProvider
	logger         otelLog.Logger

	eventContext     context.Context
	eventCancel      context.CancelFunc
	subscription     observable.Subscription[trafficcontrol.ConnectionEvent]
	subscriptionDone <-chan struct{}
	eventLoopDone    chan struct{}
}

func NewService(ctx context.Context, _ boxLog.ContextLogger, tag string, options option.TrafficTelemetryServiceOptions) (adapter.Service, error) {
	endpoint, err := parseEndpoint(options.Endpoint)
	if err != nil {
		return nil, err
	}
	trafficManager := service.PtrFromContext[trafficcontrol.Manager](ctx)
	if trafficManager == nil {
		return nil, E.New("missing traffic manager")
	}

	exporter, err := otlploghttp.New(
		ctx,
		otlploghttp.WithEndpointURL(endpoint.String()),
		otlploghttp.WithURLPath(logsPath(endpoint)),
		otlploghttp.WithHeaders(options.Headers),
		otlploghttp.WithHTTPClient(newHTTPClient()),
	)
	if err != nil {
		return nil, E.Cause(err, "create OTLP HTTP log exporter")
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(newResource()),
	)
	eventContext, eventCancel := context.WithCancel(ctx)

	return &Service{
		Adapter:        boxService.NewAdapter(C.TypeTrafficTelemetry, tag),
		trafficManager: trafficManager,
		provider:       provider,
		logger:         provider.Logger("sing-box/traffic-telemetry"),
		eventContext:   eventContext,
		eventCancel:    eventCancel,
	}, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateInitialize || s.eventLoopDone != nil {
		return nil
	}
	subscription, done, err := s.trafficManager.SubscribeEvents()
	if err != nil {
		return E.Cause(err, "subscribe traffic events")
	}
	s.subscription = subscription
	s.subscriptionDone = done
	s.eventLoopDone = make(chan struct{})
	go s.eventLoop()
	return nil
}

func (s *Service) eventLoop() {
	defer close(s.eventLoopDone)
	for {
		select {
		case event, ok := <-s.subscription:
			if !ok {
				return
			}
			s.handleEvent(event)
		case <-s.subscriptionDone:
			s.drainSubscription()
			return
		case <-s.eventContext.Done():
			s.drainSubscription()
			return
		}
	}
}

func (s *Service) drainSubscription() {
	for {
		select {
		case event, ok := <-s.subscription:
			if !ok {
				return
			}
			s.handleEvent(event)
		default:
			return
		}
	}
}

func (s *Service) handleEvent(event trafficcontrol.ConnectionEvent) {
	if event.Type != trafficcontrol.ConnectionEventClosed || event.Metadata == nil {
		return
	}
	if event.Metadata.OutboundType == C.TypeDNS {
		return
	}
	s.logger.Emit(s.eventContext, connectionRecord(event, time.Now()))
}

func (s *Service) Close() error {
	if s.subscription != nil {
		s.trafficManager.UnSubscribeEvents(s.subscription)
	}
	if s.eventLoopDone != nil {
		<-s.eventLoopDone
	}
	if s.eventCancel != nil {
		s.eventCancel()
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), C.StopTimeout)
	defer cancel()
	return s.provider.Shutdown(shutdownContext)
}

func parseEndpoint(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, E.New("missing endpoint")
	}
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, E.Cause(err, "invalid endpoint")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, E.New("unsupported endpoint scheme: ", endpoint.Scheme, ", expected http or https")
	}
	if endpoint.Host == "" || endpoint.Hostname() == "" {
		return nil, E.New("missing endpoint host")
	}
	if endpoint.User != nil {
		return nil, E.New("endpoint must not contain user info")
	}
	if endpoint.RawQuery != "" || endpoint.ForceQuery {
		return nil, E.New("endpoint must not contain a query")
	}
	if endpoint.Fragment != "" {
		return nil, E.New("endpoint must not contain a fragment")
	}
	return endpoint, nil
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func logsPath(endpoint *url.URL) string {
	return strings.TrimRight(endpoint.Path, "/") + "/v1/logs"
}

func newResource() *resource.Resource {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", "sing-box"),
		attribute.String("service.version", C.Version),
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		attrs = append(attrs, attribute.String("host.name", hostname))
	}
	return resource.NewSchemaless(attrs...)
}

func connectionRecord(event trafficcontrol.ConnectionEvent, observedAt time.Time) otelLog.Record {
	metadata := event.Metadata
	closedAt := event.ClosedAt
	if closedAt.IsZero() {
		closedAt = metadata.ClosedAt
	}

	record := otelLog.Record{}
	record.SetTimestamp(closedAt)
	record.SetObservedTimestamp(observedAt)
	record.SetEventName(connectionClosedEventName)
	record.SetBody(otelLog.StringValue(connectionClosedBody))

	uploadBytes := int64(0)
	if metadata.Upload != nil {
		uploadBytes = metadata.Upload.Load()
	}
	downloadBytes := int64(0)
	if metadata.Download != nil {
		downloadBytes = metadata.Download.Load()
	}

	attrs := make([]otelLog.KeyValue, 0, 11)
	attrs = append(attrs,
		otelLog.String("connection.id", event.ID.String()),
		otelLog.Int64("source.port", int64(metadata.Metadata.Source.Port)),
		otelLog.String("network.transport", metadata.Metadata.Network),
		otelLog.String("inbound", metadata.Metadata.Inbound),
		otelLog.String("outbound", metadata.Outbound),
		otelLog.String("outbound.type", metadata.OutboundType),
		otelLog.Int64("upload.bytes", uploadBytes),
		otelLog.Int64("download.bytes", downloadBytes),
		otelLog.Int64("duration.ms", closedAt.Sub(metadata.CreatedAt).Milliseconds()),
	)
	if source := metadata.Metadata.Source; source.Addr.IsValid() {
		attrs = append(attrs, otelLog.String("source.ip", source.Addr.String()))
	}
	if metadata.Metadata.User != "" {
		attrs = append(attrs, otelLog.String("user", metadata.Metadata.User))
	}
	record.AddAttributes(attrs...)
	return record
}
