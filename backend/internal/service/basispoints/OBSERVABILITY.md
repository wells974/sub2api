# BPS 请求诊断（第一批）

本批只增加服务端观测。图片限额、上传及去重、Schema 校验规则、工具目录选择、重试、HTTP/SSE 错误和用量计算均保持现状。

后续第二批的预算配置、提前压缩和可选截图裁剪见 [IMAGE_BUDGET.md](IMAGE_BUDGET.md)。

第三批的一次 Schema 纠错与安全边界见 [SCHEMA_REPAIR.md](SCHEMA_REPAIR.md)。

第四批的按需工具目录与说明回传见 [ON_DEMAND_CATALOG.md](ON_DEMAND_CATALOG.md)。

## 查看结果

每次 `forwardExcelBPS` 返回时尝试写一条 INFO 结构化日志，消息为 `bps_request_diagnostics`，版本字段为 `diagnostics_version: 1`。沿用请求上下文的 `request_id`，额外记录 `account_id`；一次客户端请求若发生账号切换，可能有多条记录。输出受现有日志级别和采样设置影响，本批不改日志配置，也不能把采样后的日志条数当作全量请求计数。

若日志文件为逐行 JSON，可按消息和请求 ID 筛选：

```sh
jq 'select(.msg == "bps_request_diagnostics")' gateway.log
jq 'select(.msg == "bps_request_diagnostics" and .request_id == "目标请求 ID")' gateway.log
```

`forward_failed` 表示本次转发返回了错误，不等同于 HTTP 状态或上游故障。客户端取消、提前拒绝、已经开始的 SSE 失败均应结合已有错误日志分析。本日志不复制原始错误文本。

## 图片统计口径

`input_images` 统计进入 BPS 转发函数时、图片策略转换和附件上传**之前**的请求。只遍历 `input` 中 message/agent_message 的 `content` 和 function/custom tool output 的 `output` 数组中的 `input_image`，不检查普通文本或工具参数里的任意字符串。

| 字段 | 含义 |
| --- | --- |
| `valid_json` | 输入是否为有效 JSON；无效时其余统计不代表完整请求 |
| `inline_count` | data URL 图片出现次数，包含无法计算字节数的图片 |
| `inline_bytes` | 可计算部分的 Base64 解码后字节数之和 |
| `max_inline_bytes` | 可计算部分的最大单图字节数 |
| `message_inline_bytes` | message/agent_message 内联图片字节数 |
| `tool_output_inline_bytes` | 两类工具结果中的内联图片字节数 |
| `unknown_inline_byte_count` | 非 Base64 data URL、编码损坏或空载荷等无法计算的次数 |
| `url_count` / `file_count` | 非 data URL / file_id 引用出现次数，不下载或估算内容大小 |

相同图片重复出现会重复计数；这是请求携带的数据量，不是去重后的上传量。错误请求同时带 URL 和 file_id 时，两类引用都会计数。超过现有限额后仍统计完整请求，便于定位累积超限。

Base64 字节数通过编码字符及 padding 计算，兼容 CR/LF，不额外解码图片缓冲区。能计算字节数不代表图片格式有效或允许上传；未知部分不计入 `inline_bytes`，必须同时看 `unknown_inline_byte_count`。本批没有改变或自动放宽任何限额。

## 目录统计口径

`catalog_prepared` 表示最终持有成功准备的 Bridge；为 false 时，不能把零值理解为工具目录为空。

`catalog` 统计最终准备版本的**有效目录**，包括继承和动态加载的工具；`tool_choice: none` 的未启用工具不计入：

- `tool_count`：实际目录条目数，命名空间中的工具按展开后的条目计数。
- `prompt_bytes`：实际目录文本的 UTF-8 字节数，不包含外围协议说明、示例或历史。按需目录关闭时是原 `describeCatalog` 的结果；第四批启用且生效时改为精简目录的实际大小，完整目录大小另见 `bps_tool_catalog.full_prompt_bytes`。
- `schema_bytes`：有效工具原始 parameters 紧凑 JSON 字节数之和；不含工具描述、名称及 custom format。
- `max_schema_bytes`：最大的单工具 parameters 紧凑 JSON 字节数。

目录文本和原始 parameters 口径不同，不能直接相减作为“压缩节省”。这些均不是模型 token 数，也不参与计费。附件上传或自动压缩后的 Reprepare 会生成新统计，日志读取最终 Bridge。

## Schema 诊断

- `schema_phase: catalog_compile`：把已有 `ToolSchemaValidationError` 的工具、路径、原因写入 `schema_tool`、`schema_path`、`schema_reason`；客户端错误保持原样。
- `schema_phase: tool_arguments`：只在桥接器最终发出协议失败时，记录 `schema_failure`，避免把内部试探校验误报为请求失败。

`schema_failure` 包含 `tool`、`path`（实例 JSON Pointer）、`keyword`、`reason`。`reason` 区分 `schema_validation` 和 `resource_limit`。类型错误另有 `expected_types`、`actual_type`；required 错误的 `missing` 最多记录 8 个 Schema 字段名。资源预算失败不猜测路径或关键字。

诊断只选校验树第一条叶子，不是全部错误列表，也不自动修复参数。直接 Schema 中无法确认的属性（含动态键和间接结构）显示为 `[redacted]`；路径长度受限。记录声明的安全标识符，不记录参数值、枚举值、Schema 正文、提示词、图片载荷/URL、凭据或原始校验器错误。

## 二开维护边界

新增实现集中在：

- `backend/internal/service/basispoints/image_observations.go`：独立图片统计。
- `backend/internal/service/basispoints/argument_observations.go`：校验错误转为有界诊断。
- `backend/internal/service/basispoints/bridge_observations.go`：目录统计与并发安全快照。
- `backend/internal/service/openai_excel_bps_diagnostics.go`：日志汇总，不依赖 Gin 或响应写入器。

原有代码仅有六个文件的窄接入点：Bridge 初始化及目录渲染包装、两处现有校验路径的错误转换、错误类型的诊断载荷、终态失败观测、转发退出日志和准备失败捕获。没有复制原转发流程，没有更改原目录渲染函数，也没有新增数据库迁移、配置项或前端依赖。

合并上游时，保留这些接入点；重点检查上游是否新增了图片输入位置、改变目录渲染入口、校验器类型或终态错误路径。诊断状态使用指针和锁，因为现有代码会按值复制 Bridge 并在流读取协程中运行校验。诊断快照不暴露内部可变切片。

统计带来输入 JSON 遍历、Base64 字符扫描和原始 parameters 序列化开销，不发起额外网络请求。图片测量会分配一份 input JSON 片段，不创建解码后图片副本。本机 Apple M4 的 1 MiB 单图基准约为 8.4 ms、1.4 MB / 1 次分配；这只是本地观测，不是生产性能保证。所有统计仅服务后续图片预算、Schema 优化和目录缩减决策；这三类策略不在本批实现。

## 验证

新增测试覆盖重复图片、工具结果图片、损坏编码、超过 32 MiB 继续计数、输入不变、Schema 字段诊断及脱敏、并发快照、目录继承/Reprepare/禁用、HTTP/SSE 成功及拒绝路径。流测试比较启用和停用观测时的完整 SSE 字节，服务测试验证请求次数、原错误文案及用量保持不变。

```sh
cd backend
go test ./internal/service/basispoints
go test ./internal/service -run 'TestExcelBPS|TestNewExcelBPS|TestSetting.*ExcelBPS' -count=1
go test -race ./internal/service/basispoints -run 'Test(ImageObservations|InlineDecoded|ArgumentObserv|CatalogObserv)' -count=1
```
