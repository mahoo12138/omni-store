# 当前项目状态

## 已发布

```text
1.1.0 — File Experience
```

已经完成：

- Upload Task Manager；
- 文件夹上传；
- 相对路径上传；
- 多文件任务；
- 冲突策略；
- 批量移动/复制/删除/下载/分享；
- 外部拖拽上传；
- 内部拖拽移动；
- 最近文件；
- 收藏；
- 文件管理器移动端优化。

## 当前 main 已完成但不再单独规划为 1.2 发布目标

Preview & Sharing 已基本落地：

- 统一 Preview Resolver；
- Image / Text / Markdown / JSON / YAML / Code；
- PDF；
- Audio / Video + Range；
- unsupported -> download；
- 文件管理器预览；
- Share 文件预览；
- 目录 List / Gallery；
- 流式 ZIP；
- 二维码；
- 410 expired/exhausted 状态；
- Instance Branding。

这些功能是 2.0 基线，Agent 不得回退。

## 当前目标

```text
2.0.0
```

2.0 是允许破坏性调整的架构与产品整合版本。

核心目标：

1. Capability Scope 重构。
2. Site Capability 全局单一 Source。
3. Source Protocol 独立开关。
4. Operation Center。
5. Backup & Recovery。
6. Compatibility Matrix。
7. 删除旧多公开挂载和用户图床 Source 选择等历史模型。

## 版本兼容

历史已发布 migration：

```text
v1.0.0.sql
v1.1.0.sql
v1.1.1.sql
```

全部冻结。

当前代码已包含的 2.0 migration：

```text
v2.0.0.sql
```

不能修改历史 migration。迁移文件编号不等于产品发布版本；当前开发版本为 `2.0.0-dev`。2.0.1–2.0.4 的增量变更已在发布前合并进 `v2.0.0.sql`，不再保留独立文件；升级路径只保留 1.1.x → 2.0.0。

## 本次文档修订基线（2026-09-29）

核对 main 为 `af9a3859bedcd7736ced38ecb8da285a11e595c8`，提交日期 2026-09-28。该提交文档描述已有 relative_path 上传以及 Preview/Share 2.0 的开发中能力；不是本次 2.0 新服务已经实现的证据。

本次更新是对上一份 2.0 文档包的整合：统一 `.omnistore/`，新增 static_assets，快速图床与路径型博客资源分工，补齐缓存/跨域/权限/命名空间/重绑/验收。没有在此任务中修改或提交仓库代码，也没有运行产品测试。

上述为 2026-09-29 的文档整合记录；静态作用域已由下面的实现记录更新。其余 Transfer Send/Collect、运维、备份恢复和兼容性范围继续保留。

## 2026-10-04：静态 HTTP 协议重构

静态访问从 Site 单例改为每源独立的匿名只读 HTTP 协议。Site binding 仅保留公开盘、图床和流转中心。后台入口为「存储源 → 静态 HTTP」，各源独立发布、启停和设置缓存/CORS。管理 API 改为 `/api/v1/admin/sources/{key}/static-access/*`，公开 `/assets/{id}/{path}` 不变。

静态访问的 schema 变更（原 `v2.0.1.sql`，现并入 `v2.0.0.sql`）建立按源的发布配置：不自动发布任何源，不移动或删除普通文件。跨源重绑不覆盖已有目标配置，撤销配置须先关闭协议；源有静态配置时禁止删除。

验证：`go test ./...`；`go test -race ./internal/staticassets ./internal/http ./internal/db ./internal/sources ./internal/operations`；前端 48 项单元测试和生产构建；Chromium 8 项 E2E（静态协议与后台设置回归），覆盖 1440px/375px、多源同路径内容、普通上传后匿名读取、单源启停、根发布确认、持久化、版本冲突、旧配置迁移及系统备份恢复。浏览器使用独立临时数据和端口 18090，没有重启现有 18080 服务。未验证 Safari/Firefox、真实 CDN 或跨机器部署。

## 2026-10-14：访客中转、公开入口与站点配置

