package box

import (
	"os"
	"strings"
)

const (
	otelLogsEndpointEnvironment    = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	otelGenericEndpointEnvironment = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

func isTrafficTelemetryEnabled() bool {
	return strings.TrimSpace(os.Getenv(otelLogsEndpointEnvironment)) != "" ||
		strings.TrimSpace(os.Getenv(otelGenericEndpointEnvironment)) != ""
}
