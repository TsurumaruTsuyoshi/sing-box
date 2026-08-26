---
icon: material/new-box
---

# Traffic Telemetry

The `traffic-telemetry` service exports an OTLP log record when it receives a tracked connection's close event.
DNS pseudo-connections are omitted.

Delivery is best-effort and buffered in memory. Sustained connection bursts can overflow the bounded buffers, and connections still active when sing-box shuts down are not exported.

The endpoint is contacted directly, without sing-box outbound routing or proxy environment variables. HTTP and HTTPS are supported.

### Activation

The documented JSON service remains available when this service type is supported; see
[Structure](#structure) below.

Alternatively, this build automatically enables one service when
`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` is non-empty and the JSON configuration has
no `traffic-telemetry` service. The environment-enabled exporter uses the
standard `otlploghttp` environment variables, including
`OTEL_EXPORTER_OTLP_LOGS_HEADERS`, `OTEL_EXPORTER_OTLP_LOGS_TIMEOUT`, and
`OTEL_EXPORTER_OTLP_LOGS_COMPRESSION`.

`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` is a complete signal URL and its path is
used exactly as supplied. Set it to, for example,
`http://10.140.2.231:4318/v1/logs`; `/v1/logs` is not appended in this mode.
The JSON configuration itself contains no custom service type, so an official
or older sing-box binary can still parse and start the same configuration; it
will simply ignore these environment variables.

### Structure

```json
{
  "services": [
    {
      "type": "traffic-telemetry",
      "endpoint": "http://10.140.2.231:4318",
      "headers": {
        "Authorization": "Bearer <token>"
      }
    }
  ]
}
```

### Fields

#### endpoint

==Required==

Full OTLP/HTTP base endpoint. The service appends `/v1/logs` to it. This
field is used only by the explicit JSON configuration path; the environment
path uses the exact URL in `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`.

#### headers

Optional HTTP headers sent with each OTLP request.
