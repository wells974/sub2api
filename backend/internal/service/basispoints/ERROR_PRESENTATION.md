# 对外错误展示：第一至三批

默认将普通错误、SSE 和终态 Excel/BPS 错误出口转换成中性提示。内部错误定义、HTTP 状态、账号切换、重试、图片和 Schema 策略不变。这不是所有对外输出的渠道隐藏方案。

## 配置

后端进程环境变量 `SUB2API_BPS_ERROR_PRESENTATION`：

| 值 | 行为 |
| --- | --- |
| 未设置、空值、`neutral` | 中性提示，默认 |
| `legacy` | 保留接入前的错误文案、错误码、param、用户错误记录、响应头行为及公开配置字段名 |
| 其他值 | 回退中性提示，进程内首次遇到时记录警告；不回显配置值 |

不新增系统设置 DTO、数据库字段、前端表单或部署默认项。Docker 部署需显式将环境变量传入后端容器；统一各实例配置。此开关与图片提前压缩、旧截图裁剪、Schema 纠错、按需目录的开关独立。

## 已覆盖的出口

- `forwardExcelBPS` 的 `fail` 普通 JSON 出口，包含发送上游前的拒绝及非 2xx HTTP 拒绝。
- Excel/BPS 图片生成/编辑的显式错误输出，以及解析出的 `OpenAIImagesUpstreamError`。
- Excel OAuth 路由拒绝。
- BPS 限流与图片请求准入在账号切换耗尽后的普通 JSON 输出。

即使请求参数为 `stream:true`，只要还没有进入 SSE，普通 JSON 出口也使用中性提示。

第二批已覆盖：

- 已接受响应的 SSE `error`、`response.failed`、`response.cancelled`、`response.incomplete` 的错误对象；非流式失败终态和流中断。
- compact keepalive 提交响应后的错误，以及最终准入/限流错误的协议序列化出口。Responses 仍用 `response.failed`，通用协议出口仍用 `event: error`，不增加完成事件。
- `response.incomplete_details` 只保留标准 reason（`max_output_tokens`、`content_filter`）；其他原因使用固定 `response_incomplete`。

现有 Chat Completions / Anthropic 转发链路没有通过 `forwardExcelBPS`，本批未改变路由或新增该接入。协议测试覆盖共享错误序列化方法的各入口格式，不能解释为新增端到端 BPS 协议支持。

第三批已覆盖：

- `/api/v1/usage/errors` 普通用户列表的 message、`/api/v1/usage/errors/:id` 的 message 和 error_body。使用读取时投影，历史记录同样适用；不改原存储。通过内部 endpoint、错误类型、错误文案/正文中的明确渠道标识识别（含历史分隔写法、配置名与 JSON 转义的错误字段）。渠道无法识别且没有已知标识的其他错误保持现有展示。
- 详情 error_body 重建为固定错误对象，不逐字段透传原始 JSON/HTML/截断 SSE。保留记录 ID、时间、状态码、上游 HTTP 状态、类别和用户请求属性；归属校验在投影前完成。
- 公开设置 API 与首页 HTML 的 `__APP_CONFIG__`：默认把 `excel_bps_enabled` 改为中性 `tool_bridge_enabled`，布尔值不变。前端独立兼容函数读取新旧格式，保持原有后台开关消费方式。管理员 Settings DTO/数据库键仍保留原名。
- 图片响应头：只在 BPS 图片出口对副本过滤明确的渠道标识/调试头（包括含渠道 URL 的 Location），再走原共享白名单。即使扩大共享白名单也不暴露这些标识；原始响应头仍供请求关联、配额和内部诊断使用。

管理员 Ops 和观察员（受控诊断角色、仍只可看自己的记录）走原有接口，保留原始 message、error_body、上游信息及既有权限约束。不要给普通调用者授予观察员角色来代替普通用户角色。

这不是任意内容的匿名化机制：用户/管理员设置的模型、分组、API Key 名称和站点文案，以及模型生成内容、图片资源内容与 URL 不做全局替换。前端静态代码仍含功能实现名称；历史已下载的记录/浏览器缓存无法撤回。图片能力地址 `/api/bps-images/:token` 是上游取图协议，未更名，避免破坏在途请求。

## 公开契约

中性错误主体保留 `error.type`、`error.code`、`error.message`。第二批终态还保留原有数字 `status` / `status_code`（若存在），以免已提交 HTTP 200 后丢失错误的语义状态。`param` 暂时全部省略；后续如需恢复，仅允许已验证的客户端字段路径。不得反射原始 message，也不得复制 failure_stage、rejected_tool、repair_skipped_reason 等内部字段。

| 分类 | 公开 code |
| --- | --- |
| 未分类失败、普通上游拒绝 | `request_failed` |
| 请求参数不合法 | `invalid_request` |
| 连接、代理、授权、内部配置或容量不可用 | `service_unavailable` |
| 限流 | `rate_limit_exceeded` |
| 请求体过大 | `request_too_large` |
| 图片数量、单图或总量超限 | `image_limit_exceeded` |
| 历史、检查点、需要手动压缩 | `conversation_context_invalid` |
| 工具声明 Schema 不合法 | `invalid_tool_schema` |
| 模型或路由不可用 | `model_unavailable` |

