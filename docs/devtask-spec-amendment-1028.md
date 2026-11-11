# 规格修订：DevTask 模型 URL 与 API Key（方案 B1）

- 修订对象：docs/recovery/salvage/devtask/devtask-v1.1.36-taskbook.txt
- 批准：OPC 于 2026-10-28 批准方案 B1，并批准主密钥使用 hub.env 中的 VPS_COMMANDER_PROVIDER_KEY。
- 审核：本修订的安全边界已于 2026-11-11 由 OPC 复核并接受。

## 修订内容
1. 第 39 行原文“网关 key 和长任务都不进 Hub 进程”改为：
   “模型提供商的 API URL 与 API Key 由 Hub 加密保存（AES-256-GCM，主密钥来自 hub.env 的 VPS_COMMANDER_PROVIDER_KEY），只在 runner 领取任务时按需下发，key 不写入 runner 磁盘。长任务仍由 runner 运行，Hub 不运行任务。”
2. 第 117 行原文“配置 /etc/devtask/runner.env……网关地址、网关 key、Hub 地址、runner token”改为：
   “runner.env 配置 Hub 地址（必须为 https://，回环地址除外且需显式开关）与 runner token；模型提供商的 URL 与 key 不再写入 runner.env。”
3. 模型名仍来自任务；白名单、不切换、不探测的规则（第 45、68 行）保持不变。
4. 空白名单语义：`devtask.allowed_models` 为空或未设置时**拒绝一切模型**（fail-closed），不是「不限制」；生产环境必须显式配置；修改白名单只允许管理员，每次修改写审计（旧值、新值、操作人）。

## 复核结论与硬性前提
本方案安全边界已于 2026-11-11 由 OPC 复核并接受，下发与审计须满足以下三项硬性前提：
- **前提 1**：取凭据接口必须同时校验四项——`lease_id` 有效且未过期、模型在白名单内、provider 已启用、调用方为 runner 命名令牌；四项全满足才下发，任一不满足即拒绝。
- **前提 2**：审计只记录 `provider_id`、`model`、`lease_id`、结果，**绝不记录 key 值**（含截断、哈希、前缀）。
- **前提 3**：`X-Forwarded-Proto` 仅作辅助防护，不得作为唯一来源判定；局域网客户端可伪造。
- **残留风险（必须明示）**：key 会经过 Cloudflare 边缘（对外入口 `https://vcs.king.nyc.mn`），Cloudflare 到源站的链路无法从本机核实；主密钥丢失则已存 key 无法解密，需重新填写。

## 安全边界（2026-11-11 已复核接受）
- Hub 进程持有加密后的 key，并在 runner 领取任务时解密后下发。key 会经过 Cloudflare 边缘（https://vcs.king.nyc.mn）。
- Cloudflare 到源站的链路无法从本机核实，交付时必须写明。
- 下发接口只接受 runner 的命名令牌，并要求租约有效、模型在白名单内、来源为 https（X-Forwarded-Proto 或回环）。X-Forwarded-Proto 可被局域网内客户端伪造，这一条只是辅助防护。
- 主密钥丢失则已存的 key 无法解密，需要重新填写。

## 不在本修订内
- 面板内的“测试连接”（由 runner 完成，另开卡）。
- Hub 代理模型调用（方案 B2，本次未选）。
