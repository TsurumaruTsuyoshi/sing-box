package box_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/include"
	singService "github.com/sagernet/sing/service"
)

func TestTrafficTelemetryServiceIsInternal(t *testing.T) {
	registry := include.ServiceRegistry()
	_, loaded := registry.CreateOptions("traffic-telemetry")
	require.False(t, loaded)
	require.NotContains(t, registry.OptionTypes(), "traffic-telemetry")
}

func TestTelemetryEnvironmentInstantiatesTrafficManagerWithoutPublicService(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://127.0.0.1:1/v1/logs")
	ctx := include.Context(context.Background())
	instance, err := box.New(box.Options{Context: ctx})
	require.NoError(t, err)

	serviceManager := singService.FromContext[adapter.ServiceManager](ctx)
	require.NotNil(t, serviceManager)
	require.NoError(t, instance.Start())
	t.Cleanup(func() { _ = instance.Close() })

	require.NotNil(t, singService.PtrFromContext[trafficcontrol.Manager](ctx))
	require.Empty(t, serviceManager.Services())
}