第二批对已拦截的工具批次使用 `invalid_tool_response`，流提前结束使用 `response_incomplete`。未知内部 code 使用固定兜底，不透传。

HTTP 状态由原调用方保留，包括 401、403、413、429、524；标准 error.type 保留，未知类型按状态转成通用类型。Retry-After、请求次数、返回的内部 error、成功数据及用量不改。不会把上游 401 当成新的客户端 API key 校验失败，也不新增自动重试。

## 内部诊断和维护

- 新增 `internal/pkg/errorpresentation` 仅提供模式解析、固定文案和公开类型，不依赖 Gin、网关或 BPS。
- 新增 `openai_excel_bps_error_presentation.go` 收拢内部分类、日志与图片错误副本；handler 的同名模块只处理最终准入/限流出口。
- 新增 `openai_excel_bps_error_events.go` 收拢事件/终态展示。主循环先消费原事件进行用量、失败归因及冷却判断，再在写出的一行接入转换；成功内容、事件种类、sequence、ID、状态和 usage 保持原样。
- 通用 handler 和 compact 错误写出方法各增加可选展示回调，原诊断记录完成后才调用；不传回调的其他渠道保持原路径。两个回调只由 BPS 适配器使用。
- 不改 `basispoints` 桥接器、原错误定义、DTO 或共享 ResponseWriter；不缓存整段 SSE，不改变刷新、断连及重试逻辑。
- 原错误对象、Ops 状态和事件保持原样。新增 `bps_error_presentation` 日志关联现有请求上下文，记录原状态、公开 code 和经过现有 BPS 脱敏及 2048 字节限长的内部错误；不记录请求正文。
- 分类使用内部调用点和内部错误码。对于 `basispoints_request_invalid` 的细分仅将已知错误文案匹配到固定分类，无法识别时使用通用参数错误。不得由客户端 header/query 决定渠道归属或 legacy 模式。
- 不使用请求级“曾走过 BPS”标志，避免账号切换后污染其他渠道的错误展示。

上游合并时核对新增错误出口、桥接器的终态事件结构，并复用展示函数；不要在桥接器解析或纠错前转换错误。内部格式变化最多降低分类精度，不应恢复透传。

## 验证

新增测试检查默认/neutral/legacy/非法模式、未知错误与类型兜底、元数据白名单、状态保留、原错误对象不变、内部日志脱敏、主请求和图片转发、限流与准入出口、Retry-After、SSE/非流式终态的默认与 legacy 对照、Schema 拦截、流提前结束、冷却状态、用量与 Ops 不变，以及成功帧和大整数用量的保留。

既有专门断言原始英文文案或内部 code 的测试显式设置 `legacy`，继续验证原行为；新增测试独立显式使用空配置验证默认中性模式。不要全局将测试环境设为 legacy。

```sh
cd backend
go test ./internal/pkg/errorpresentation -count=1
go test -tags unit ./internal/service ./internal/handler -run 'ExcelBPS|ExcelOAuth|PublicError|Compact|StreamingAware' -count=1
go test -race -tags unit ./internal/service ./internal/handler -run 'TestExcelBPSPublic' -count=1
```

均为本地模拟验证，不代表已在真实付费会话或生产环境验证。

2026-11-12 本地验证结果：上述普通回归通过（service 1075、handler 32 个通过项，包含子测试）；新增路径的 race 检查通过（service 24、handler 4 个通过项）；共享映射包测试及 race 均通过。`git diff --check` 通过。


第二批本地验证（2026-11-12）：

- 扩展回归 `ExcelBPS|ExcelOAuth|PublicError|Compact|StreamingAware` 通过：service 1382、handler 113 个通过项（含子测试）。
- 最新新增的 compact 终态对照也纳入 `TestExcelBPSPublic` race 检查：service 47、handler 13 个通过项，均通过；共享映射包 race 通过。
- 默认与 legacy 对照验证流中限流、取消、不完整终态、EOF、Schema 拦截、compact 心跳后的失败；核对 HTTP 状态、事件类型、请求次数、用量、冷却状态和原始 Ops 诊断。成功帧不改写，大整数用量保持原字面值。
- 相对第一批，仅 3 个原业务文件新增接入：主转发替换 8 处输出调用，compact writer 增加可选回调，handler writer 增加诊断之后的展示回调。其余逻辑放在独立文件；7 个原始错误契约测试显式切换 legacy。
- `git diff --check` 通过。未进行真实上游或生产验证。


## 第三批维护与部署

