# VCHub 自有保险库 v0.3（OPC 10-14 23:11 定为主线；23:14 要求能对外开放，供其他项目调用）

## 0. 原则（照搬 Hark 的做法）
1. 明文只进不出：录入以后，任何 API、面板、日志、exec 输出都拿不到明文，只给安全预览（类型、标签、末 4 位或主机名、指纹）。
2. 按 id 调用：使用方只拿 `vault:<id>` 引用，由 Hub 在最后一步把值注入，调用方（agent、员工、面板）看不到值。
3. 绑定使用范围：每条 secret 都绑定允许的用途，包括主机白名单、通道、命令，以及哪些员工可用；越界使用直接拒绝。
4. 录入不经过聊天：用一次性安全录入链接、Hub 生成，或从节点命令输出直接捕获，三种方式都不经过 agent 的上下文。
5. 每次使用都写审计（员工、用途、目标），审计里只有 id，没有值。
6. 不依赖单一账号：主密钥由 OPC 离线托管，可以分片；和数据库备份分开存。

## 1. 存（数据模型）
- vault_secrets 表：id（vs_xxx）、label、type（login / api_key / token / webhook / ssh_key / note）、preview、fingerprint、ciphertext、dek_wrapped、key_version、bindings_json、allowed_principals、created_by、created_at、rotated_at、last_used_at、disabled_at。
- 信封加密：每条 secret 一把随机 DEK（AES-256-GCM，AAD 绑定 id 和 type）；DEK 由主密钥 KEK 包裹。KEK 取自 hub.env 的 VPS_COMMANDER_MASTER_KEY，没有时回落到现在的 NOTIFICATION_KEY（作为 v1）。轮换 KEK 只需重新包裹 DEK。
- login 类型按 {username, password} 结构存，预览只显示用户名打码后的样子。

## 2. 录（三种入口，均为只写）
- 安全录入链接：admin 发起 `POST /api/v1/vault/intents`，Hub 返回一次性链接（10 分钟、只能用一次）；OPC 用手机打开 Hub 页面填写。agent 只拿到 intent id 和最后生成的 secret id。
- 生成：`POST /api/v1/vault/generate`（type、长度、字符集），Hub 自己生成、自己存，只返回 id 和预览。用于建账号、新令牌。
- 捕获：exec 加一个 `capture_to_vault` 参数，命令输出不回给调用方，直接存成 secret，只返回 id。用于节点上已有的 key，比如把 hub.env 里的 key 录进去，全程不经过聊天。

## 3. 调（三种注入方式）
- 内部引用：通知通道、DevTask 模型 key 等配置里写 `secret_ref: vault:<id>`，Hub 在内存中解密使用。
- 出站注入（对应 Hark 的按主机注入）：secret 绑定 {host 白名单, header 模板，如 `Authorization: Bearer {{v}}`}。agent 经 `POST /api/v1/vault/fetch`（或 MCP 工具 vault_http）发出请求，Hub 只在目标主机命中白名单时注入头，响应里出现该值就脱敏。
- exec 注入：exec_command 加 `env_from_vault: {NAME: vault:<id>}`，只注入进程环境变量；stdout/stderr 里命中值的部分替换成 [REDACTED]；审计 command 字段不含值。

## 4. 权限
- 新增 scope：vault:write（录入、生成、捕获）、vault:use（按绑定调用）、vault:admin（停用、轮换、删除、改绑定）。list 只返回元数据和预览。
- allowed_principals 按员工工号控制（员工表落地前先按令牌名）。

## 4.5 对外服务（OPC 23:14：供其他项目调用）
- 命名空间：每个外部项目一个 namespace（ns），secret 归属于 ns；跨 ns 一律不可见。
- 客户端：一个 vault_clients 表（client_id、ns、scopes、允许来源 IP/主机、过期时间），只存凭据哈希。外部项目用自己的 client 令牌调用，不复用 Hub 员工令牌。
- 稳定接口 /vault/v1/*（版本化），与 Hub 内部 API 解耦：
  - 存：intents（安全录入链接）、generate、list（只有元数据）、disable、rotate。
  - 调：fetch（出站注入，按主机绑定）、resolve-for-exec（只对已登记的 Hub 节点有效）。没有明文读接口。
  - 对外调用方式：REST、MCP 工具（vault_list、vault_http、vault_intent）和一个小 CLI（vcvault，值只从 stdin 进）。
- 出站注入因此是第一期必做：这是外部项目「用而不见」的唯一方式。
- 防滥用：按 client 限流；fetch 目标主机必须在 secret 绑定的白名单里；响应里命中值的部分脱敏；每次调用审计到 client 和 ns。
- 公网暴露：沿用 vcs.king.nyc.mn 的反代，路径为 /vault/v1；管理类接口（建 client、建 ns）只允许内网加 admin。

## 5. 拆卡（两位开发：GPT、RikkaHub-1，各卡改动的文件不重叠）
- V-B 存储与加密：新包 internal/vault/ 加 storage 表（secrets、namespaces、clients）和信封加密；KEK 加载与回落；轮换。
- V-C 对外 REST 与权限：/vault/v1/*、client 令牌鉴权、ns 隔离、scope、限流、审计。依赖 V-B。
- V-D 注入：vault_http 出站注入、exec 的 env_from_vault 和 capture_to_vault、输出脱敏、对应的 MCP 工具。依赖 V-C。
- V-E 面板：保险库页面（只写不读）和安全录入页。可以和 V-D 并行。
- V-F 迁移（运维）：通知通道的 secrets 改为 vault 引用；系统群 webhook 用「捕获」方式录入。
- V-G 托管（运维加 OPC）：主密钥离线托管，分片可选，做一次恢复演练。

## 6. 待 OPC 定
- 第一批要接入的外部项目，以及它们在哪里运行（公网还是内网）。
- 分工：建议 V-B 和 V-C 给 GPT，V-E 给 RikkaHub-1，V-D 等 V-C 完成后再派。
