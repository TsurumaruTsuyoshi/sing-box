---
icon: material/new-box
---

# 流量遥测

`traffic-telemetry` 服务收到所跟踪连接的关闭事件后，会导出一条 OTLP 日志记录。
DNS 伪连接不会被导出。

发送采用尽力而为方式，数据只缓存在内存中。持续的连接突发可能塞满有限的 buffer；sing-box 关闭时仍然活跃的连接不会被导出。

端点通过主机直接连接，不使用 sing-box 出站路由或代理环境变量。支持 HTTP 和 HTTPS。

### 启用方式

在支持该服务类型时，仍可使用文档中的 JSON 服务配置，见下方的[结构](#结构)。

此外，当 `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` 非空且 JSON 配置中没有
`traffic-telemetry` 服务时，本版本会自动启用一个服务。通过环境变量启用时，
exporter 会自行读取 `otlploghttp` 的标准环境变量，包括
`OTEL_EXPORTER_OTLP_LOGS_HEADERS`、`OTEL_EXPORTER_OTLP_LOGS_TIMEOUT` 和
`OTEL_EXPORTER_OTLP_LOGS_COMPRESSION`。

`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` 是完整的信号 URL，路径会按原样使用。例如应设置为
`http://10.140.2.231:4318/v1/logs`；此模式不会再追加 `/v1/logs`。
JSON 配置本身不包含自定义服务类型，因此官方或较旧的 sing-box 二进制仍可解析并启动同一份配置；
它们只会忽略这些环境变量。

### 结构

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

### 字段

#### endpoint

==必填==

完整的 OTLP/HTTP 基础端点。服务会在其后追加 `/v1/logs`。此字段只用于显式 JSON 配置；
环境变量模式会按原样使用 `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` 中的完整 URL。

#### headers

可选的 HTTP 请求头，会随每个 OTLP 请求发送。
