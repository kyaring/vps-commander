# 故障复盘与排查报告：路由器防火墙空规则致 9521 反向代理阻断事故

- **故障日期**：2026-11-03
- **故障定级**：P1（承载全舰队公网入口的 Hub 反代全量中断）
- **影响服务**：VPS-Commander Hub 公网反代接入（`https://vcs.king.nyc.mn`）、管理控制台（`/panel`）、MCP 服务（`/mcp`）
- **故障状态**：已解决并复核验证通过

---

## 一、事故概览

2026-11-03 凌晨，外部请求及舰队客户端经 Cloudflare 访问 VPS-Commander Hub 公网入口 `https://vcs.king.nyc.mn/healthz` 时全面报 **`HTTP 502 Bad Gateway`**。经排查，路由器（`192.168.20.1`）上的反向代理向局域网目标机（`192.168.20.132:9521`）转发流量时被内核直接以 TCP RST 拒绝（`Connection refused`）。

**根因**：路由器 `/etc/config/firewall` 在 2026-11-03 00:43 进行变更时，意外残留了一个无任何属性参数的空规则块 `config rule`。OpenWrt 防火墙引擎（`fw3`）在解析该空块时，将其默认推导为**路由器本机出站规则（OUTPUT 链）**且默认 target 为 **`REJECT`**，导致路由器本机发出的**所有新 TCP/UDP 出站连接**（包括反向代理发往内网各主机的请求）被路由器自身内核防火墙直接拦截拒绝。

删除该异常空规则并重载防火墙后，反向代理及公网访问已于 01:48 全面恢复正常。

---

## 二、事故时间线

| 时间（2026-11-03） | 阶段 | 关键事件与排查操作 |
|---|---|---|
| **00:43:00 – 00:43:57** | **故障引入** | 运维 SSH 会话登录路由器 `192.168.20.1`，备份配置并编辑 `/etc/config/firewall`，误留空白 `config rule` 段，防火墙重载后全局出站拦截生效。 |
| **01:03** | **主机排查** | 核查目标机 `wjy5010u`，确认 Hub 进程（PID: 789）正常运行在 `192.168.20.132:9521`，本机访问健康检查正常（`HTTP 200 {"status":"ok"}`）。 |
| **01:06** | **主机防火墙** | 确认主机 `nftables` 规则正常放行 `192.168.20.0/24` 的 9521 端口访问。 |
| **01:18** | **公网验证** | 探测公网入口 `https://vcs.king.nyc.mn/healthz`，证实返回 `HTTP/2 502 Bad Gateway`。 |
| **01:43** | **登录路由器** | 使用 root 凭据成功 SSH 登录路由器 `192.168.20.1`，通过 `logread` 定位到 00:43 的变更记录与备份文件 `firewall.bak-1793637781`。 |
| **01:45** | **连通性复现** | 在路由器上执行 `wget http://192.168.20.132:9521/healthz`，直接报 `Connection refused`，且探测 22 端口同样被拒绝。 |
| **01:46** | **根因锁定** | 检查 iptables `OUTPUT` 链，发现异常规则 `-A OUTPUT -p tcp -j reject`；执行 `fw3 check` 明确告警 `@rule[15]` 被当作 OUTPUT 链默认 REJECT 处理。 |
| **01:48** | **故障修复** | 路由器上执行 `uci delete firewall.@rule[15] && uci commit firewall && /etc/init.d/firewall reload`，成功清除异常拦截规则。 |
| **01:48 – 01:50** | **复核验收** | 路由器直连 9521 恢复 200 OK；公网 `https://vcs.king.nyc.mn` 健康检查与 Web 控制台恢复正常。 |

---

## 三、根因机理深入剖析

### 1. 配置文件异常比对
对比路由器上的备份文件与当前文件：
```diff
--- /etc/config/firewall.bak-1793637781
+++ /etc/config/firewall
@@ -224,6 +224,13 @@
 	list dest_ip '240e:36a:6cfb:800:d54e:d1e5:533:daa7'
 
+config rule
+
+
+config rule
+	option name '8886-ipv6'
+	option src 'wan'
+...
```

