# v2.21.3 上游合并记录

合并来源：`ranxi2001/sub2api` 的 `v2.21.3`（`aa8e596e6d6f3e4648d6beba73b39b240dd5357a`）。
二开基线：`v2.20.5.4`（`406779e88`）。合并后本地版本：`2.21.3.1`。
本次仅合并和验证代码，不创建发布 tag、不推送、不部署、不修改线上配置。

## 重叠实现的取舍

| 范围 | 合并后的实现 |
| --- | --- |
| BPS 参数诊断 | 采用上游 `newToolArgumentsError` 的规则选择、路径脱敏及 JSON/SSE `tool_arguments`；删除二开独立遍历校验树的实现。命名空间工具保留完整名称。 |
| 一次 Schema 纠错 | 保留。通过薄适配层读取上游选中的同一条诊断，只补充内部需要的缺失字段/类型信息。`oneOf`/`anyOf` 不再被折叠到任意失败分支。再次纠错失败时继续保留上游诊断。 |
| 对外错误展示 | 保留 neutral/legacy 设置。neutral 隐藏渠道与工具诊断细节，内部 Ops 仍记录上游诊断；legacy 返回上游的新格式。 |
| 图片处理 | 保留独立预算、旧工具截图裁剪、普通历史图片压缩、附件复用和检查点回放。接入上游 Fast 策略，但图片准备、重放和纠错请求均不向 BPS 发送 `service_tier`。 |
| 工具目录与推理强度 | 保留按需目录、未披露工具的纠错边界及 `configuration_update` 支持；上游没有等价替代功能。 |
| 账号池通知 | 保留号池快照、通知队列和迁移 `336_account_ops_pool_notifications.sql`，挂载到上游新版账号运维布局，并保留全局设置与号池设置的保存互斥。 |
| 更新机制 | 保留自定义更新仓库、容器更新持久化、四段版本号及 reauth 打包支持。 |

其余 v2.21.3 新增功能和修复按上游合入，包括 BPS bearer 用户头、托管连接临时 5xx 处理、400/409 恢复、千问平台、风控、调度及用量平台快照。400/409 Beta 和二开可选扩展保持各自默认配置，不自动开启。

## 关键回归

- 桥接包全量测试：图片预算/压缩/回放、工具目录、自动纠错和诊断。
- 新增联合 Schema 回归：验证第二个分支也可成功纠错，失败最多纠正一次，不提前下发工具；纠错再次失败仍带规范诊断。
- JSON/SSE 分别验证 neutral 与 legacy：公开结果和内部 Ops 的可见范围不同，但不丢失内部错误归因，不泄漏参数值。
- 自动纠错及普通历史图片压缩测试携带 `priority`，检查初次转发、纠错和关闭扩展后的检查点重放都省略 BPS 不支持的字段。
- 服务层竞态检查发现上游新增的服务档位读取与纠错回调更新 `upstreamBody` 并发；合并适配改为在启动桥接协程前提取初始档位，避免主转发流程读取纠错中的请求体变量。
- 前端验证号池设置只有一个入口；号池保存和全局配置保存互斥，未提交的全局编辑不被号池保存响应覆盖。
- 临时 PostgreSQL/Redis 集成测试覆盖全部启动迁移、号池并发入队/重启/重试以及 BPS 图片历史缓存。

## 验证命令

以下验证最终均通过，包括修复请求体并发访问后的服务层竞态复验。网关定向测试范围包含 381 个顶层测试（另有子测试）；前后端正式构建通过。

后端在 `backend/` 执行：

```sh
go test ./internal/service/basispoints -count=1
go test -tags unit ./internal/service ./internal/handler ./internal/server/routes ./internal/server/middleware -run 'ExcelBPS|ExcelOAuth|AccountOps|UpdateService|CompareVersionsLocalRevision|Setting.*ExcelBPS|Ops.*(Bridge|Presentation|Stream)' -count=1
go test -race ./internal/service/basispoints -run 'Test(SchemaRepair|ToolArgumentsDiagnostic|ArgumentObservation|MessageImage|Trim|ImageBudget|OnDemand|CatalogDisclosure)' -count=1
go test -race -tags unit ./internal/service -run 'TestExcelBPS(SchemaRepair|MessageImage|ToolArgumentsDiagnostic|Extensions)' -count=1
go test ./migrations ./internal/pkg/errorpresentation -count=1
SUB2API_TEST_POSTGRES_DSN= SUB2API_TEST_REDIS_URL= SUB2API_TEST_REDIS_IMAGE=redis:8-alpine CI=true go test -tags integration ./internal/repository -run 'TestAccountOpsPool|TestExcelBPSImagePolicyRealRedis' -count=1
go build -tags embed -o /tmp/sub2api-v2.21.3.1-merge-check ./cmd/server
```

