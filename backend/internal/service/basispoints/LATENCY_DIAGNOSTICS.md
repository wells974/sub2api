# BPS 阶段耗时与输出类型诊断

新增内部日志 `bps_latency_diagnostics`，与 `bps_request_diagnostics` 共用现有
`request_id` / `client_request_id`。每次进入 BPS 转发尝试记录一次，包括提前拒绝。
不新增环境变量、数据库字段或公开接口；沿用现有 INFO 日志级别和采样设置。
没有修改原 `FirstTokenMs`、用量、HTTP 状态、重试、附件缓存规则、工具整批校验
或流刷新行为。非 BPS 路径不安装观察器。

## 时钟和阶段

`latency.forward_elapsed_ms` 及所有事件时间点均从现有 Forward 的 `start` 起算，
使用 Go 单调时钟。这不是客户端发出请求的时间，也不是用户页面渲染时间。
`entry_offset_ms` 表示进入 BPS 观察器前的 Forward 时间。网关排队仍看现有
`queue_wait_ms`，不要将它解释为某个阶段的纯 CPU 耗时。

`latency.stages_ms` 是顺序阶段的墙钟耗时，包含相应网络、锁、缓存等待：

| 阶段 | 范围 |
| --- | --- |
| `setup` | 原始输入统计、配置、准入、身份及请求初步处理 |
| `image_history` | 历史检查点读取、重放、预算规划、裁剪与保存 |
| `image_prepare` | 图片完整校验、原生附件计划或中转改写 |
| `catalog_prepare` | 初始目录准备及 Schema 编译/校验 |
| `authorization_proxy` | 授权、账号及附件代理准备 |
| `attachment_upload` | 原生附件缓存查询、在途等待、实际上传及结果改写 |
| `attachment_reprepare` | 附件完成后的协议重新准备，仅有附件时出现 |
| `auto_compact` | 原有自动压缩分支，包含其上游请求、读取及检查点处理 |
| `catalog_delivery` | 按需目录处理，未满足条件时原样回退 |
| `generation_headers` | 最终生成请求到响应头及接受/拒绝处理，包含原有重试 |
| `generation_receive` | 初始上游读取、协议桥接、可能的纠错、下游流写出 |
| `finish` | 流结束后的终态处理、非流式返回、状态保存及 defer 收尾 |

阶段名只接受固定词汇。`last_stage` 是最终观察时所在阶段；失败阶段可能只有
部分耗时。未进入的阶段不出现。条件分支未执行时也可能经过它的边界，显示
0 毫秒，不能据此认定执行过压缩或上传。小于 1 毫秒按整数截断，阶段之和加上
`entry_offset_ms` 与总量可有数毫秒舍入差。没有独立测量每一项授权、Redis 或
HTTP 网络子阶段。

## 输出事件

`latency.upstream`：桥接器解析**初始被接受的生成响应**时观察到的事件。
这是完整 SSE 事件解析时间，不是网卡首字节；不包含附件响应、自动压缩响应
和纠错/重新生成回调内部读取的事件。初始上游 completed 后仍可能发生纠错。

`latency.client_write`：流式转发在 Gin writer 写入 data 行成功后观察到的事件。
只保留计时与类型，不缓存或复制整个流。不代表 Flush 完成、网络送达或客户端
已显示；没有替换/包装共享 ResponseWriter。非流式路径不会伪装为流式下发，
成功 JSON 返回后单独记录 `json_return_ms`（handler 写出调用返回，非送达确认）。

两个对象字段相同：

- `first_event_ms`：首个 JSON SSE 事件，可是 created 等元数据；keepalive、
  `[DONE]` 不计入，不能当“首字”。
- `first_text_ms`：首次观察到非空输出文本，包括 delta、done、part 或 item/终态快照。
- `first_reasoning_ms`：首次观察到非空推理摘要；空 reasoning item 不算可见内容。
- `first_tool_ms`：首次观察到工具 item/参数事件，或终态里的工具。
- `terminal_ms` / `terminal`：终态时间及固定标准事件名。
- `text`、`tool`、`reasoning`、`refusal`、`other`：曾观察到的输出类别。
- `output_type`：固定摘要分类 `text_only`、`tool_only`、`text_and_tool`、
  `refusal_only`、`reasoning_only`、`other`、`none`。优先文本/工具，详细混合情况
  看布尔字段。失败流也保留已观察到的类型；不代表对应工具已经执行。

