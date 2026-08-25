---
icon: material/new-box
---

# 流量遥测

`traffic-telemetry` 服务收到所跟踪连接的关闭事件后，会导出一条 OTLP 日志记录。
DNS 伪连接不会被导出。

发送采用尽力而为方式，数据只缓存在内存中。持续的连接突发可能塞满有限的 buffer；sing-box 关闭时仍然活跃的连接不会被导出。

端点通过主机直接连接，不使用 sing-box 出站路由或代理环境变量。支持 HTTP 和 HTTPS；服务会在配置的端点后追加 `/v1/logs`。

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

完整的 OTLP/HTTP 基础端点。服务会在其后追加 `/v1/logs`。

#### headers

可选的 HTTP 请求头，会随每个 OTLP 请求发送。
