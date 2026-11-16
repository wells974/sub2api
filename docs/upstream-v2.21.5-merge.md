# v2.21.5 上游合并记录

上游：`ranxi2001/sub2api`，`v2.21.5`（`b9feadf7dd6228a4117e360821d9994b7cc54897`）。
二开基线：`83091088a`，版本 `2.21.3.1`。合并分支：`codex/merge-v2.21.5`；本地版本：`2.21.5.1`。
仅本地合并和验证，不推送、不创建发布标签、不部署、不修改线上配置。

## 合并取舍

- 合入 v2.21.4 和 v2.21.5 的全部上游改动，包括 BPS 工具选择/账号搜索、HTTP/WS 会话恢复、供应商钱包、多邮箱、Free Fast 费用快照、QQ/优惠券和余额预测扫描移除。
- 保留二开的图片独立预算、历史截图裁剪、普通历史图片压缩与缓存复用、一次 Schema 纠错、按需工具目录、configuration_update 推理强度、neutral/legacy 错误展示和内部诊断。
- 保留缓存转普通输入的全部别名/TTL 明细兼容、图片用量加减的 30 分钟/12 小时明细，以及启动纠错协程前读取服务档位的并发修复。
- 保留号池状态/数量通知、配置保存互斥、持久化队列及迁移 `336_account_ops_pool_notifications.sql`；同时保留上游供应商关联和逐邮箱投递回执。
- 保留自定义更新源、容器持久化更新、四段版本号和 reauth 发布支持。各项可选功能沿用原默认开关。

## 实际兼容修复

1. **指定工具与 Schema 纠错**：原批次的参数错误可能遮住后续错误目标，导致不必要的纠错请求。现在纠错资格检查遍历整批工具选择约束；纠错完成后仍执行完整批次验证。先用测试复现错目标仍触发纠错，再修复；成功的 required/指定工具仍可正常纠错。
2. **当前任务与历史配置更新**：上游新增“最后有意义输入”识别未包含二开支持的 configuration_update，可能把其前的当前密文任务当成旧历史省略。增加控制条目识别，覆盖消息、agent_message 和工具结果；新的明文任务仍允许旧密文历史按原策略省略。
3. **号池与供应商配置**：解决页面 normalize 冲突，同时保留 pool_notifications 和 provider_id。另修复分组保存后、后台刷新尚未返回时立即重新打开编辑导致 provider_id 丢失的问题；通过真实组件交互复现和验证。
4. **搜索与公开错误**：上游搜索错误测试使用 legacy 断言原始协议；二开 neutral 路径单独验证隐藏渠道/参数细节、保留失败与已知用量。

## 搜索组合边界

搜索使用上游独立的多轮流处理，保持“不纠错、不重放、不切号”。进入搜索流程的请求，即使本轮尚未执行搜索，也不使用普通 Schema/传输纠错回调；这些回调持有初始请求体，无法直接用于搜索后的原生续轮。未进入搜索流程的请求保留一次 Schema 纠错。

按需目录与搜索可以同时启用。联合回归覆盖自动/必选搜索、JSON/SSE、并发为 1 的响应释放、搜索后披露工具说明、下一轮实际调用，以及关闭搜索后的历史回放；私有搜索工具不会继承进客户端工具目录。生成和续轮均不携带 BPS 不支持的 service_tier。

号池联合回归覆盖多邮箱去重、失败地址独立重试、不重复发送成功地址、供应商关联与号池配置共存，以及关闭通知后停止投递。

## 合并后二次复查

新增修复均先以测试复现：

- **搜索失败归因**：搜索流程对本地工具校验失败原先统一输出 `basispoints_search_failed`，丢失 Schema 结构化诊断，且被当成上游故障计入账号质量。现在通过内部类型化错误跨越搜索管道，保留 `bridge_tool_validation`、`search_no_repair`、字段路径及累计用量。仍不纠错、不重放、不下发非法工具；原始上游 SSE 不能伪造本地失败归因。覆盖搜索前后、未知工具、JSON/SSE、neutral/legacy 与诊断脱敏。
- **历史压缩与推理强度**：自动压缩删除历史 `configuration_update` 后，生成续轮会退回顶层默认 effort。现在改写历史前验证并折叠原始配置更新，保留有效强度；同时覆盖后续请求的 checkpoint 对账，且不会修改原请求或掩盖被移除的非法配置。工具目录压缩后的实际生成请求也验证 effort 保持一致。

复查同时核对二开文件、搜索与目录的接入顺序、纠错整批原子性和号池通知配置保留。工作区已有的限流诊断提交 `008c0f621` 一并保留，本次修复继续基于该提交。

