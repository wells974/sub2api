# 对外错误展示：第一、二批

普通错误出口沿用第一批。第二批新增 SSE error/response.failed/cancelled/incomplete、流提前结束、非流式失败终态以及 compact 心跳后的中性展示。先解析原事件进行用量与 Ops 处理，再修改输出。保留状态、ID、sequence、output、usage；保留错误对象原有数字 status/status_code。

共享 handler/compact writer 增加可选展示回调，其他渠道不传回调则保持原行为。不改变路由；Chat Completions/Anthropic 转发链路未新增 BPS 支持。

## 配置与回退

`SUB2API_BPS_ERROR_PRESENTATION` 未设置、空值或 `neutral` 时默认使用中性提示；非法值回退中性并记录一次警告。设置 `legacy` 并重启后端可恢复本批接入前的展示；容器需显式传入该环境变量。无数据库迁移，与图片预算、Schema 纠错、按需工具目录开关独立。

仅修改输出投影，保留原 HTTP 状态、内部返回错误、用量、请求次数和调度策略。不按请求级“曾走过 BPS”标志影响其他渠道。

## 验证

本批原始扩展回归：service 1382、handler 113 个通过项；最终新增路径 race：service 47、handler 13；共享映射包通过。均含子测试，本地模拟验证。覆盖限流、Schema 拦截、取消、不完整终态、EOF、compact 心跳、成功帧、大整数用量与内部诊断对照。

```sh
cd backend
go test -tags unit ./internal/service ./internal/handler -run 'ExcelBPS|ExcelOAuth|PublicError|Compact|StreamingAware' -count=1
go test -race -tags unit ./internal/service ./internal/handler -run 'TestExcelBPSPublic' -count=1
```

普通用户历史错误记录及其他公开配置由第三批接入。
