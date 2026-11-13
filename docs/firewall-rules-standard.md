# 路由器防火墙规则规范

> **状态**：🔒 **已固化，勿修改**
> **最后确认**：2026-11-11 17:50
> **教训来源**：同一错误犯过 2 次（11-03、11-11）

---

## 铁律：只按端口放行，绝不绑定 dest_ip

### ❌ 错误写法（会失效）

```bash
-A zone_wan_forward -d 240e:36a:6c13:be00:72e2:4cff:fe68:216/128 -p tcp --dport 8886 -j ACCEPT
                        ↑ 写死具体 IPv6 地址
```

**为什么错**：运营商 IPv6 前缀会变，地址一变，规则就静默失效。

### ✅ 正确写法

```bash
-A zone_wan_forward -p tcp --dport 8886 -j zone_lan_dest_ACCEPT
```

**不指定 `-d`，任何目标的 8886 都放行。**

---

## 已发生的 IPv6 前缀变更

| 时间 | 前缀 | 结果 |
|---|---|---|
| ~2026-10 | `240e:36a:6c16:a000::` | 规则绑定此地址 |
| 2026-11-03 | → `240e:36a:6c13:be00:` | 旧规则失效，全舰队断连 75 分钟 |
| 2026-11-11 | → `240e:36a:6c12:8000:` | 旧规则再次失效 |

**三个月内变了 3 次。绑定固定地址必然失效。**

---

## 当前生效的规则（8886）

### uci 配置（`/etc/config/firewall` 的 `@rule[15]`）

```
option name 'Allow-orca6-8886'
option src 'wan'
option dest 'lan'
option proto 'tcp'
option dest_port '8886'
option target 'ACCEPT'
option family 'ipv6'
option enabled '1'
```

**注意：没有 `option dest_ip`。**

### iptables 实际生效

```bash
-A zone_wan_forward -p tcp -m tcp --dport 8886 -m comment --comment "!fw3: Allow-orca6-8886" -j zone_lan_dest_ACCEPT
```

---

## 修改规则的正确方法

**必须用 `uci`，禁止 `sed`/`awk`/手写文件。**

```bash
# 1. 备份
cp /etc/config/firewall /root/firewall.bak-$(date +%s)

# 2. 用 uci 修改
uci set firewall.@rule[15].dest_port='8886'
uci set firewall.@rule[15].proto='tcp'
uci set firewall.@rule[15].target='ACCEPT'
uci set firewall.@rule[15].family='ipv6'
# 关键：不要设 dest_ip

uci commit firewall

# 3. 语法检查（必须有）
fw3 check 2>&1 | grep -i "does not specify\|unknown"

# 4. 检查空规则块（11-03 事故的元凶）
awk '/^config /{if(prev && !opt) print "空块: " prev; prev=$0; opt=0; next}
     /^[[:space:]]*option /{opt=1}
     END{if(prev && !opt) print "空块: " prev}' /etc/config/firewall

# 5. 重载
/etc/init.d/firewall reload

# 6. 验证（应无 -d 参数）
ip6tables-save | grep 8886

# 7. 检查 OUTPUT 链（11-03 事故症状）
iptables -L OUTPUT -n | grep -c reject
ip6tables -L OUTPUT -n | grep -c reject
```

---

## 新增放行规则的模板

**需要放行新端口时，照这个写：**

```bash
uci add firewall rule
uci set firewall.@rule[-1].name='Allow-<服务名>-<端口>'
uci set firewall.@rule[-1].src='wan'
uci set firewall.@rule[-1].dest='lan'
uci set firewall.@rule[-1].proto='tcp'
uci set firewall.@rule[-1].dest_port='<端口>'
uci set firewall.@rule[-1].target='ACCEPT'
uci set firewall.@rule[-1].family='ipv6'
uci set firewall.@rule[-1].enabled='1'
# ⚠️ 绝对不要加 dest_ip
uci commit firewall
```

---

## 故障识别特征

**出现以下症状时，先查规则是否绑定了旧 IP：**

| 症状 | 判断 |
|---|---|
| **本机自测正常，外网访问不通** | 🔴 高度怀疑防火墙规则绑定旧地址 |
| 路由器能 ping 通本机 | 网络层没问题 |
| 路由器 TCP 连不上本机端口 | 防火墙规则不匹配 |
| 本机 `curl --resolve` 返回 200 | 服务本身正常 |

**排查命令：**

```bash
# 1. 本机当前 IPv6
ip -6 addr show dev enp2s0 scope global | grep inet6

# 2. 路由器规则
ssh root@192.168.20.1 'ip6tables-save | grep <端口>'

# 3. 对比：规则里的 -d 是否等于本机当前地址
```

---

## 历史事故

### 2026-11-03（P0）

- **操作**：手写 `/etc/config/firewall` 添加规则，留下空 `config rule` 块
- **后果**：fw3 把空块解释为 OUTPUT 链全局 REJECT，全舰队断连 **75 分钟**
- **教训**：必须用 `uci`，改完必须 `fw3 check` + 检查空块

### 2026-11-11（P1）

- **操作**：规则绑定 `240e:36a:6c13:be00:...`
- **后果**：运营商换前缀到 `6c12:8000`，外网访问失败
- **教训**：**规则永远不绑定具体 IP**

---

## 相关文档

| 文档 | 位置 |
|---|---|
| 事故教训库 | `/home/orca/projects/vps-commander/docs/lessons/README.md` |
| 11-03 事故复盘 | `docs/incident-report-20261103-router-proxy-outage.md` |

---

*本规范已固化。任何时候改路由器防火墙，先读这份文档。*
