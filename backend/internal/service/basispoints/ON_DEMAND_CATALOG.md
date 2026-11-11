# 按需工具目录（第四批）

默认关闭。借鉴 [ghcp_proxy 的客户端工具说明回传](https://github.com/Nonary/ghcp_proxy/blob/846467460080b0272b4d7636dbe984fc0fea0a9e/docs/tool-token-reduction-2026-11-04.md)，只减少普通生成请求中工具目录的展示量。完整有效工具目录、原始 Schema 校验、现有 `run_officejs` 转发协议和 Excel 请求头均保留。

## 配置与准入

```sh
SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED=true
```

环境变量需传入后端进程或容器；修改部署配置后重启。未设置或设为 `false` 时不改变请求内容和调用行为。无效布尔值在发送上游请求前返回设置错误。受控实验不启用此优化。

启用后还需同时满足：

- 有效目录超过 20 个工具。
- 客户端声明了 `mcp__node_repl.js`（命名空间或完整名称）或 `mcp__node_repl__js`，是带 `code: string` 的 function，描述明确包含 `nodeRepl.write`，且原始完整 Schema 允许只提供代码的打印调用。
- 没有与内部查询名 `__sub2api_tool_catalog` 冲突的真实客户端工具。
- 使用自动工具选择，不涉及结构化输出，并且实际目录文本确实变小。

未满足条件时使用原完整目录。原桥接层仍只支持 `tool_choice: auto/none`，本批不新增强制工具选择支持。

Node 输出工具、`exec_command`、`write_stdin`、`apply_patch`、`update_plan`，以及明确声明 `defer_loading:false` 的工具展示完整定义。过大或不能通过输出工具 Schema 的说明也保留完整展示。其他工具只列出完整名称；每轮都保留全部执行契约。

## 首次使用与说明版本

模型通过普通 FUNCTION 包装请求元数据：

```json
{"name":"__sub2api_tool_catalog","arguments":{"names":["exact_namespace.tool"]}}
```

一次可请求 1–4 个不同的有效工具名，合计说明最多 32 KiB；过大的组合需分开请求。内部查询名不会加入客户端工具定义，也不会作为虚构工具下发。

代理把查询转换为已经声明的 Node 工具调用，代码只包含：

```js
nodeRepl.write("经过 JSON 转义的完整说明文字");
```

说明包含完整工具声明，包括描述、Schema 的原始约束、custom grammar、其他声明字段及独立版本摘要。任何描述中的引号、换行和类似 JavaScript 的文字都只是字符串内容。代理不执行 JavaScript、不访问文件或网络。

模型若直接尝试未披露的工具，普通 FUNCTION、原文传输及直接调用路径都会先改成说明打印；本次目标动作没有执行。客户端执行打印并回传完整说明后，模型需要再发出实际调用。常规 Schema 校验仍在实际调用时执行。

只有当前历史中满足以下条件，才认为说明已经披露：

- 存在匹配的完整打印调用和其后的唯一成功结果。
- 结果确实包含完整说明内容，只有版本摘要或部分文字不算。
- 说明的名称、版本及完整定义与当前有效目录一致。

版本摘要包含描述、`defer_loading`、Schema、grammar 和其他声明字段，独立于原先忽略描述的执行契约指纹。定义改变后需重新披露。失败、拒绝、截断、缺结果或缺调用会回退完整目录，避免不断要求重复打印。通用结果只接受文本及常见 MCP `content` 包装；无法确认其完整性时保守回退。

历史压缩删除说明调用和结果后，重新披露；如果仅残留结果则回退完整目录。绝不因为缓存里存有定义就认定模型读过说明。披露状态只在当前请求中计算，不新增 Redis 键、数据库状态或跨会话缓存。

完整目录原有缓存仍按原规则工作：显式声明可恢复目录；目录缓存丢失且客户端省略 `tools` 时，不能从说明结果凭空恢复工具权限。客户端需重新发送有效声明。

## 回放、费用与压缩

整批仍通过原有暂存、完整校验后才下发。打印调用保留原 call ID，原生回放缓存命中时保留原上游调用；缓存丢失时可从完整客户端调用重建打印历史。后续请求的目录前缀不随“已披露工具数”增长。

没有新增代理侧模型循环或重试。首次使用按需工具通常增加一次客户端工具往返和下一轮生成，费用和耗时由原有各轮用量记录反映；不保证总费用下降。第三批 Schema 纠错不会把正常说明打印当作参数失败重新生成。


接入点位于上传和自动压缩完成后、最终生成发送前。压缩请求保持原有禁用工具的准备逻辑；最终生成根据实际压缩后的历史重新判断披露状态。图片 compact 的用量仍由原流程独立累计。

## 可观测性

开关启用时记录 `bps_tool_catalog`，只包含计数、字节数和静态原因，不含工具描述、参数、历史或凭据：

- `active`、`reason`：是否生效以及回退原因。
- `eager_count`、`deferred_count`、`disclosed_count`：完整展示、按需展示、已披露数量。
- `full_prompt_bytes`、`prompt_bytes`：完整目录和实际目录文本的 UTF-8 字节数；不包括外围协议、示例和历史，也不是 token 计费值。

第一批 `bps_request_diagnostics.catalog.prompt_bytes` 同步统计实际目录文本，`tool_count` 和 Schema 大小仍统计完整有效目录。

常见回退原因：`small_catalog`、`incompatible_carrier`、`lookup_name_collision`、`structured_output`、`no_deferred_tools`、`no_size_reduction`、`disclosure_failed`、`disclosure_incomplete`。

## 合并上游时的接入点

主体集中在 3 个新增文件：

- `tool_catalog_on_demand.go`：目录展示、启用条件、完整目录回退。
- `tool_catalog_disclosure.go`：说明版本、历史识别、安全打印和调用拦截。
- `../openai_excel_bps_tool_catalog.go`：环境配置和日志。

原有 3 个文件增加窄接入点：

- `request.go`：保存目录展示信息，原渲染返回值不变。
- `tools.go`：识别内部说明查询；在实际工具参数处理前判断是否需先披露。复用原有批次校验和回放写入。
- `../openai_excel_bps.go`：提前读取配置，在最终生成提交前应用目录展示。

不修改目录缓存协议、公开 Prepare/Reprepare 签名、前端设置或数据库。上游修改目录 prologue、示例拼接、call/result 历史或工具翻译路径时，需同步检查这些接入测试。目录文本替换只作用于代理生成的 developer 消息，要求找到完整原目录；布局不匹配会在生成提交前明确失败，不静默发送不一致的目录。

## 验证与灰度

本地测试覆盖开关关闭、准入与回退、首次调用、批量查询、完整说明、定义更新、原 Schema 校验、声明继承、历史压缩、回放缓存失效、目录缓存失效、原文与直接调用、混合批次原子性、文本注入、HTTP/SSE、路由和用量、图片压缩联合路径。Node 可用时还实际运行隔离的打印桩，确认恶意形状的描述只能打印，不能变成代码。

```sh
cd backend
go test ./internal/service/basispoints -count=1
go test -tags unit ./internal/service -run '^TestExcelBPSOnDemandCatalog' -count=1
go test -race ./internal/service/basispoints -run '^TestOnDemandCatalog' -count=1
go test -race -tags unit ./internal/service -run '^TestExcelBPSOnDemandCatalog' -count=1
```

以上为模拟上游与本地回归，未进行真实付费会话验证。灰度时观察目录字节、回退原因、首用额外轮次、总输入用量和任务完成率；524 故障率应单独对照，目录缩小不代表已经解决上游 524。
