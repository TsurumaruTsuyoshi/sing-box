package box_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	singService "github.com/sagernet/sing/service"
)

func TestAutoEnabledTrafficTelemetryInstantiatesManagers(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://127.0.0.1:1/v1/logs")
	ctx := include.Context(context.Background())
	instance, err := box.New(box.Options{Context: ctx})
	require.NoError(t, err)

	serviceManager := singService.FromContext[adapter.ServiceManager](ctx)
	require.NotNil(t, serviceManager)
	t.Cleanup(func() {
		for _, service := range serviceManager.Services() {
			_ = service.Close()
		}
		_ = instance.Close()
	})

	require.NotNil(t, singService.PtrFromContext[trafficcontrol.Manager](ctx))
	services := serviceManager.Services()
	require.Len(t, services, 1)
	require.Equal(t, constant.TypeTrafficTelemetry, services[0].Type())
}
