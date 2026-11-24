# 系统架构

> 修订：2026-09-29 / R2。2.0 目标规范；未标为基线的内容不代表已实现。

## 1. 长期技术模型

```text
Go modular monolith
+ SQLite
+ Local filesystem
+ embedded React
```

不增加 Redis、PostgreSQL、消息队列、微服务或分布式锁。

## 2. Storage Source

```text
Storage Source = Local Real Directory
```

每个 Source：

- internal integer ID；
- immutable random `storage_key`；
- name；
- root path；
- quota；
- exclude；
- disabled；
- WebDAV switch；
- S3 switch；
- 静态 HTTP 配置（匿名只读、发布根、公开 ID、缓存/CORS）。

`storage_key` 是外部稳定协议 ID。

示例：

```text
src-a4d83f29c701be42
```

满足 S3 Path-style bucket 字符要求，也可安全作为 WebDAV path segment。

Source 改名不能改变 key。

## 3. Capability Scope

```text
OmniStore
├── Site Capabilities
│   ├── Public Drive -> Source A
│   └── Image Bed -> Source B
│
├── Source Protocols
│   ├── Source A: WebDAV / S3 / Static HTTP + publish_root
│   └── Source B: WebDAV / S3 / Static HTTP + publish_root
│
└── Resource Capabilities
    ├── Preview
    ├── Share
    ├── Copy
    ├── Move
    └── Trash
```

一个 Source 可以同时承担多个 Site Capability。

## 4. Service-managed Namespace

Site Capability 需要在绑定 Source 内保存内部数据时使用：

```text
.omnistore/
```

建议：

```text
.omnistore/
└── image-bed/
```

规则：

- 用户不能通过普通文件 API 创建/读取该 namespace；
- File Manager 隐藏，直接读取/浏览返回 404；
- Public Drive 隐藏；
- WebDAV 隐藏/拒绝；
- S3 隐藏/拒绝；
- Search 不索引；
- Generic Reconcile 不把它当普通用户文件；
- Source 物理 quota 必须统计这些真实字节；
- 功能模块自己维护业务记录；
- 不能按文件名猜测所有权并删除。

图床初始化托管根时，发现未知 `.omnistore/` 必须阻止接管并提示人工处理。公开盘/静态资源不创建托管根，只隔离并告警，不因普通发布目录旁存在未知内部目录就擅自改动它。完整规则见 [托管命名空间](design/managed-namespace.md)。

## 5. Site Capability Binding

SQLite 保存：

```text
capability
enabled
storage_source_id
updated_at
```

Capability：

```text
public_drive
image_bed
```

删除 Source 前必须解除当前绑定，并处理所有历史图床、回收站与未完成恢复日志引用；仅 Disable 不清除外键关系，也不授权删除。

Rebind 语义：

- Public Drive：管理员确认后切换当前公开根。
- Image Bed：只影响新上传；旧 images 继续按记录中的 source 读取。

静态 HTTP 在源协议配置中独立维护。source/publish_root 变更必须走 reset/relocate，不允许静默让旧 URL 指向另一批内容；跨源迁移不能覆盖目标源已有静态配置。

## 6. Protocol Layer

### WebDAV

按 Source 独立开启。

请求必须同时满足：

- Source enabled；
- `webdav_enabled=true`；
- credential valid；
- user Policy；
- path/exclude/symlink；
- quota；
- lock。

### S3

S3 listener 可以仍由实例配置决定是否监听。

Bucket：

```text
bucket = storage_key
```

实际对象路径：

```text
/{storage_key}/{object_key}
```

只有 `s3_enabled=true` 且用户有权限的 Source 才进入 ListBuckets/对象操作。

## 7. File Write Pipeline

通用 durable write（两阶段无锁落盘 + 短临界区提交）：

