---
icon: material/new-box
---

# Traffic Telemetry

sing-box can export one OTLP log record when a tracked connection closes. DNS
pseudo-connections are omitted. This is an environment-only operational
feature; it is not a JSON service configuration.

### Activation

The feature is enabled when either of these variables contains a non-whitespace
value:

- `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`
- `OTEL_EXPORTER_OTLP_ENDPOINT`

Whitespace-only values do not enable it. The service is synthesized at startup,
so do not add a `traffic-telemetry` object to the JSON configuration.

### Endpoint and exporter settings

The standard `otlploghttp` exporter reads the endpoint and all other OTel
settings directly from the environment. No sing-box endpoint, header, timeout,
compression, or TLS fields are available.

`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` is the signal-specific endpoint. It takes
precedence over `OTEL_EXPORTER_OTLP_ENDPOINT`, and its URL path is used exactly
as supplied:

```text
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://collector:4318/custom/logs
```

`OTEL_EXPORTER_OTLP_ENDPOINT` is the generic OTLP base endpoint. The exporter
appends `/v1/logs` to its path:

```text
OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318/otlp
# sends logs to /otlp/v1/logs
```

Signal-specific OTel settings likewise take precedence over their generic
counterparts. For example, use `OTEL_EXPORTER_OTLP_LOGS_HEADERS` or
`OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_LOGS_TIMEOUT` or
`OTEL_EXPORTER_OTLP_TIMEOUT`, and the corresponding compression and TLS
variables. The exporter honors these standard settings, including gzip,
custom headers, timeouts, certificates, and client certificates.

The collector is contacted directly. sing-box outbound routing and HTTP proxy
environment variables are not used for this export.

### Delivery and shutdown

Delivery is best-effort. Records are buffered in bounded in-memory queues;
records can be dropped when those queues are full, and connections still active
when sing-box stops are not exported. Collector outages do not block startup or
normal traffic handling.

On close or reload, sing-box first drains the traffic-event subscription and
then attempts a bounded flush using the normal stop timeout. Flush and exporter
shutdown errors are logged and ignored, so an unavailable collector does not
turn shutdown or reload into a failure. Records that cannot be flushed before
the deadline can be lost.