文件中转对未登录访客开放：访客可创建发件包与收集任务、上传文件并管理自己的收发记录。访客身份使用独立 Cookie，与普通登录会话分开，不创建普通用户、不授予文件管理、存储源或管理权限；身份有效期不超过 Web 会话期限与 7 天中的较小值，界面提供 1 天有效期，单个访客最多 20 个活跃任务、1 GiB 活跃载荷，单文件也不高于 1 GiB，实际剩余空间仍在写入时校验。登录用户的任务归账号所有；从网盘复制快照、保存提交到普通目录仍需真实登录与目录权限。

中转台账的 schema 变更（原 `v2.0.2.sql`/`v2.0.3.sql`，现并入 `v2.0.0.sql`）：`transfers`/`transfer_collections` 直接建为最终形态，`require_code` 默认 `1`；新增 `transfer_guest_identities`，`owner_user_id` 与 `owner_guest_id` 二选一且 guest 删除受限；访问会话与上传草稿按 publicKey 初始化。

Web 端新增公开入口 `/transfer`（发送文件/收集文件/收发记录三个 Tab，与 `/app/transfer` 共用同一界面，详情地址支持直接打开与刷新）、公共导航（公开网盘/图床/文件中转，登录态显示工作台入口）和后台「站点配置」页（站点服务绑定、实例信息、配置备份）。

验证：`go build ./...`；`go test ./...`（26 个包全部通过）；`go test -race ./internal/staticassets ./internal/http ./internal/db ./internal/sources ./internal/operations ./internal/transfers ./internal/backup ./internal/managedroot`；前端 58 项单元测试、`tsc -b` 与生产构建；Chromium E2E 66 项全部通过，其中新增公开导航（1440/320/375px）、公开中转入口（访客记录持久化、停用服务不影响已有链接、登出后不泄漏上一位所有者缓存）、站点配置（状态保存、失败保存与并发编辑恢复）与静态协议（1440/375px）。E2E 使用干净种子环境，完成后清理了本次创建的公开配置。未验证 Safari/Firefox、真实客户端矩阵与跨机器部署。

## 2026-10-18：2.0 恢复一致性与浏览器兼容加固

基于 `bac9423` 继续开发，成果已提交在分支 `codex/2.0-release-hardening`。本轮不是已发布版本，也不代表整个 2.0 已达到 RC 条件。

- Collect 在写文件前持久化批次日志；空提交占位行不再被当成已提交依据。进程中断后，未提交批次回滚，已提交文件保留，启动时清除无文件的占位行。
- Send 记录精确临时路径，恢复校验源、任务、文件路径与托管根归属；失败保留日志。上传、定稿与草稿 GC 协调，重复同路径上传不能破坏已获胜请求的文件。
- 已完成删除的恢复只移除日志，避免删除同名新内容；未完成删除阻止托管路径复用。GC 保留带待恢复日志的任务，不再按名称猜测删除无归属证据的文件或目录。
- Collect 关闭仅停止接收；既有内容持续可下载、保存，仍计入站点/访客容量与源删除保护，直到原定过期时间再回收。无过期时间的关闭任务保留内容。
- 新增真实第二 origin 浏览器检查，覆盖图片解码、Canvas、CORS、WebM 播放/seek、非法类型及服务启停；统一浏览器门禁改用新临时种子与动态端口，失败保留证据。
- crashcheck 增加 Send 上传/定稿、Collect 提交、Save to Files、GC 五条真实 SIGKILL 路径，连同既有九条共十四条。
- 下载名额按（访问会话, 下载目标）去重（`v2.0.0`）：同一文件的 Range 续传、失败重试与并发重复请求只计一次，不同目标或重新解锁的新会话仍各自计数；声明与条件 UPDATE 同事务，名额用完时回滚声明。

迁移与 API：`v2.0.0.sql` 增加 `transfer_download_claims`，不新增独立迁移。对外路由和响应结构保持兼容；收件箱隐藏未完成占位提交，关闭任务的载荷统计包含保留文件。旧上传日志仍可读取；旧版未记录路径的临时文件保留供人工核查，不承诺自动识别删除。

