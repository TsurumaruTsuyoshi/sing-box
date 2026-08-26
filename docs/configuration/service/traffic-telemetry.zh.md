---
icon: material/new-box
---

# 流量遥测

sing-box 可以在所跟踪的连接关闭时导出一条 OTLP 日志记录；DNS 伪连接不会导出。
这是一个只通过环境变量启用的运维功能，不是 JSON 服务配置。

### 启用

以下任一变量包含非空白内容时启用：

- `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`
- `OTEL_EXPORTER_OTLP_ENDPOINT`

只有空格等空白内容不会启用功能。服务会在启动时内部合成，因此不要在 JSON 配置中添加
`traffic-telemetry` 对象。

### 端点与 exporter 设置

标准 `otlploghttp` exporter 会直接从环境变量读取端点以及其他 OTel 设置。sing-box 不提供
自定义的端点、请求头、超时、压缩或 TLS 字段。

`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` 是日志信号专用端点，优先于
`OTEL_EXPORTER_OTLP_ENDPOINT`，并且完整 URL 的路径会按原样使用：

```text
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://collector:4318/custom/logs
```

`OTEL_EXPORTER_OTLP_ENDPOINT` 是通用 OTLP 基础端点，exporter 会在其路径后追加 `/v1/logs`：

```text
OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318/otlp
# 日志发送到 /otlp/v1/logs
```

其他 OTel 设置也遵循信号专用变量优先于通用变量的规则。例如请求头使用
`OTEL_EXPORTER_OTLP_LOGS_HEADERS` 或 `OTEL_EXPORTER_OTLP_HEADERS`，超时使用
`OTEL_EXPORTER_OTLP_LOGS_TIMEOUT` 或 `OTEL_EXPORTER_OTLP_TIMEOUT`，压缩和 TLS 也相同。
exporter 会保留这些标准设置，包括 gzip、自定义请求头、超时、证书和客户端证书。

collector 通过主机直接连接；这次导出不会使用 sing-box 出站路由或 HTTP 代理环境变量。

### 发送与关闭

发送采用尽力而为方式。记录缓存在有限的内存队列中；队列满时可能丢弃记录，sing-box 停止时
仍然活跃的连接也不会导出。collector 不可用不会阻塞启动或正常流量处理。

关闭或 reload 时，sing-box 会先排空流量事件订阅，再使用正常的停止超时进行一次有界的
flush 尝试。flush 或 exporter 关闭错误会记录日志并忽略，因此 collector 不可用不会把关闭
或 reload 变成失败。截止时间前未能 flush 的记录可能丢失。
