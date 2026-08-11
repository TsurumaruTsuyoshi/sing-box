# AGENTS.md — sing-box (local patched)

Based on upstream `SagerNet/sing-box` branch `testing`, currently `v1.14.0-beta.14`.

## Local patch

### Circuit breaker for URLTest outbound groups

Real TCP/UDP dial failures feed a per-outbound, per-network circuit breaker. Three consecutive failures exclude that outbound for 30 seconds and trigger immediate reselection. A successful real dial clears both the failure count and cooldown. Caller cancellation does not count as an outbound failure.

The selected TCP/UDP outbounds use `common.TypedValue` because real dials, URL tests, and interface-update checks can access selection concurrently.

When updating upstream, check changes to `protocol/group/urltest.go` carefully. Keep upstream URL-test and interface-update behavior; do not restore removed adapter methods merely because an old patch contains them. Drop this patch if upstream adds equivalent real-dial health tracking.

## AnyTLS client metadata

Upstream now provides `client_metadata` on AnyTLS outbounds and rewrites the `client=` settings field itself. Use that option for servers that require a specific client identifier. The old local `client_id` option and `changchinlan/sing-anytls` fork are obsolete and must not be restored.

## Build

```bash
nx "go gcc" -- go build -mod=mod -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -o sing-box ./cmd/sing-box
```
