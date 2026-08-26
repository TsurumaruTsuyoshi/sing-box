package box

import (
	"os"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

const otelLogsEndpointEnvironment = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"

func autoEnableTrafficTelemetry(options *option.Options) {
	if os.Getenv(otelLogsEndpointEnvironment) == "" {
		return
	}
	for _, service := range options.Services {
		if service.Type == constant.TypeTrafficTelemetry {
			return
		}
	}
	options.Services = append(options.Services, option.Service{
		Type: constant.TypeTrafficTelemetry,
		Options: &option.TrafficTelemetryServiceOptions{
			UseEnvironment: true,
		},
	})
}