- `ops_service.go` 两处普通用户投影调用替换；原 `ops_user_error.go` 与观察员/管理员序列化不改。
- 新增 `openai_excel_bps_user_errors.go` 负责历史记录识别和白名单投影；读取记录不新增逐条诊断日志，不回写数据库。
- 新增两个独立 MarshalJSON 实现，共用 `MarshalPublicSettingsPresentation`，不修改两份大 Settings 结构，也不拷贝字段表，避免后续上游增加字段漏同步。必须同时验证 API 与 HTML 注入。
- 图片入口仅增加响应头副本转换调用，共享图片解析器、用量逻辑和过滤器不改。
- 前端只在公开设置 API 和 bootstrap 应用入口接入 `publicSettingsPresentation.ts`，后台各表单、featureFlags 注册表不改。
- 发布时一并部署前后端：旧前端不认识中性字段名，会按旧逻辑把缺失的开关当默认开启。若必须先部署后端，先使用 `legacy`，待前端更新后再启用默认中性模式。HTML/配置缓存应随发布刷新。
- 审计确认：用户用量 DTO 已排除 upstream_endpoint/account 等内部字段，CSV 从此 DTO 导出；用户错误页使用同一错误 API，当前没有独立错误导出接口。渠道监控 v1 不返回错误文案，v2 普通用户投影移除 Details，v3 只返回聚合分类；公开展示页只返回成功作品。
- 不保证从用户自定义内容或功能实现指纹无法推断渠道；本批针对系统生成的错误、公开配置和已知响应头标识。


第三批本地验证（2026-11-12）：

- BPS/Excel OAuth、普通用户错误、观察员错误、公开配置及用量 DTO 回归：service 1164、handler 64、DTO 13 个通过项（含子测试）。
- 前端公开配置 API/首页注入兼容、缓存、featureFlags、用量页与导出测试：5 个文件、53 项通过；`vue-tsc --noEmit` 通过。
- 新增测试验证默认/neutral/legacy/非法配置、历史 JSON/HTML/截断 SSE、仅正文含渠道标识、管理员和观察员诊断保留、越权 404、原对象不变、API/HTML 开关一致及扩展响应头白名单下的隔离。

使用现有前端依赖可直接复验：

```sh
cd frontend
node node_modules/vitest/vitest.mjs run src/api/__tests__/auth-public-settings.spec.ts src/utils/__tests__/publicSettingsPresentation.spec.ts src/stores/__tests__/app.spec.ts src/utils/__tests__/featureFlags.spec.ts src/views/user/__tests__/UsageView.spec.ts
node node_modules/vue-tsc/bin/vue-tsc.js --noEmit
```

最终 race 验证通过：service 95、handler 25、DTO 1 个通过项（包含子测试，service 已纳入最后补充的历史配置名/分隔写法/图片中转错误）。`git diff --check` 通过。以上均为本地模拟，未执行生产部署或真实上游验证。


## 三批整体复查修复（2026-11-12）

1. **Ops 再次解析中性响应的问题**：原来仅保留了内部返回错误、部分 Ops marker 和结构化日志；Ops 中间件仍可能解析中性 wire 文案，导致普通错误/部分 SSE 的入库文案、错误码分类、忽略规则与 legacy 不同。新增 `openai_excel_bps_ops_presentation.go`，展示边界保留小型原错误副本；中间件读取捕获响应时，仅对匹配本次公开 type/code/message 的错误对象恢复内部副本，然后继续原有分类和入库脱敏流程。该副本不写入客户端、不更改上游状态、不复制完整请求或长响应。原中间件仅增加一处调用；既有 BridgeFailure 专用诊断路径保持不变。
2. **历史诊断识别遗漏**：未知嵌套字段、JSON 转义的字段名/值以及 SSE data 内的转义字符串也会检测。`Excel/BPS`、`Excel-BPS`、大小写/空白变化统一识别；普通 Excel 文件错误、用户标签和生成内容不做全局替换。
3. **扩展响应头遗漏**：补充 `X-Excel-*` 和值为单独 `Excel`/`BPS` 的标识头，仍只过滤 BPS 图片出口副本；原请求 ID、重试信息和内部完整响应头保留。
4. **历史授权分类**：内部错误码统一按大小写不敏感方式分类，避免 `OPENAI_EXCEL_AUTH_*` 在历史提取后变成通用错误。legacy 不经过该映射。

共同识别规则移到独立 `openai_excel_bps_error_markers.go`，错误记录和响应头共用；没有增加数据库字段、全局 ResponseWriter 包装或前端表单改动。

复查范围包括普通 JSON、失败 SSE、compact、图片出口、普通用户历史记录、管理员/观察员诊断、公开配置 API/首页注入，以及现有用量 DTO/导出和监控视图。保留此前说明的边界：自定义名称、用户/模型内容、前端静态实现及内部图片能力 URL 不属于全站指纹隐藏；不能据此宣称任何渠道信息都无法推断。


整体复查验证结果：后端相关回归 service 1489、handler 285、DTO 3 个通过项（合计 1777，含子测试）；展示路径 race 检查 service 102、handler 30 个通过项；前端 5 个文件 53 项通过。新增端到端 Ops 中间件测试对比 JSON/SSE 在 neutral 与 legacy 下的原始入库文案、错误类型、归因、状态及业务分类，并验证客户端仅得到中性提示。`git diff --check` 通过。未部署生产，未调用真实上游。
