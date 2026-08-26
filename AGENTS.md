# AGENTS.md — sing-box (local patched)

Based on upstream `SagerNet/sing-box` branch `testing`, currently `v1.14.0-beta.14`.

## Local patch

### Circuit breaker for URLTest outbound groups

Real TCP/UDP dial failures feed a per-outbound, per-network circuit breaker. Three consecutive failures exclude that outbound for 30 seconds and trigger immediate reselection. A successful real dial clears both the failure count and cooldown. Caller cancellation does not count as an outbound failure.

The selected TCP/UDP outbounds use `common.TypedValue` because real dials, URL tests, and interface-update checks can access selection concurrently.

#### What it does not catch

The breaker is fed at `protocol/group/urltest.go:198`, where `recordSuccess` fires the moment `DialContext` returns without error. Everything after that point is invisible to it: upload stalls, transfer-time i/o timeouts, sessions the peer closed while we still hold them.

Observed on 2026-08-19 with a nexitally AnyTLS endpoint. TLS handshakes kept succeeding, then the upload direction died — `connection upload closed: write tcp ...: i/o timeout`, single connections hanging 55s to 4m26s before being torn down. Twenty-two such failures on one outbound, zero breaker trips, because every one of them happened after a successful dial.

The three-consecutive-failures threshold is a second gap. A sibling endpoint on the same subscription did fail at dial time (`failed to create stream: use of closed network connection`), but those ten failures were spread across several hundred successful dials, and each success resets the counter. Intermittent rot never accumulates.

Trips are logged at `Debug` (`urltest.go:442`). Production runs at `INFO`, so a trip that does happen leaves no trace. Raise the log level before trying to verify breaker behaviour.

The tempting fix — wrap the returned `conn` and score on observed data flow — does not work, and it is worth writing down why so nobody spends another afternoon on it. Everything past the dial is opaque: a tunnel that goes quiet for forty seconds is indistinguishable from a model that is simply thinking, and a long reasoning request whose client gives up first looks exactly like a dead route. Passive traffic shape cannot separate a broken path from a slow answer.

What can separate them is a controlled probe. URLTest already probes actively, so the gap is in what it sends, not in whether it measures: `http://clients3.google.com/generate_204` is a tiny request with an empty response, and it stays green on exactly the endpoints that fail. Measured on 2026-08-19 against the same subscription, small requests through the bad endpoint passed 100%, while a 1 MiB upload hung past 35 seconds or died with EOF; a healthy sibling endpoint finished the same upload in 0.60-0.75s. A probe carrying a known payload in the direction that actually breaks would have caught this; the current one never could.

Disabling AnyTLS session reuse was also tried during that investigation. It removed the stale `CreateStream` errors but uploads on brand-new sessions still hung, so session reuse is a symptom rather than the cause.

When updating upstream, check changes to `protocol/group/urltest.go` carefully. Keep upstream URL-test and interface-update behavior; do not restore removed adapter methods merely because an old patch contains them. Drop this patch if upstream adds equivalent real-dial health tracking.

## AnyTLS client metadata

Upstream now provides `client_metadata` on AnyTLS outbounds and rewrites the `client=` settings field itself. Use that option for servers that require a specific client identifier. The old local `client_id` option and `changchinlan/sing-anytls` fork are obsolete and must not be restored.

## Traffic telemetry

Traffic telemetry exists only as an internally synthesized service. A non-whitespace `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT` activates it; the standard JSON configuration must not contain a telemetry service type. The synthetic service is added in `box.New` before the traffic manager requirement is calculated, and its exporter/provider are created at initialize time rather than during service construction; this keeps `sing-box check` from leaking a batch-processor goroutine. Keep the standard config free of the private service type.

The environment exporter must let `otlploghttp` consume its standard signal-specific settings, including signal endpoint precedence and the exact `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` path. Do not replace this with a custom endpoint parser or client that suppresses the standard timeout/TLS settings. The generic endpoint gets `/v1/logs` appended by `otlploghttp`; signal-specific endpoint paths are used as supplied. Telemetry is best-effort: close/reload drains the event queue and attempts a bounded flush, but exporter errors are logged and ignored.

## Build

```bash
nx "go gcc" -- go build -mod=mod -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -o sing-box ./cmd/sing-box
```
