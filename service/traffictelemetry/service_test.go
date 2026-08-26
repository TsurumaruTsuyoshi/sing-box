package traffictelemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	C "github.com/sagernet/sing-box/constant"
	boxLog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/metadata"
	singService "github.com/sagernet/sing/service"

	otelLog "go.opentelemetry.io/otel/log"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

const (
	otelLogsEndpointEnvironment    = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	otelGenericEndpointEnvironment = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

func TestConnectionRecord(t *testing.T) {
	id, err := uuid.FromString("01234567-89ab-cdef-0123-456789abcdef")
	require.NoError(t, err)
	createdAt := time.Unix(100, 0)
	closedAt := createdAt.Add(1234 * time.Millisecond)
	observedAt := closedAt.Add(time.Second)
	upload := new(atomic.Int64)
	download := new(atomic.Int64)
	upload.Store(123)
	download.Store(456)

	event := trafficcontrol.ConnectionEvent{
		Type:     trafficcontrol.ConnectionEventClosed,
		ID:       id,
		ClosedAt: closedAt,
		Metadata: &trafficcontrol.TrackerMetadata{
			ID:        id,
			CreatedAt: createdAt,
			Upload:    upload,
			Download:  download,
			Metadata: adapter.InboundContext{
				Inbound: "mixed-in",
				Network: "tcp",
				Source: metadata.Socksaddr{
					Addr: netip.MustParseAddr("192.0.2.10"),
					Port: 12345,
				},
				Destination: metadata.Socksaddr{Fqdn: "requested.example"},
				Domain:      "sniffed.example",
				User:        "alice",
			},
			Outbound:     "proxy-out",
			OutboundType: "shadowsocks",
		},
	}

	record := connectionRecord(event, observedAt)
	attrs := recordAttributes(record)
	require.Equal(t, connectionClosedEventName, record.EventName())
	require.Equal(t, connectionClosedBody, record.Body().AsString())
	require.Equal(t, closedAt, record.Timestamp())
	require.Equal(t, observedAt, record.ObservedTimestamp())
	require.Equal(t, id.String(), attrs["connection.id"].AsString())
	require.Equal(t, "192.0.2.10", attrs["source.ip"].AsString())
	require.Equal(t, int64(12345), attrs["source.port"].AsInt64())
	require.Equal(t, "tcp", attrs["network.transport"].AsString())
	require.Equal(t, "mixed-in", attrs["inbound"].AsString())
	require.Equal(t, "sniffed.example", attrs["destination.domain"].AsString())
	require.Equal(t, "alice", attrs["user"].AsString())
	require.Equal(t, "proxy-out", attrs["outbound"].AsString())
	require.Equal(t, "shadowsocks", attrs["outbound.type"].AsString())
	require.Equal(t, int64(123), attrs["upload.bytes"].AsInt64())
	require.Equal(t, int64(456), attrs["download.bytes"].AsInt64())
	require.Equal(t, int64(1234), attrs["duration.ms"].AsInt64())
	require.Equal(t, otelLog.KindInt64, attrs["source.port"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["upload.bytes"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["download.bytes"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["duration.ms"].Kind())
	require.NotContains(t, attrs, "destination")
	require.NotContains(t, attrs, "chain")
}

func TestConnectionRecordDestinationFQDNFallback(t *testing.T) {
	closedAt := time.Unix(150, 0)
	event := trafficcontrol.ConnectionEvent{
		ID:       uuid.Must(uuid.NewV4()),
		ClosedAt: closedAt,
		Metadata: &trafficcontrol.TrackerMetadata{
			CreatedAt: closedAt.Add(-time.Second),
			Metadata: adapter.InboundContext{
				Domain:      "192.0.2.1",
				Destination: metadata.Socksaddr{Fqdn: "requested.example"},
			},
		},
	}

	attrs := recordAttributes(connectionRecord(event, closedAt))
	require.Equal(t, "requested.example", attrs["destination.domain"].AsString())
}

func TestConnectionRecordDoesNotExportDestinationIPAsDomain(t *testing.T) {
	closedAt := time.Unix(175, 0)
	event := trafficcontrol.ConnectionEvent{
		ID:       uuid.Must(uuid.NewV4()),
		ClosedAt: closedAt,
		Metadata: &trafficcontrol.TrackerMetadata{
			CreatedAt: closedAt.Add(-time.Second),
			Metadata: adapter.InboundContext{
				Domain:      "2001:db8::1",
				Destination: metadata.Socksaddr{Fqdn: "192.0.2.1"},
			},
		},
	}

	attrs := recordAttributes(connectionRecord(event, closedAt))
	require.NotContains(t, attrs, "destination.domain")
}

func TestConnectionRecordOptionalFields(t *testing.T) {
	id, err := uuid.FromString("01234567-89ab-cdef-0123-456789abcdef")
	require.NoError(t, err)
	closedAt := time.Unix(200, 0)
	event := trafficcontrol.ConnectionEvent{
		ID:       id,
		ClosedAt: closedAt,
		Metadata: &trafficcontrol.TrackerMetadata{
			CreatedAt: closedAt.Add(-time.Second),
			Metadata: adapter.InboundContext{
				Source: metadata.Socksaddr{Fqdn: "client.example"},
			},
		},
	}

	record := connectionRecord(event, closedAt)
	attrs := recordAttributes(record)
	require.NotContains(t, attrs, "user")
	require.NotContains(t, attrs, "source.ip")
	require.NotContains(t, attrs, "destination.domain")
	require.Equal(t, otelLog.KindInt64, attrs["source.port"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["upload.bytes"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["download.bytes"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["duration.ms"].Kind())
}

func TestUnstartedServiceCloseDoesNotCreateExporter(t *testing.T) {
	trafficManager := trafficcontrol.NewManager(nil)
	ctx := singService.ContextWithPtr(context.Background(), trafficManager)
	telemetryService, err := NewService(ctx, boxLog.NewNOPFactory().Logger())
	require.NoError(t, err)
	require.Nil(t, telemetryService.provider)
	require.Nil(t, telemetryService.logger)

	require.NoError(t, telemetryService.Close())
	require.NoError(t, telemetryService.Close())
	require.Nil(t, telemetryService.provider)
}

func TestServiceStartFailureShutsDownProvider(t *testing.T) {
	t.Setenv(otelLogsEndpointEnvironment, "http://127.0.0.1:1/v1/logs")
	trafficManager := trafficcontrol.NewManager(nil)
	require.NoError(t, trafficManager.Start(adapter.StartStateInitialize))
	require.NoError(t, trafficManager.Close())
	defer trafficManager.Close()

	ctx := singService.ContextWithPtr(context.Background(), trafficManager)
	telemetryService, err := NewService(ctx, boxLog.NewNOPFactory().Logger())
	require.NoError(t, err)
	require.Error(t, telemetryService.Start(adapter.StartStateInitialize))
	require.Nil(t, telemetryService.provider)
	require.Nil(t, telemetryService.logger)
	require.NoError(t, telemetryService.Close())
}

func TestServiceCloseDrainsEventsAndFlushesProvider(t *testing.T) {
	requestBody := make(chan []byte, 1)
	requestPath := make(chan string, 1)
	requestHeader := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requestBody <- body
		requestPath <- request.URL.Path
		requestHeader <- request.Header.Get("X-Test")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", server.URL+"/environment/v1/logs")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "X-Test=environment")

	// A non-nil HTTP proxy must not affect this service's direct host transport.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}

	trafficManager := trafficcontrol.NewManager(nil)
	require.NoError(t, trafficManager.Start(adapter.StartStateInitialize))
	defer trafficManager.Close()
	ctx := singService.ContextWithPtr(context.Background(), trafficManager)
	service, err := NewService(ctx, boxLog.NewNOPFactory().Logger())
	require.NoError(t, err)
	require.Nil(t, service.provider)
	require.NoError(t, service.Start(adapter.StartStateInitialize))
	trafficManager.UnSubscribeEvents(service.subscription)
	<-service.eventLoopDone

	id, err := uuid.FromString("01234567-89ab-cdef-0123-456789abcdef")
	require.NoError(t, err)
	closedAt := time.Unix(100, 0)
	validEvent := testConnectionEvent(id, closedAt, "shadowsocks")
	dnsEvent := testConnectionEvent(uuid.Must(uuid.NewV4()), closedAt, C.TypeDNS)
	newEvent := trafficcontrol.ConnectionEvent{Type: trafficcontrol.ConnectionEventNew}

	events := make(chan trafficcontrol.ConnectionEvent, 3)
	events <- newEvent
	events <- dnsEvent
	events <- validEvent
	subscriptionDone := make(chan struct{})
	service.subscription = events
	service.subscriptionDone = subscriptionDone
	service.eventLoopDone = make(chan struct{})
	go service.eventLoop()
	close(subscriptionDone)

	// Close unsubscribes first; the event loop must drain the three buffered
	// events before the provider is shut down.
	require.NoError(t, service.Close())
	require.Equal(t, "/environment/v1/logs", receiveRequestValue(t, requestPath))
	require.Equal(t, "environment", receiveRequestValue(t, requestHeader))

	var exportRequest collogpb.ExportLogsServiceRequest
	require.NoError(t, proto.Unmarshal(receiveRequestBody(t, requestBody), &exportRequest))
	require.Len(t, exportRequest.ResourceLogs, 1)
	resourceLogs := exportRequest.ResourceLogs[0]
	resourceAttrs := protoAttributes(resourceLogs.Resource.Attributes)
	require.Equal(t, "sing-box", resourceAttrs["service.name"].GetStringValue())
	require.Equal(t, C.Version, resourceAttrs["service.version"].GetStringValue())
	require.Len(t, resourceLogs.ScopeLogs, 1)
	require.Len(t, resourceLogs.ScopeLogs[0].LogRecords, 1) // DNS and new events were filtered.

	record := resourceLogs.ScopeLogs[0].LogRecords[0]
	require.Equal(t, connectionClosedEventName, record.EventName)
	require.Equal(t, connectionClosedBody, record.Body.GetStringValue())
	require.Equal(t, uint64(closedAt.UnixNano()), record.TimeUnixNano)
	require.NotZero(t, record.ObservedTimeUnixNano)
	attrs := protoAttributes(record.Attributes)
	require.Equal(t, id.String(), attrs["connection.id"].GetStringValue())
	require.Equal(t, "192.0.2.10", attrs["source.ip"].GetStringValue())
	require.Equal(t, int64(12345), attrs["source.port"].GetIntValue())
	require.Equal(t, "tcp", attrs["network.transport"].GetStringValue())
	require.Equal(t, "mixed-in", attrs["inbound"].GetStringValue())
	require.Equal(t, "telemetry.example", attrs["destination.domain"].GetStringValue())
	require.Equal(t, "alice", attrs["user"].GetStringValue())
	require.Equal(t, "proxy-out", attrs["outbound"].GetStringValue())
	require.Equal(t, "shadowsocks", attrs["outbound.type"].GetStringValue())
	require.Equal(t, int64(123), attrs["upload.bytes"].GetIntValue())
	require.Equal(t, int64(456), attrs["download.bytes"].GetIntValue())
	require.Equal(t, int64(1234), attrs["duration.ms"].GetIntValue())
	require.NotContains(t, attrs, "destination")
	require.NotContains(t, attrs, "chain")
}

func TestServiceUsesStandardExporterConfiguration(t *testing.T) {
	t.Run("signal endpoint and settings take precedence", func(t *testing.T) {
		requestPath := make(chan string, 1)
		requestHeader := make(chan string, 1)
		requestEncoding := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.Copy(io.Discard, request.Body)
			requestPath <- request.URL.Path
			requestHeader <- request.Header.Get("X-Environment")
			requestEncoding <- request.Header.Get("Content-Encoding")
			writer.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		t.Setenv(otelLogsEndpointEnvironment, server.URL+"/signal/logs")
		t.Setenv(otelGenericEndpointEnvironment, server.URL+"/generic")
		t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "X-Environment=signal")
		t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "X-Environment=generic")
		t.Setenv("OTEL_EXPORTER_OTLP_LOGS_TIMEOUT", "1000")
		t.Setenv("OTEL_EXPORTER_OTLP_LOGS_COMPRESSION", "gzip")
		t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
		setInvalidHTTPProxyEnvironment(t)

		telemetryService := newStartedService(t)
		telemetryService.handleEvent(testConnectionEvent(uuid.Must(uuid.NewV4()), time.Unix(100, 0), "shadowsocks"))
		require.NoError(t, telemetryService.Close())

		require.Equal(t, "/signal/logs", receiveRequestValue(t, requestPath))
		require.Equal(t, "signal", receiveRequestValue(t, requestHeader))
		require.Equal(t, "gzip", receiveRequestValue(t, requestEncoding))
	})

	t.Run("generic endpoint gets the logs path", func(t *testing.T) {
		requestPath := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.Copy(io.Discard, request.Body)
			requestPath <- request.URL.Path
			writer.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		t.Setenv(otelLogsEndpointEnvironment, "")
		t.Setenv(otelGenericEndpointEnvironment, server.URL+"/generic")

		telemetryService := newStartedService(t)
		telemetryService.handleEvent(testConnectionEvent(uuid.Must(uuid.NewV4()), time.Unix(100, 0), "shadowsocks"))
		require.NoError(t, telemetryService.Close())

		require.Equal(t, "/generic/v1/logs", receiveRequestValue(t, requestPath))
	})
}

func TestServiceCloseIgnoresExporterErrors(t *testing.T) {
	t.Setenv(otelLogsEndpointEnvironment, "http://127.0.0.1:1/v1/logs")
	t.Setenv(otelGenericEndpointEnvironment, "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_TIMEOUT", "1")

	telemetryService := newStartedService(t)
	telemetryService.handleEvent(testConnectionEvent(uuid.Must(uuid.NewV4()), time.Unix(100, 0), "shadowsocks"))
	require.NoError(t, telemetryService.Close())
}

func newStartedService(t *testing.T) *Service {
	t.Helper()
	trafficManager := trafficcontrol.NewManager(nil)
	require.NoError(t, trafficManager.Start(adapter.StartStateInitialize))
	t.Cleanup(func() { require.NoError(t, trafficManager.Close()) })
	ctx := singService.ContextWithPtr(context.Background(), trafficManager)
	telemetryService, err := NewService(ctx, boxLog.NewNOPFactory().Logger())
	require.NoError(t, err)
	require.NoError(t, telemetryService.Start(adapter.StartStateInitialize))
	return telemetryService
}

func setInvalidHTTPProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}
}

func testConnectionEvent(id uuid.UUID, closedAt time.Time, outboundType string) trafficcontrol.ConnectionEvent {
	upload := new(atomic.Int64)
	download := new(atomic.Int64)
	upload.Store(123)
	download.Store(456)
	return trafficcontrol.ConnectionEvent{
		Type:     trafficcontrol.ConnectionEventClosed,
		ID:       id,
		ClosedAt: closedAt,
		Metadata: &trafficcontrol.TrackerMetadata{
			ID:        id,
			CreatedAt: closedAt.Add(-1234 * time.Millisecond),
			Upload:    upload,
			Download:  download,
			Metadata: adapter.InboundContext{
				Inbound: "mixed-in",
				Network: "tcp",
				Source: metadata.Socksaddr{
					Addr: netip.MustParseAddr("192.0.2.10"),
					Port: 12345,
				},
				Domain: "telemetry.example",
				User:   "alice",
			},
			Outbound:     "proxy-out",
			OutboundType: outboundType,
		},
	}
}

func receiveRequestValue(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OTLP request")
		return ""
	}
}

func receiveRequestBody(t *testing.T, values <-chan []byte) []byte {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OTLP request")
		return nil
	}
}

func recordAttributes(record otelLog.Record) map[string]otelLog.Value {
	attrs := make(map[string]otelLog.Value)
	record.WalkAttributes(func(kv otelLog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	return attrs
}

func protoAttributes(values []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	attrs := make(map[string]*commonpb.AnyValue, len(values))
	for _, value := range values {
		attrs[value.Key] = value.Value
	}
	return attrs
}