二次复查验证通过：BPS 桥接包全量 `-race`；服务层、管理 handler、DTO 的 BPS / 号池定向测试；服务层和管理 handler 的搜索、纠错、目录、图片策略、号池定向 `-race`；后端 embed 构建及 `-version`（2.21.5.1）；`git diff --check`。本轮未修改前端和迁移，因此沿用上次对应验证结果。所有调用仍使用本地测试桩，没有连接生产服务。

## 验证

已通过的检查：

- BPS 桥接包全量测试及全量 `-race` 检查，包含上游搜索/工具选择/上下文与二开纠错/目录/图片测试。
- 服务、handler、路由、中间件定向回归：BPS、托管授权、号池运维、更新与版本比较、错误展示、缓存 TTL。
- 隔离 PostgreSQL/Redis：完整迁移启动、号池并发入队/回滚/重启/重试、供应商钱包快照、图片历史缓存、Free Fast 费用快照；另验证质量规则搜索开关的保存/恢复及手动修改保留。
- 前端 28 个测试文件、317 项去重测试，包含号池/供应商配置交互、搜索设置、质量规则、Free Fast 费用展示和公开错误设置；类型检查、相关文件 ESLint、Vite 正式构建均通过。
- 迁移包及中性错误工具包测试；相对上游的 `git diff --check`。

后端验证命令（在 `backend/` 执行）：

```sh
go test -race ./internal/service/basispoints -count=1
go test -tags unit ./internal/service ./internal/handler ./internal/server/routes ./internal/server/middleware -run 'ExcelBPS|ExcelOAuth|ManagedBPS|AccountOps|UpdateService|CompareVersionsLocalRevision|Setting.*ExcelBPS|Ops.*(Bridge|Presentation|Stream)|CacheWriteTTL' -count=1
go test -race -tags unit ./internal/service ./internal/handler/admin ./internal/handler/dto -run 'TestExcelBPS(SchemaRepair|Search|AccountSearch|OnDemandCatalog|MessageImage|Extensions|CacheCreationAsInput)|TestManagedBPSConnectionBillsCacheCreationAsShownToClient|TestAccountOps|TestBPSSearchDefaults|TestFreeFastCustomerCost' -count=1 -timeout=10m
SUB2API_TEST_POSTGRES_DSN= SUB2API_TEST_REDIS_URL= SUB2API_TEST_REDIS_IMAGE=redis:8-alpine CI=true go test -tags integration ./internal/repository -run 'TestAccountOps|TestExcelBPSImagePolicyRealRedis|TestFreeFast' -count=1
SUB2API_TEST_POSTGRES_DSN= SUB2API_TEST_REDIS_URL= SUB2API_TEST_REDIS_IMAGE=redis:8-alpine CI=true go test -tags integration ./internal/repository -run '^TestQuality(BPSSnapshotEqualDefaultsAndManualChanges|EnableBPSRestoreAfterAccountEditorSave|BPSSearchRestorePreservesManualChanges)$' -count=1 -timeout=10m
go test ./migrations ./internal/pkg/errorpresentation -count=1
go build -tags embed -ldflags '-X main.Version=2.21.5.1' -o /tmp/sub2api-v2.21.5.1-merge-check ./cmd/server
```

前端验证命令（在 `frontend/` 执行）：

```sh
node node_modules/vitest/vitest.mjs run src/components/admin/operations/__tests__ src/views/admin/__tests__/AccountOpsView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts src/utils/__tests__/publicSettingsPresentation.spec.ts src/api/__tests__/auth-public-settings.spec.ts src/utils/__tests__/qualityRulePatch.spec.ts src/components/admin/usage/__tests__/UsageTable.spec.ts
node node_modules/vitest/vitest.mjs run src/components/account/__tests__/ExcelBPSSearchOptions.spec.ts src/composables/__tests__/useExcelBPSDefaults.spec.ts src/views/admin/__tests__/AccountOpsView.spec.ts --maxWorkers=2 --minWorkers=1
node node_modules/vue-tsc/bin/vue-tsc.js -b
node node_modules/vite/bin/vite.js build
node node_modules/eslint/bin/eslint.js src/views/admin/AccountOpsView.vue src/views/admin/__tests__/AccountOpsView.spec.ts src/i18n/locales/en/accountOps.ts src/i18n/locales/zh/accountOps.ts
```

服务层、管理 handler 和 DTO 的定向竞态检查最终全部通过；后端嵌入前端的正式构建通过，执行 `-version` 确认为 `2.21.5.1`。构建产物仅在临时目录，不安装或启动服务。

## 升级范围

累计新增 35 份上游 SQL 迁移：345–348、350–380；没有修改任何既有迁移。隔离数据库验证不代表生产数据副本升级演练。二进制回退不撤销迁移；供应商关联、多邮箱等新配置应遵守上游回退说明。

测试使用本地桩、临时 PostgreSQL/Redis 和虚构通知接收器，不调用真实付费模型，不发送真实邮件或 Webhook。真实搜索供应商兼容性、线上负载与部署验收不在本次范围内。
