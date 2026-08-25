---
icon: material/new-box
---

# Traffic Telemetry

The `traffic-telemetry` service exports an OTLP log record when it receives a tracked connection's close event.
DNS pseudo-connections are omitted.

Delivery is best-effort and buffered in memory. Sustained connection bursts can overflow the bounded buffers, and connections still active when sing-box shuts down are not exported.

The endpoint is contacted directly, without sing-box outbound routing or proxy environment variables. HTTP and HTTPS are supported; `/v1/logs` is appended to the configured endpoint.

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

Full OTLP/HTTP base endpoint. The service appends `/v1/logs` to it.

#### headers

Optional HTTP headers sent with each OTLP request.
