# 对外错误展示：第一批

新增纯错误映射包、独立 BPS 适配器与最终准入/限流适配器。普通 JSON、图片错误与 Excel OAuth 路由错误默认只公开 type/code/message，移除 param 等内部字段。原错误继续供内部处理，新增限长并脱敏的诊断日志。SSE、compact 已提交响应和失败终态由下一批接入。

## 配置与回退

`SUB2API_BPS_ERROR_PRESENTATION` 未设置、空值或 `neutral` 时默认使用中性提示；非法值回退中性并记录一次警告。设置 `legacy` 并重启后端可恢复本批接入前的展示；容器需显式传入该环境变量。无数据库迁移，与图片预算、Schema 纠错、按需工具目录开关独立。

仅修改输出投影，保留原 HTTP 状态、内部返回错误、用量、请求次数和调度策略。不按请求级“曾走过 BPS”标志影响其他渠道。

## 验证

本批原始验证：service 1075、handler 32 个通过项；新增路径 race：service 24、handler 4；共享映射包及 race 通过。均含子测试，使用本地模拟上游。

```sh
cd backend
go test ./internal/pkg/errorpresentation -count=1
go test -tags unit ./internal/service ./internal/handler -run 'ExcelBPS|ExcelOAuth|PublicError' -count=1
go test -race -tags unit ./internal/service ./internal/handler -run 'TestExcelBPSPublic' -count=1
```

旧文案断言显式设置 legacy；默认中性契约由独立测试覆盖。不修改桥接器或 DTO。
