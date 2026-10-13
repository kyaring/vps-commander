# VCHub 自有保险库规划 v0.1（OPC 06:16 提出：不能过分依赖单一账号，后续再做）

## 目标
- 敏感值（webhook、bot token、API key、主密钥备份）统一由 Hub 自己的保险库保管，按 secret_ref 引用，不依赖任何个人账号或外部 SaaS 账号。
- 只写不读：明文写进去以后，任何 API、面板、日志都不再返回明文，只显示指纹（sha256 前 8 位）或末 4 位。
- 每一次访问都可审计，并且能追溯到具体员工（工号）。

## 设计要点
1. 存储：新增 vault_secrets 表，字段为 id、name、type、scope、ciphertext、key_version、fingerprint、created_by、created_at、rotated_at、disabled_at。加密方式为 AES-256-GCM，AAD 绑定 id 和 type。
2. 主密钥：现在的 VPS_COMMANDER_NOTIFICATION_KEY 升级为 VPS_COMMANDER_MASTER_KEY（旧变量名继续兼容），带 key_version，支持轮换：新 key 生效后后台重新加密全部密文，旧 key 留一个版本作回退。
3. 主密钥托管（不放在单一账号里）：
   - 离线副本由 OPC 亲自保管，可以是纸质或加密 U 盘；
   - 可选 Shamir 2-of-3 分片，三份分开存放；
   - 可选异地加密备份，和数据库备份分开存。
4. API（仅 admin）：创建、更新（覆盖写）、轮换、停用、列表（只返回元数据）。不提供明文读取接口；Hub 内部按 secret_ref 解密后使用。
5. 引用：通知通道的 secrets 改为 secret_ref；以后 DevTask 的模型 key 等也走同一套机制。
6. 审计：创建、更新、轮换、停用和每次内部解密使用都写审计，记录员工快照和调用来源，不记任何值。
7. 面板：新增保险库页面，字段只写不读，显示指纹、类型、引用方和最近使用时间。
8. 备份与演练：数据库和主密钥分开备份；每季度做一次恢复演练，在临时目录用离线 key 解密抽检。

## 拆卡（顺序待 OPC 定）
- V-A 设计定稿（Hark，docs/org 或 docs/design）
- V-B 存储、加密、轮换（开发，GPT）
- V-C API 和审计（开发，依赖员工表 B 的员工快照）
- V-D 面板保险库页面（开发）
- V-E 运维迁移：通道 secrets 改为 secret_ref；当前的系统群 webhook 也录进 VCHub 保险库
- V-F 运维：主密钥离线托管加恢复演练（OPC 亲自参与）

## 依赖
T-14 完成之后开始；V-C 依赖员工表 B。主密钥离线托管（V-F 的一部分）可以提前做：OPC 现在就可以把 hub.env 里的 key 抄到离线介质上。