### 2. OpenWrt `fw3` 引擎的默认推导行为
当 `fw3` 解析到未经任何修饰的 `config rule` 块时：
- **未配置 `src` / `dest`**：无法归属于任何转发或入站区域，`fw3` 假定其作用于宿主机自身产生的数据包，将其归入 **`OUTPUT`** 链；
- **未配置协议 `proto`**：自动假定匹配 **TCP + UDP**；
- **未配置动作 `target`**：依据全局默认安全策略，自动回退默认值为 **`REJECT`**（发送 TCP RST 或 ICMP Port Unreachable）。

执行 `fw3 check` 时的原始诊断输出如下：
```text
Warning: Section @rule[15] has neither a source nor a destination zone assigned - assuming an output rule
Warning: Section @rule[15] does not specify a protocol, assuming TCP+UDP
Warning: Section @rule[15] has no target specified, defaulting to REJECT
```

### 3. iptables 内核链的表现机制
在路由器的 `filter` 表中，`OUTPUT` 链的顺序被编译为：
```iptables
-A OUTPUT -o lo -j ACCEPT
-A OUTPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A OUTPUT -p tcp -m comment --comment "!fw3: @rule[14]" -j reject
-A OUTPUT -p udp -m comment --comment "!fw3: @rule[14]" -j reject
-A OUTPUT -o br-lan -j zone_lan_output
```
- **为何已有入站 SSH 仍然正常？**
  由外部发起的连接到达路由器时属于 `INPUT` 链；当路由器回复数据时，连接状态已是 `ESTABLISHED`，第 2 条规则立即放行，因此已有 SSH 不受影响。
- **为何出站反代与探测全部被拒？**
  当反向代理服务（Lucky / Nginx）尝试主动向 `192.168.20.132:9521` 发起新握手时，数据包状态为 `NEW`，跳过第 2 条规则后命中第 3 条 `-j reject`，由路由器本地内核立即回复 TCP Reset，表现为立即“连接被拒绝”。

---

## 四、修复操作与变更确认

### 1. 修复命令
在路由器（`192.168.20.1`）执行：
```sh
# 删除第 15 号空规则节点
uci delete firewall.@rule[15]

# 提交持久化
uci commit firewall

# 重载生效防火墙规则
/etc/init.d/firewall reload
```

### 2. 生效状态确认
重载后检查 `OUTPUT` 链，全局 `reject` 规则已完全消失：
```text
-P OUTPUT ACCEPT
-A OUTPUT -o lo -m comment --comment "!fw3" -j ACCEPT
-A OUTPUT -m conntrack --ctstate RELATED,ESTABLISHED -m comment --comment "!fw3" -j ACCEPT
-A OUTPUT -o br-lan -m comment --comment "!fw3" -j zone_lan_output
-A OUTPUT -o pppoe-wan -m comment --comment "!fw3" -j zone_wan_output
-A OUTPUT -o wan -m comment --comment "!fw3" -j zone_wan_output
```

---

## 五、复核验证矩阵

| 测试维度 | 测试目标 / 命令 | 修复前表现 | 修复后表现 | 判定 |
|---|---|---|---|---|
| **内网连通性** | 路由器上执行：<br>`wget -O - -S http://192.168.20.132:9521/healthz` | `failed: Connection refused.` | `Connecting... connected.`<br>`HTTP/1.1 200 OK`<br>`{"status":"ok"}` | **PASS** |
| **公网 API 健康检查** | `curl -s -i https://vcs.king.nyc.mn/healthz` | `HTTP/2 502`<br>`server: cloudflare` | `HTTP/2 200`<br>`{"status":"ok"}` | **PASS** |
| **公网控制台访问** | `curl -s https://vcs.king.nyc.mn/panel/login` | 502 错误页 | 正常返回完整 HTML 登录表单 | **PASS** |

---

## 六、教训与运维规范

1. **UCI 配置格式规范**：
   - OpenWrt 的 UCI 格式解析器对于空白 section（如单纯的 `config rule`）非常敏感。在手工或脚本编辑 `/etc/config/firewall` 时，严禁遗留空 block。
2. **防火墙配置发布前必须校验**：
   - 任何涉及 `/etc/config/firewall` 的修改，在执行 `reload` 或重启前，必须运行：
     ```sh
     fw3 check
     ```
     若出现 `assuming an output rule` 或未显式声明的 `defaulting to REJECT` 警告，严禁盲目 reload。
3. **关键网关与反代节点变更原则**：
   - 路由器不仅承担 NAT / 路由，还承载反代与 DDNS 入口。任何针对路由器的变更操作，在交付前必须同时验证内网连通性与公网对外暴露服务的端到端可用性。
