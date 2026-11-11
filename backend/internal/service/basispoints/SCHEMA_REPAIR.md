# 一次 Schema 纠错（第三批）

本批新增独立的 Schema 参数纠错分支，默认关闭。它使用现有 BPS 连接、路由、取消机制、HTTP 错误处理、配额快照和用量解析，不修改原有传输纠错的保留操作规则。

## 配置

通过后端进程环境变量配置，不增加前端设置、系统设置 DTO 或数据库迁移：

| 环境变量 | 默认 | 含义 |
| --- | --- | --- |
| `SUB2API_BPS_SCHEMA_REPAIR_ENABLED` | `false` | 允许一次 Schema 纠错 |
| `SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS` | `30` | 这次纠错从提交到完整校验的时间预算，启用时允许 1–120 秒；仍受客户端剩余 deadline 限制 |

```sh
SUB2API_BPS_SCHEMA_REPAIR_ENABLED=true
SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS=30
```

变量需要注入后端进程/容器，修改部署后重启；仅在 Docker `.env` 文件中写变量不会自动传入容器。无效开关或启用后的非法超时值会在请求发送前返回设置错误。受控实验请求保持禁用。

关闭开关即恢复原 Schema 失败行为，不需要迁移或清理会话状态。原有传输纠错、未知工具纠错的开关与边界不由此开关替代。

## 触发条件

必须同时满足：

- 原始上游以 `response.completed` 正常结束，失败确实来自已编译 Schema 的参数校验。
- 工具整批仍被桥接器暂存，没有工具被下发；流式客户端没有收到正文、拒绝文本或推理摘要。带正文的生命周期快照同样视为已交付内容。
- 每个调用都有明确且唯一的 item ID / call ID；已有工具历史必须完整，不允许复用已完成历史的身份。
- 所有目标均为当前有效目录中的 function，且使用明确的普通 FUNCTION `run_officejs` 包装。
- 整批没有未知工具、JSON 不完整、资源预算超限、取消、直接原生调用、custom 或原文传输等不符合条件的部分。
- 与按需目录同时启用时，整批不含尚未披露说明的工具。说明打印通过不等于实际参数合法；此类混合批次跳过纠错，避免发送错误的 `schema_valid` 反馈。已披露工具仍可正常进入一次纠错。
- 仍有纠错次数和时间预算。

本批采用保守范围：FUNCTION_CODE、FUNCTION_CMD、CUSTOM，以及代码/命令/补丁/SQL 等工具名或参数字段特征会跳过。具体标识符规则集中在 `schemaRepairPayloadTool` / `schemaRepairPayloadKeys`；例如 `exec`、`shell`、`patch`、`code`、`cmd`、`query`、`content` 等会被排除。这会跳过一部分实际上可以安全修复的工具，以避免通用纠错改写原文载荷。它不是任意执行能力的语义分类器。

非流式请求的正文尚未交付客户端，可以进入新分支；已交付正文的流式请求继续明确失败，不缓冲或重放整轮文本。

与图片自动压缩叠加时，如果流式压缩窗口保留了可见正文，保守跳过本轮 Schema 纠错（`compaction_visible_window`），避免外层窗口已经交付正文而内层桥接器尚不知情。只有不含可见正文的窗口可以继续参与新分支。

## 流程与边界

1. 遍历整批调用，提取经过限长与脱敏的字段路径、关键字、期望类型、缺失字段等诊断。
2. 沿用原生工具错误续接格式，为整批添加 `executed:false` 反馈，明确工具未执行；每个结果只携带本调用的诊断，合法调用标记为参数有效，避免反馈随批次数量平方增长。要求只修正非法参数，保持调用数量、顺序和目标。
3. 使用同一模型、effort、账号、授权、代理和请求元数据提交一次续接，先释放原响应的并发资源。
4. 缓冲纠错响应并重新校验完整批次。要求使用新的调用身份；不接受额外调用、目标替换或已合法调用参数变化。
5. 整批验证通过后才写入回放并下发工具事件，保持原客户端 response ID 和工具输出位置；否则整批失败。

Schema 分支最多提交一次。它与已有纠错回调共享原来的最多两次续接预算，失败后不再进入传输纠错或再发起 Schema 重试。第一版只处理原始完成响应的 Schema 失败；传输/未知工具纠错产生的后续 Schema 错误不会再串联一次新 Schema 纠错。

