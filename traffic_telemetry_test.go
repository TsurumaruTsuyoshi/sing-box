package box

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrafficTelemetryEnabled(t *testing.T) {
	tests := []struct {
		name    string
		logs    string
		generic string
		enabled bool
	}{
		{name: "logs endpoint", logs: "http://collector:4318/v1/logs", enabled: true},
		{name: "generic endpoint", generic: "http://collector:4318", enabled: true},
		{name: "no environment", enabled: false},
		{name: "logs endpoint whitespace", logs: " \t\n", enabled: false},
		{name: "generic endpoint whitespace", generic: " \t\n", enabled: false},
		{name: "both endpoints whitespace", logs: " \t", generic: "\n", enabled: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(otelLogsEndpointEnvironment, test.logs)
			t.Setenv(otelGenericEndpointEnvironment, test.generic)
			require.Equal(t, test.enabled, isTrafficTelemetryEnabled())
		})
	}
}