前端在 `frontend/` 使用现有锁定依赖直接运行：

```sh
node node_modules/vitest/vitest.mjs run src/components/admin/operations/__tests__ src/views/admin/__tests__/AccountOpsView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts src/utils/__tests__/publicSettingsPresentation.spec.ts src/api/__tests__/auth-public-settings.spec.ts
node node_modules/vue-tsc/bin/vue-tsc.js -b
node node_modules/vite/bin/vite.js build
```

前端上述范围去重后共 165 项测试。发布矩阵 17 项测试在临时 Debian 容器（Python 3.11、Bash 5）执行；容器更新持久化 6 项测试使用临时 Alpine 容器和虚构程序执行，均不发布镜像或部署应用。

## 升级边界

累计带入上游 8 个 SQL 迁移，保留原有二开迁移原文。数据库集成测试使用临时空库，不代表已对生产数据库副本进行升级演练。
`250_openai_cache_write_ttl.sql` 的尾部空行来自上游 tag；为遵守不可变迁移约束未作格式调整。相对上游的自有改动执行 `git diff --check v2.21.3` 检查。

所有请求链路验证使用测试桩或隔离服务，不调用真实付费模型；真实供应商兼容性、线上负载与部署验收不在本次范围内。

## 二次逻辑复查

重点对照自动合并后的跨模块契约：规范 Schema 诊断与一次纠错、图片裁剪/压缩/缓存回放、按需目录、公开错误与 Ops 原始归因、号池配置保存，以及更新仓库与持久化更新。发现并修复两处缓存用量字段适配遗漏：

1. 上游新增嵌套 `cache_creation_input_tokens` 别名和 30 分钟/12 小时缓存写入明细后，BPS 的缓存转普通输入功能仍只清除旧字段。客户端因此仍可能读到缓存写入用量，与内部按普通输入计费不一致。现在清除所有受支持的别名和时长明细，保留原始内部测量、总输入、缓存读取及无关扩展字段。
2. 图片压缩用量的加减没有同步扩展时长字段。正常终态可从聚合响应中读取明细，但纠错取消或压缩后转发失败时，兜底结果漏记压缩阶段的时长明细。现在加减同时覆盖两种时长，保持正常终态不重复累计。

先扩充回归测试，确认修复前缓存转普通输入的 JSON/SSE、普通/compact 接口及中间/失败终态存在遗漏，并复现纠错取消和压缩后 HTTP 500 两条路径丢失明细。修复后的验证覆盖成功、纠错失败、取消、可见历史阻止纠错和压缩后转发失败；同时复验托管授权连接计费和上游缓存时长计价。

本轮复验命令（后端在 `backend/`，前端在 `frontend/`）：

```sh
go test ./internal/service/basispoints -count=1
go test -tags unit ./internal/service ./internal/handler ./internal/server/routes ./internal/server/middleware -run 'ExcelBPS|ExcelOAuth|ManagedBPS|AccountOps|UpdateService|CompareVersionsLocalRevision|Setting.*ExcelBPS|Ops.*(Bridge|Presentation|Stream)|CacheWriteTTL' -count=1
go test -race -tags unit ./internal/service -run 'TestExcelBPS(SchemaRepair|MessageImage|ToolArgumentsDiagnostic|Extensions|CacheCreationAsInput)|TestManagedBPSConnectionBillsCacheCreationAsShownToClient' -count=1
node node_modules/vitest/vitest.mjs run src/components/admin/operations/__tests__/AccountOpsPoolSettings.spec.ts src/views/admin/__tests__/AccountOpsView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts src/utils/__tests__/publicSettingsPresentation.spec.ts src/api/__tests__/auth-public-settings.spec.ts
```

上述复验最终全部通过，包含服务层竞态检查和前端 36 项测试。计费回归同时覆盖有/无时长明细；有明细且未开启缓存转普通输入时，按上游 30 分钟/12 小时价格分别计费。

本轮没有修改数据库迁移和前端实现；沿用上一轮隔离数据库验证及正式构建结果。未推送、未部署。