原本合法的调用参数必须保持一致，比较在解析后的 JSON 上进行；不会用“恢复原参数”掩盖模型对合法调用的改写。所有纠正后的参数仍接受原完整 Schema、数字/嵌套预算和工具批次规则约束。

结构化反馈不包含原始校验器错误或额外参数值。作为同一上游会话的续接，原失败调用本身仍会随现有历史机制发送给同一服务；不会执行任何工具，也不会写入新的凭据或跨账号重放。

## 用量、取消与日志

- 正常成功、再次校验失败、HTTP 拒绝或部分 SSE 后断流，都保留上游已经报告的用量。
- Schema 纠错终态缺失计数或报告零值时，仍保留该次续接的渐进计数；原有传输纠错读取器的默认语义不变。
- 纠错自己的超时产生明确失败终态，携带已知累计用量；不会当作新的客户端请求重发。
- 客户端取消时关闭原响应及纠错响应，不再下发工具。网关在流关闭后最多等待 1 秒收尾，读取独立快照以补回因管道关闭而未到达终态的已知用量。
- 正常终态已经包含累计用量，收尾不会再次相加；已有图片 compact 用量仍由原有 defer 单独累计。
- 未上报的用量不估算；若底层取消收尾超过上述有界等待，最终返回时只能记录当时已知的快照。

启用且遇到 Schema 失败时，写一条 `bps_schema_repair` 结构化日志，沿用请求上下文的 request_id。字段包含 `considered`、`attempted`、`outcome`、`skip_reason` 和 `total_corrections`，不记录参数、提示词、原始错误或凭据。它沿用现有日志级别和采样设置。

常见结果：`succeeded`、`failed`、`timed_out`、`canceled`、`skipped`。常见跳过原因包括 `visible_content`、`resource_limit`、`raw_payload_contract`、`raw_payload_transport`、`unsafe_tool_identity`、`incomplete_tool_history`、`undisclosed_tool` 和 `repair_budget_exhausted`。

## 合并上游时的接入点

主体集中在三个新增文件：

- `schema_repair.go`：整批资格判断、一次纠错、反馈构造及结果约束。
- `schema_repair_context.go`：请求内开关、共享次数、取消收尾与用量快照。
- `../openai_excel_bps_schema_repair.go`：部署配置和服务层收尾。

原有三个文件仅保留窄接入点：

- `stream.go`：包装原回调以共享预算；完成时分派到新入口；识别已交付的生命周期快照。
- `tool_repair.go`：识别新的内部反馈类型，并为 Schema 分支提供渐进用量保留选项；原请求构造、读取器公开入口和旧传输纠错流程保持原样。
- `../openai_excel_bps.go`：建立请求配置上下文，并在流关闭后收尾。

反馈构造复用原 `BuildToolRepairRequest`，只替换它追加的工具结果和最后一条纠错指令；上游若改变追加顺序或 compaction_trigger 的位置，需要同步检查这里的边界测试。原 stream、Prepare 和 ToolRepairFunc 的公开签名保持不变。

## 验证

新增测试覆盖默认关闭、配置拒绝、混合合法/非法批次、脱敏反馈、原身份与回放、目标/数量/合法参数变更、可见正文/摘要/生命周期快照、资源限制、共享次数、超时、取消、部分用量、HTTP/SSE、原路由与并发为 1 时的响应释放。

另有带 `unit` 标签的图片 compact 联合测试，验证压缩、初始生成与纠错三段用量在成功、失败、取消和可见窗口场景下不重计、不漏计已知值。

```sh
cd backend
go test ./internal/service/basispoints -count=1
go test ./internal/service -run '^TestExcelBPSSchemaRepair' -count=1
go test -race ./internal/service/basispoints -run '^TestSchemaRepair' -count=1
go test -race ./internal/service -run '^TestExcelBPSSchemaRepair' -count=1
go test -tags unit ./internal/service -run '^TestExcelBPSSchemaRepair' -count=1
```

这些是本地模拟上游与回归验证，未提交线上请求。启用后仍需观测实际纠错成功率、额外 token 用量和延迟。本批不包含第四批的按需工具目录，也不代表上游 524 已解决。
