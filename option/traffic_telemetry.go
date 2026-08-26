package option

type TrafficTelemetryServiceOptions struct {
	Endpoint       string            `json:"endpoint"`
	Headers        map[string]string `json:"headers,omitempty"`
	UseEnvironment bool              `json:"-"`
}