开发门禁：Node 24.19.0 / Go 1.27.0 下，`OMNISTORE_ALLOW_DIRTY=1 ./scripts/verify-release.sh` 完整运行成功。前端 58 项单元测试、TypeScript 与生产构建；Go 格式检查、vet、全量非缓存测试（总语句覆盖率 71.7%，全部关键包门槛通过）及全量非缓存 race；全新临时实例 Chromium E2E 67/67；嵌入前端的发布二进制、Linux amd64/arm64 交叉构建、Compose 配置与 diff 检查均通过。Compose 配置校验不代表已运行容器；本轮未发布镜像或标签。

恢复验收：`OMNISTORE_CRASH_ROUNDS=10 ./scripts/verify-crash-recovery.sh` 完整运行成功，十四条路径各十轮，共 140 次真实 SIGKILL。验证文件字节、数据库台账、重复恢复/重试与保存到普通目录的副本；不把一次进程能重启当作数据一致性通过。

下一阶段仍需：完成真实 WebDAV/S3/PicGo 客户端矩阵、MP4 与其他浏览器/生产代理验收、GT-08 实际访客配额压测，以及 Transfer 预览的目标闭环。逐项结果见 [兼容矩阵](design/compatibility-testing.md)；不能以 API 自编请求替代真实客户端通过结论。

## 2026-10-31：未发布的文件中转功能整块移除

未发布的文件中转（Transfer Center）功能已整块移除：前端入口与页面、后端 API 与 `internal/transfers`、访客身份与数据库表、`transfer_center` 能力绑定、运维看板与测试一并删除，`v2.0.0.sql` 中的相关表、能力行与设置 key 同步移除；该功能从未发布，不需要删除迁移。旧链接直接 404。

验证：后端 `go test ./...` 全量通过，旧入口 HTTP 404 有单测覆盖；前端 `tsc -b`、97 项单元测试与生产构建通过；全新临时实例 Chromium E2E 59/59 通过。

## 2026-11-01：2.0 升级迁移语义修正

`v2.0.0.sql` 的存量升级行为与迁移设计、FAQ 和发布规范对齐：

- 存量源迁移保留 1.x 的 S3 可达性（`s3_enabled=1`），fresh 2.0 源仍默认关闭；管理员可逐源关闭。
- 公开盘与图床只在恰好一个 legacy 候选时自动绑定；零个或多个保持 disabled/unbound，不再按排序、匿名开关或用户偏好猜测目标。
- 新增 `TestMigrateV2BindingRules` 覆盖 0/1/多候选，并断言存量源 S3 保持、fresh 源默认关闭；补齐 3 个 Go 文件的 gofmt。

验证：`gofmt -l cmd internal migrations` 无输出；`go vet ./...`；`go test -count=1 ./...`；`go test -race -count=1 ./internal/db`；`git diff --check` 均通过；全新临时实例 Chromium E2E 59/59 通过。已有本地 2.0.0-dev 库不会重放已记录的迁移，需要重建测试数据才能验证新规则。未重跑完整 `verify-release.sh`/crashcheck，未发布镜像或标签。

## 2026-11-01：2.0.0-rc.1 发布候选

- 候选代码提交 `4489abb`；本条目所在提交仅追加发布文档。
- 干净工作区以 `OMNISTORE_RELEASE_VERSION=2.0.0-rc.1` 完整通过 `./scripts/verify-release.sh`：前端 97 项单元测试、`tsc -b` 与生产构建；Go 格式、vet、全量非缓存测试（总覆盖率 70.9%，全部关键包过线）与全量 race；全新临时实例 Chromium E2E 59/59；嵌入前端的发布二进制、Linux amd64/arm64 交叉构建、Compose 配置与 diff 检查。
- `OMNISTORE_CRASH_ROUNDS=10 ./scripts/verify-crash-recovery.sh` 通过：9 条场景共 90 次真实 SIGKILL。
- 真实客户端矩阵仍未执行：Windows Explorer/Finder/rclone/Cyberduck/WinSCP/RaiDrive/Mountain Duck、AWS CLI/rclone S3/s3cmd/MinIO Client、PicGo 均为 not-tested；MP4、Safari/Firefox 与生产反向代理同样未验收。这些仍是 2.0.0 正式发布前的剩余门禁，本次 RC 不声称已完成。
- 本地创建标签 `v2.0.0-rc.1`；未推送、未发布镜像或 GitHub Release。
- 标签 `v2.0.0-rc.1` 随后已推送；GitHub Actions 质量门禁因 Linux E2E/测试环境问题失败，未产出镜像或 Release，由 `v2.0.0-rc.2` 取代；已发布标签未移动。

