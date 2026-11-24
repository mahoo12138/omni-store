# 代码设计与实现 Review

> 修订：2026-11-24 / R2。2026-11-24 R1 评审中 P0/P1、P2 及前端各项问题已全部核对并完成修复，相关架构与安全契约已同步至 [TASKS.md](TASKS.md)、[SECURITY.md](SECURITY.md) 与 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 后续可选演进项（非阻塞）

以下条目属于长期可选增强，暂不阻塞当前版本发布：

1. **Journal 通用阶段状态机（3.4 远期演进）**：当前已抽离 `internal/journal` 共享原语（原子独占落盘、严格反序列化、幂等标记与孤儿清理）并统一修复了各套恢复日志的边角不一致，8 套日志保持现有磁盘布局与 `cmd/crashcheck` 100% 兼容。若未来新增更多跨磁盘/数据库复合操作，可在 `internal/journal` 上进一步演进通用阶段机接口。
2. **S3 `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` 分块流式签名**：已在 [S3 特性文档](features/s3.md) 明确标注当前返回 `501 NotImplemented` 的限制；如后续需要支持默认启用明文 HTTP 流式签名的 SDK，可按 AWS Chunked Encoding 规范补齐解码与逐块签名校验。
3. **Unicode 路径 NFC/NFD 归一化策略**：当前与底层 POSIX 文件系统字节语义保持一致（详见 [SECURITY.md §10](SECURITY.md)）；若后续明确将 macOS 共享挂载作为生产宿主环境，可再评估是否对锁键增加 Unicode 规范化。
