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
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/metadata"
	singService "github.com/sagernet/sing/service"

	otelLog "go.opentelemetry.io/otel/log"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
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
				User: "alice",
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
	require.Equal(t, otelLog.KindInt64, attrs["source.port"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["upload.bytes"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["download.bytes"].Kind())
	require.Equal(t, otelLog.KindInt64, attrs["duration.ms"].Kind())
}

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		path string
	}{
		{name: "http", raw: "http://127.0.0.1:4318", path: "/v1/logs"},
		{name: "https", raw: "https://collector:4318", path: "/v1/logs"},
		{name: "base path", raw: "http://collector:4318/otlp/", path: "/otlp/v1/logs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, err := parseEndpoint(test.raw)
			require.NoError(t, err)
			require.Equal(t, test.path, logsPath(endpoint))
		})
	}

	for _, raw := range []string{
		"",
		"ftp://127.0.0.1:4318",
		"http://",
		"http://user@127.0.0.1:4318",
		"http://127.0.0.1:4318?token=secret",
		"http://127.0.0.1:4318#logs",
	} {
		_, err := parseEndpoint(raw)
		require.Error(t, err, raw)
	}
}

func TestUnstartedServiceCloseDoesNotCreateExporter(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestSeen <- struct{}{}
	}))
	defer server.Close()

	trafficManager := trafficcontrol.NewManager(nil)
	ctx := singService.ContextWithPtr(context.Background(), trafficManager)
	serviceValue, err := NewService(ctx, boxLog.NewNOPFactory().Logger(), "telemetry", option.TrafficTelemetryServiceOptions{
		Endpoint: server.URL,
	})
	require.NoError(t, err)
	telemetryService := serviceValue.(*Service)
	require.Nil(t, telemetryService.provider)
	require.Nil(t, telemetryService.logger)

	require.NoError(t, telemetryService.Close())
	require.NoError(t, telemetryService.Close())
	require.Nil(t, telemetryService.provider)
	select {
	case <-requestSeen:
		t.Fatal("unstarted service made an export request")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestServiceStartFailureShutsDownProvider(t *testing.T) {
	trafficManager := trafficcontrol.NewManager(nil)
	require.NoError(t, trafficManager.Start(adapter.StartStateInitialize))
	require.NoError(t, trafficManager.Close())
	defer trafficManager.Close()

	ctx := singService.ContextWithPtr(context.Background(), trafficManager)
	serviceValue, err := NewService(ctx, boxLog.NewNOPFactory().Logger(), "telemetry", option.TrafficTelemetryServiceOptions{
		Endpoint: "http://127.0.0.1:1",
	})
	require.NoError(t, err)
	telemetryService := serviceValue.(*Service)
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
	serviceValue, err := NewService(ctx, boxLog.NewNOPFactory().Logger(), "telemetry", option.TrafficTelemetryServiceOptions{
		Endpoint: server.URL + "/collector/",
		Headers:  map[string]string{"X-Test": "present"},
	})
	require.NoError(t, err)
	service := serviceValue.(*Service)
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
	require.Equal(t, "/collector/v1/logs", receiveRequestValue(t, requestPath))
	require.Equal(t, "present", receiveRequestValue(t, requestHeader))

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
	require.Equal(t, "alice", attrs["user"].GetStringValue())
	require.Equal(t, "proxy-out", attrs["outbound"].GetStringValue())
	require.Equal(t, "shadowsocks", attrs["outbound.type"].GetStringValue())
	require.Equal(t, int64(123), attrs["upload.bytes"].GetIntValue())
	require.Equal(t, int64(456), attrs["download.bytes"].GetIntValue())
	require.Equal(t, int64(1234), attrs["duration.ms"].GetIntValue())
	require.NotContains(t, attrs, "destination")
	require.NotContains(t, attrs, "chain")
}

func TestEnvironmentServiceUsesStandardExporterConfiguration(t *testing.T) {
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

	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", server.URL+"/collector/v1/logs")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "X-Environment=present")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_TIMEOUT", "1000")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_COMPRESSION", "gzip")
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
	serviceValue, err := NewService(ctx, boxLog.NewNOPFactory().Logger(), "telemetry", option.TrafficTelemetryServiceOptions{
		UseEnvironment: true,
	})
	require.NoError(t, err)
	telemetryService := serviceValue.(*Service)
	require.Nil(t, telemetryService.provider)
	require.NoError(t, telemetryService.Start(adapter.StartStateInitialize))
	telemetryService.handleEvent(testConnectionEvent(uuid.Must(uuid.NewV4()), time.Unix(100, 0), "shadowsocks"))

	require.NoError(t, telemetryService.Close())
	require.Equal(t, "/collector/v1/logs", receiveRequestValue(t, requestPath))
	require.Equal(t, "present", receiveRequestValue(t, requestHeader))
	require.Equal(t, "gzip", receiveRequestValue(t, requestEncoding))
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
				User: "alice",
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
