# SWS Vault：独立保险库项目规划 v1.0（OPC 10-14 23:21：单独立项，不放进 Hub）

取代 VAULT-PLAN v0.3 中「放进 Hub」的部分；原则和对外调用的要求不变。

## 0. 原则（照搬 Hark 的做法）
1. 只写不读：没有任何取明文的接口，只给预览（类型、标签、末 4 位或主机名、指纹）。
2. 按 id 调用：使用方只拿 `vault:<ns>/<id>` 引用，由保险库在最后一步注入。
3. 绑定使用范围：主机白名单、header 模板、允许的 client；越界一律拒绝。
4. 录入不经过聊天：一次性安全录入链接，或由保险库自己生成，或从命令输出直接捕获。
5. 每次使用都审计，审计里只有 id，没有值。
6. 不依赖单一账号：主密钥由 OPC 离线托管，可以分片。

## 1. 独立性（与 Hub 完全解耦）
- 独立仓库（建议 kyaring/sws-vault）、独立 Go 单二进制、独立 SQLite、独立端口（建议内网 9531）、独立域名（建议 vault.king.nyc.mn 经 Cloudflare 反代）、独立 systemd 服务和备份。
- 主密钥文件 /etc/sws-vault/master.key（600），与 hub.env 无关。
- 自带 admin 引导：首次启动时生成一次性 bootstrap 链接，由 OPC 建 admin client。
- Hub 以后只是一个普通 client（ns=vps-commander），出问题时两边互不影响。

## 2. 存
- 表：namespaces、clients（只存令牌哈希，可设来源限制和过期）、secrets（ciphertext、dek_wrapped、key_version、preview、fingerprint、bindings、allowed_clients、时间戳）、intents、audit。
- 信封加密：每条 secret 一把 DEK（AES-256-GCM，AAD 绑定 ns、id、type），DEK 由 KEK 包裹。KEK 轮换时只重新包裹 DEK。
- secret 类型：login、api_key、token、webhook、ssh_key、note。

## 3. 录
- 安全录入链接：`POST /v1/intents` 返回一次性链接（10 分钟、只能用一次），OPC 在网页填写。
- 生成：`POST /v1/generate`，保险库自己生成，只返回 id 和预览。
- 捕获：`swsvault capture --ns X --label Y < 命令输出`，值只走 stdin。

## 4. 调（对外，供其他项目使用）
- 出站注入 `POST /v1/fetch`：调用方给出目标 URL 和 `vault:` 引用，保险库代发请求，只在目标主机命中白名单时注入请求头，响应里的值会被脱敏。这是外部项目「用而不见」的主要方式。
- 节点注入 `swsvault run --env NAME=vault:ns/id -- <cmd>`：节点上的包装器用节点 client 令牌换取一次性解密，只注入子进程环境变量，输出里的值会被脱敏。
- 只给预览的接口：`GET /v1/secrets`（只有元数据）。
- 三种接入方式：REST `/v1`、MCP 端点（vault_list、vault_fetch、vault_intent）、CLI `swsvault`。
- 防滥用：按 client 限流；namespace 隔离；管理接口（建 ns、建 client、轮换）只允许内网加 admin 调用。

## 5. 拆卡（S 系列，开发者 GPT 和 RikkaHub-1，各卡改动的文件不重叠）
- S-0 建仓与骨架：仓库、CI 本地验收脚本、check-task、卡格式沿用 VCHub。需 OPC 批准建 GitHub 仓库。
- S-1 存储与信封加密、KEK 加载与轮换（GPT）
- S-2 REST、client 鉴权、namespace、限流、审计（GPT，依赖 S-1）
- S-3 出站注入 fetch 和脱敏（依赖 S-2）
- S-4 Web：安全录入页和只读管理页（RikkaHub-1，可以和 S-2、S-3 并行，按接口规格开发）
- S-5 MCP 端点和 CLI，含 run、capture（依赖 S-2）
- S-6 运维部署：systemd、反代、备份，需 OPC 批准
- S-7 Hub 接入：Hub 通知通道改用 vault 引用（在 VCHub 仓库开卡）
- S-8 主密钥离线托管和恢复演练（OPC 参与）

## 6. 待 OPC 定
- 仓库名和部署节点（建议 kyaring/sws-vault，部署在 wjy5010u）。
- 域名（建议 vault.king.nyc.mn）。