未观察到的时间点**省略**，不是 0；0 表示进入观察后的同一毫秒。
例如 `upstream.first_tool_ms=3000`，`client_write.first_tool_ms=17000`：
可以确认工具首次上游出现到网关下发之间隔了 14 秒，但这包含后续生成、
整批校验、潜在纠错和阻塞写出，不能称为“Schema 校验用了 14 秒”。
纯文本在终态前仍原样流式转发，工具原有暂存边界保持不变。

使用记录的原 `FirstTokenMs` 仍按现有文本 delta 或 output item added 事件计时；
它与新增 `first_event_ms`、`first_text_ms` 的定义不同，不应直接混用。

## 附件缓存与上传

`latency.attachments` 只统计当前请求的原生附件路径：

- `cache_hits`：跨请求附件 ID 缓存命中次数；同一请求内的相同图片去重不重复计数。
- `inflight_waits` / `inflight_wait_ms`：等待其他请求正在进行的同一附件上传，
  包含因取消而结束的等待，不误记为缓存命中或本请求上传。
- `upload_attempts` / `upload_bytes`：本请求实际启动的上传次数和图片解码字节之和，
  包含失败尝试；不是 HTTP multipart 总字节，也不是成功字节。
- `upload_failures` / `upload_ms`：上传回调/附件 ID 校验失败次数及总耗时。

这些计数与使用记录中的 **token 缓存率** 完全不同；不改变 TTL、去重、并发准入、
上传顺序和返回错误。没有跨请求共享诊断状态。

## 排查用法

```sh
timeout -k 2s 15s docker logs --tail 10000 sub2api 2>&1 \
  | grep 'bps_latency_diagnostics' | tail -n 10
```

结合既有 `bps_image_history_trim`、`bps_image_budget_compaction`、`bps_tool_catalog`
及 `bps_schema_repair` 判断对应功能是否实际执行。取消时不等待观察器收齐事件；
记录当时的线程安全快照，后续未完成的上传或事件可能只有开始计数。

日志不保存工具名称、参数、正文、图片、URL、附件 ID、凭据或缓存 key。
只新增单条有界汇总日志，事件路径做固定分类与计时。存在轻量解析、时钟及锁
开销，不能宣称零开销。

## 合并维护与验证

- `latency_observation.go`：独立 context 观察器、固定分类、并发快照和附件统计。
- `../openai_excel_bps_latency.go`：日志适配。
- 原业务文件仅接入 `openai_excel_bps.go` 的阶段/写出观测、`stream.go` 的一个
  已解析事件观测点，以及 `attachments.go` 的命中/在途等待/上传边界。
- 不修改已有请求诊断字段；新增日志独立版本化。上游改变阶段、工具事件结构或
  增加纠错读取入口时，检查计时范围说明和对照测试，不只看 Git 是否冲突。

相关测试比较观察器有无时的 SSE 字节、失败工具暂存、文本增量交付、非流式
输出、原输入/用量/请求次数、附件重复/跨请求复用/上传失败、取消及并发快照。

```sh
cd backend
go test ./internal/service/basispoints -count=1
go test -tags unit ./internal/service ./internal/handler -run 'ExcelBPS|ExcelOAuth' -count=1
go test -race ./internal/service/basispoints -run '^TestLatencyObservation' -count=1
go test -race -tags unit ./internal/service -run '^TestExcelBPSLatencyIntegration$' -count=1
```

本次本地验证：上述四组测试全部通过，`git diff --check` 通过。已验证开启观察器
前后的桥接 SSE 字节一致、工具校验边界保留、文本可在终态前交付、原用量及
请求次数不变；新增观察器和服务集成测试通过竞态检查。尚未部署到线上，实际
慢请求的耗时分布需发布后按同一 request_id 采样确认。
