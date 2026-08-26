package traffictelemetry

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	C "github.com/sagernet/sing-box/constant"
	boxLog "github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/service"

	"go.opentelemetry.io/otel"
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

var _ adapter.LifecycleService = (*Service)(nil)

type Service struct {
	trafficManager *trafficcontrol.Manager
	serviceLogger  boxLog.ContextLogger
	provider       *sdklog.LoggerProvider
	logger         otelLog.Logger

	eventContext     context.Context
	eventCancel      context.CancelFunc
	subscription     observable.Subscription[trafficcontrol.ConnectionEvent]
	subscriptionDone <-chan struct{}
	eventLoopDone    chan struct{}
}

func (*Service) Name() string {
	return "traffic telemetry"
}

func NewService(ctx context.Context, logger boxLog.ContextLogger) (*Service, error) {
	trafficManager := service.PtrFromContext[trafficcontrol.Manager](ctx)
	if trafficManager == nil {
		return nil, E.New("missing traffic manager")
	}
	eventContext, eventCancel := context.WithCancel(ctx)

	return &Service{
		trafficManager: trafficManager,
		serviceLogger:  logger,
		eventContext:   eventContext,
		eventCancel:    eventCancel,
	}, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateInitialize || s.eventLoopDone != nil {
		return nil
	}
	exporter, err := otlploghttp.New(s.eventContext, otlploghttp.WithProxy(directHTTPProxy))
	if err != nil {
		s.reportError("create OTLP HTTP log exporter", err)
		return nil
	}
	s.provider = sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(newResource()),
	)
	s.logger = s.provider.Logger("sing-box/traffic-telemetry")

	subscription, done, err := s.trafficManager.SubscribeEvents()
	if err != nil {
		startErr := E.Cause(err, "subscribe traffic events")
		s.shutdownProvider()
		return startErr
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
	if s.subscription != nil && s.trafficManager != nil {
		s.trafficManager.UnSubscribeEvents(s.subscription)
	}
	if s.eventLoopDone != nil {
		<-s.eventLoopDone
	}
	if s.eventCancel != nil {
		s.eventCancel()
	}
	s.shutdownProvider()
	return nil
}

func (s *Service) shutdownProvider() {
	provider := s.provider
	if provider == nil {
		return
	}
	s.provider = nil
	s.logger = nil

	shutdownContext, cancel := context.WithTimeout(context.Background(), C.StopTimeout)
	defer cancel()
	if err := provider.Shutdown(shutdownContext); err != nil {
		s.reportError("shutdown OTLP log provider", err)
	}
}

func (s *Service) reportError(message string, err error) {
	if s.serviceLogger != nil {
		s.serviceLogger.Error(message, ": ", err)
		return
	}
	otel.Handle(E.Cause(err, message))
}

func directHTTPProxy(*http.Request) (*url.URL, error) {
	return nil, nil
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
