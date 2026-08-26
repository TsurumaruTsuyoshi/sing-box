package box

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestAutoEnableTrafficTelemetry(t *testing.T) {
	t.Run("enabled without explicit service", func(t *testing.T) {
		t.Setenv(otelLogsEndpointEnvironment, "http://collector:4318/v1/logs")
		options := option.Options{}

		autoEnableTrafficTelemetry(&options)

		require.Len(t, options.Services, 1)
		require.Equal(t, constant.TypeTrafficTelemetry, options.Services[0].Type)
		telemetryOptions, ok := options.Services[0].Options.(*option.TrafficTelemetryServiceOptions)
		require.True(t, ok)
		require.True(t, telemetryOptions.UseEnvironment)
	})

	t.Run("explicit service takes precedence", func(t *testing.T) {
		t.Setenv(otelLogsEndpointEnvironment, "http://collector:4318/v1/logs")
		options := option.Options{
			Services: []option.Service{{Type: constant.TypeTrafficTelemetry}},
		}

		autoEnableTrafficTelemetry(&options)

		require.Len(t, options.Services, 1)
		require.Equal(t, constant.TypeTrafficTelemetry, options.Services[0].Type)
	})

	t.Run("empty endpoint does not activate", func(t *testing.T) {
		t.Setenv(otelLogsEndpointEnvironment, "")
		options := option.Options{}

		autoEnableTrafficTelemetry(&options)

		require.Empty(t, options.Services)
	})
}