## 2026-11-01：2.0.0-rc.2 发布

- 候选提交 `010fb3c`；GitHub Actions 质量门禁、Docker 镜像、Linux amd64/arm64 二进制与 GitHub Release 全部成功。
- Release: https://github.com/Mahoo12138/omni-store/releases/tag/v2.0.0-rc.2（pre-release）；资产为 `checksums.txt`、两个 Linux tar.gz，下载后 `shasum -a 256 -c` 通过。
- 修复的 CI 环境问题：E2E 截图改用跨平台临时目录；备份 E2E 用 Node 校验和/realpath 并安装 `unzip`/`zip`；回收站导航与通知遮挡等待；lifecycle 测试 join goroutine；文件操作用例超时调整。
- 真实客户端实测结果与剩余 GA 缺口见 [兼容矩阵](design/compatibility-testing.md)：S3 `GetBucketLocation`、ListObjects V1、Windows 客户端、MP4/Safari/Firefox/生产代理仍未完成。

## 2026-11-07：2.0.0 正式版发布

- 候选代码提交 `a7f6057`（含 rc.2 门禁已验证的 13 个业务流 E2E spec 入库）；本条目所在提交仅定版 CHANGELOG 与发布文档。
- 干净工作区以 `OMNISTORE_RELEASE_VERSION=2.0.0` 完整通过 `./scripts/verify-release.sh`：前端 97 项单元测试与生产构建；Go 格式、vet、全量非缓存测试（总覆盖率 70.9%，全部关键包过线）与全量 race；全新临时实例 Chromium E2E 84/84；发布二进制、Linux amd64/arm64 交叉构建、Compose 配置与 diff 检查。
- `OMNISTORE_CRASH_ROUNDS=10 ./scripts/verify-crash-recovery.sh` 通过：9 条场景共 90 次真实 SIGKILL。
- 真实客户端矩阵剩余缺口同 [兼容矩阵](design/compatibility-testing.md)：Windows Explorer/Finder/rclone/Cyberduck/WinSCP/RaiDrive/Mountain Duck、AWS CLI/rclone S3/s3cmd/MinIO Client、PicGo，以及 MP4、Safari/Firefox 与生产反向代理仍未实测；Chromium 基础场景已于 2026-10-18 记录通过。本条目如实记录缺口，不声称已完成。
- 随后推送 `main` 与标签 `v2.0.0`，由 GitHub Actions 完成质量门禁、Docker 镜像、Linux 二进制与 GitHub Release 发布。

## 2026-11-08：2.0.0 正式版发布

- 发布提交 `b0a9ba5`，包含两个 Linux CI E2E 稳定性修复：policy 用例改为在种子策略上验证规则 CRUD（权限引擎跨策略取最高权限，另一条策略的只读规则压不过种子策略的源级读写）；paste-conflict 用例的回收站清理改走 API（UI 清理在慢环境偶发超时）。
- 标签 `v2.0.0` 曾两次随门禁失败重打（682f7c0 门禁失败于 policy 用例 → f254d3d 修复；重打后失败于 file-operations 清理 → b0a9ba5 修复；第三次全绿）。两次失败均未产出镜像、二进制或 Release，标签移动不涉及任何已发布产物。
- GitHub Actions 质量门禁、Docker 镜像（ghcr.io，正式版附带 latest 标签）、Linux amd64/arm64 二进制与 GitHub Release 全部成功。
- Release: https://github.com/mahoo12138/omni-store/releases/tag/v2.0.0；资产为 `checksums.txt` 与两个 Linux tar.gz。
- 真实客户端矩阵剩余缺口同 [兼容矩阵](design/compatibility-testing.md)：Windows Explorer/Finder/rclone/Cyberduck/WinSCP/RaiDrive/Mountain Duck、AWS CLI/rclone S3/s3cmd/MinIO Client、PicGo，以及 MP4、Safari/Firefox 与生产反向代理仍未实测。
- 后续 2.1 功能（OIDC 单点登录）已在 main 开发，未随 2.0.0 发布。