```text
Auth / Capability
→ Normalize / Reserved namespace / Exclude
→ Optimistic quota bound (Phase 0, unlocked)
→ staging planned journal → temp write + fsync (Phase 1, unlocked)
→ Persistent lock → Quota lock → Path lock (Phase 2, short critical section)
→ os.Root symlink safety + conflict/quota re-verify
→ prepared journal → atomic publish (Rename)
→ DB transaction (+ source_usage delta + recent/favorite rebase)
→ database-ready → cleanup
```

下载链路在 `os.Root` 安全打开 fd 并 `Stat` 核验后立即释放路径读锁，响应流期间不持有路径锁。

## 8. Backup

System backup：

- DB；
- config；
- keys；
- capability bindings。

不打包：

- Storage Source 内容；
- cache/tmp。

Restore 后通过 Source Rebind 重新建立 root path。

## 9. 静态发布不是托管存储

`internal/staticassets`（建议模块名）负责发布配置、范围解析、公开媒体读取和发布身份，不负责创建第二套文件树。图片/视频原件仍在普通目录；写入由现有 files / WebDAV / S3 管线完成。

读取路径：发布快照 → 普通文件安全读取 → 公开 HTTP 响应。其访问授权不同于登录下载，但路径边界、文件锁、类型隔离必须共用；不能直接暴露 `http.FileServer(Source.root)`。

一个源同时绑定两项 Site 服务并开启静态 HTTP 时，公开盘与静态资源只读取普通文件；图床经自己的记录访问内部内容。普通协议开启也不能绕过 `.omnistore/`。Source 计量覆盖所有实际字节，静态发布不再收取一份“托管副本”配额。

## 10. Reconcile 的作用域

`file_records.resource_scope` 分为 `file` 和 `image_bed`。普通扫描只校准 file scope；不把内部图片行删除为 missing。普通搜索 FTS 仅索引 file scope active 行；查询时仍做路径过滤。图片历史和用量使用专用作用域，不能依赖普通文件搜索。内部作用域只能由受信任 Service 写入。

## 11. 相关目标契约

- [能力与文件管理归属](design/capability-scope.md)
- [静态发布](design/static-assets.md)
- [路径隔离](design/managed-namespace.md)
- [公开 HTTP](design/public-content-http.md)

## 12. 并发、存储与装配基线（2026-11-24）

- **HTTP 超时与零拷贝**：主 HTTP Server 配置 `ReadHeaderTimeout`（10s）与 `IdleTimeout`（2m）；长传/下载通过 `http.ResponseController` 按进度滚动设置读写截止时间；`statusRecorder` 实现 `Unwrap()` 与 `io.ReaderFrom` 透传，确保 `http.ServeContent` 保留 `sendfile` 零拷贝且 `WithRecover` 放行 `http.ErrAbortHandler`。
- **SQLite 读写分池与热路径节流**：`internal/db` 提供 `db.Pool`（写单连接 + WAL 并发只读池 `mode=ro&query_only(1)`）；Session `last_seen_at`、Token/S3 `last_used_at`、`recent_files` 写入均按 ≥60s 节流去重；`Reconcile` 在只读池读取快照并以 500 行/批提交事务。
- **增量配额计量**：`v2.1.0` 新增 `source_usage` 表按写事务原子维护源物理字节增量，消除上传与查询热路径的全量 `WalkDir`；后台每 15 分钟无锁全源校准以自愈外部直接写盘带来的漂移。
- **锁管理器与路径隔离**：`internal/locks.Manager` 采用每等待者独立 channel 替代 `sync.Cond.Broadcast` 并支持 `context.Context` 取消；`internal/sources` 缓存每个存储源的 `*security.SourceRoot`（`*os.Root`）。
- **应用装配与恢复原语**：`internal/app` 统一完成依赖装配、8 套 `Recoverer` 启动恢复编排及 `context` + `WaitGroup` 管控的后台任务；`internal/lifecycle.Manager` 支持实例级注入；`internal/journal` 沉淀原子落盘、严格反序列化、幂等阶段标记与孤儿清理原语；`internal/http` 不再包含业务 SQL。
