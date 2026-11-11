# T-06 实查返工：2026-11-07 claude-2 429 与 exec 区间核对

## 结论摘要

本报告由 root 在 `wjy5010u` 上直接只读查询生产 SQLite 与 systemd journal 生成。查询窗口为本地时间（UTC+08:00）`2026-11-07 07:40:00` 至 `08:10:00`，Unix 秒时间戳 `1794008400` 至 `1794010200`。未修改数据库、生产配置或服务。

- `audit_logs` 中 `claude-2` / `Br51PEdY7TbD4CWE` 在窗口内有 **20 条**审计记录，均为 `exec`。
- journal 中同一身份共有 **28 条 HTTP 请求日志**，其中 **6 条 HTTP 429**，每条均为 `duration_ms=1` 或 `3`、`bytes=32`。
- 07:47:51.333 与 07:47:54.604 两次 429 落在两条已审计 exec 的重叠执行区间内：ID 13378（07:46:41 至 07:47:55）和 ID 13379（07:47:48 至 07:48:51）。这两次拒绝与令牌 exec 并发槽已满的情况相符。
- 07:49:52.389、07:53:13.363、08:05:51.106、08:06:29.814 四次 429 不落在窗口内任何已审计 exec 执行区间。尤其 07:49:52 之后至 07:59:29 之前没有该身份的审计 exec；08:05:23 的最后一条 exec 在 9ms 后结束，之后两次 429 也没有活动 exec 区间可对应。
- 对窗口前一小时 `06:40:00` 至 `07:40:00` 的审计记录进行跨界检查，没有找到启动于该窗口、结束时间达到或超过 07:40:00 的 `exec` / `exec_start`。
- 6 条 429 的 `bytes=32` 完全相同，但 journal 只记录响应字节数、不记录响应体。**仅凭 32 字节无法把 6 次 429 全部定性为并发拒绝**。可确定的边界是：前两次与两条重叠 exec 区间吻合；后四次无法由本窗口内的活动 exec 占槽解释，可能属于其他限流路径或日志未覆盖的活动，现有证据不足以进一步定因。

因此，本次 T-06 证据不支持“6 次 429 全部由长任务占槽造成”，也不支持“6 次均与并发无关”。证据支持的是**前两次与两个并行 exec 占满令牌并发槽的时间重叠；其余四次不在已记录 exec 区间内**。该窗口结论仅针对 2026-11-07 07:40–08:10 的 `claude-2`，不能外推为 11-08 至 11-10 Dave-1 的 67 次 429 事件结论；后者需要独立的原始审计与 journal 证据。

## 1. SQLite 实查

数据库：`/opt/vps-commander/data/commander.db`。使用 `sqlite3 -readonly`，表结构确认字段为 `actor_id`、`actor_name`，`timestamp` 为秒级 INTEGER。

### 1.1 窗口内全部审计记录

执行命令（CSV 输出避免列宽填充空格；字段按任务卡要求完整选择）：

```sh
sqlite3 -readonly -csv -header /opt/vps-commander/data/commander.db "SELECT id, timestamp, datetime(timestamp,'unixepoch','localtime'), action, command, duration_ms, exit_code, result, error_msg FROM audit_logs WHERE timestamp BETWEEN 1794008400 AND 1794010200 AND (actor_name='claude-2' OR actor_id='Br51PEdY7TbD4CWE') ORDER BY timestamp, id;"
```

原样输出：

```csv
id,timestamp,"datetime(timestamp,'unixepoch','localtime')",action,command,duration_ms,exit_code,result,error_msg
13367,1794008467,"2026-11-07 07:41:07",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; G=""git -c safe.directory=$W -C $W""; echo ""HEAD: $($G rev-parse --short HEAD) branch: $($G rev-parse --abbrev-ref HEAD)""; echo ""--- log ---""; $G log --oneline -5; echo ""--- status ---""; $G status --short; echo ""--- commits since 65912d8 ---""; $G log --format='%h %an <%ae> | %s' 65912d8..HEAD; echo ""--- diff stat since last review (7d4dcdc) ---""; $G diff --stat 7d4dcdc HEAD 2>/dev/null | tail -5",27,0,ok,""
13368,1794008474,"2026-11-07 07:41:14",exec,"R=/home/orca/projects/vps-commander; G=""git -c safe.directory=$R -C $R""; echo ""== worktrees ==""; $G worktree list; echo ""== branches touching tool descriptions after 9086f48 ==""; for b in $($G for-each-ref --format='%(refname:short)' refs/heads/); do n=$($G log --oneline 9086f48..$b 2>/dev/null | wc -l); [ ""$n"" -gt 0 ] && echo ""$b: $n new commits; head $($G log -1 --format='%h %an %s' $b | cut -c1-120)""; done; echo ""== '同一节点' wording anywhere in worktrees/branches ==""; $G grep -n ""同一节点\|同一设备"" fix/tool-desc…",826,0,ok,""
13370,1794008606,"2026-11-07 07:43:26",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; echo ""== move_file description on the reviewed commit =="" ; git -c safe.directory=$W -C $W show 7d4dcdc:internal/mcp/mcp.go | grep -o 'Name: ""move_file"", Description: ""[^""]*""' | head -1; echo ""== working tree now ==""; grep -o 'Name: ""move_file"", Description: ""[^""]*""' $W/internal/mcp/mcp.go | head -1; echo ""== any copy on disk with the new wording ==""; grep -rl ""同一节点"" /opt/dev/worktrees /home/orca/projects 2>/dev/null | grep ""mcp.go"" | head -5; echo ""(end)""; echo ""…",124992,124,exit_nonzero,"stdout: == move_file description on the reviewed commit == Name: ""move_file"", Description: ""在目标设备上移动或重命名文件，若目标文件已存在将被覆盖，源文件与目标文件必须在同一设备"" == working tree now == Name: ""move_file"", Description: ""在目标设备上移动或重命名文件，若目标文件已存在将被覆盖，源文件与目标文件必须在同一设备"" == any copy on disk with the new wording =="
13373,1794008666,"2026-11-07 07:44:26",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; git -c safe.directory=$W -C $W show 7d4dcdc:internal/mcp/mcp.go | grep -o 'Name: ""move_file"", Description: ""[^""]*""' | head -1; grep -o 'Name: ""move_file"", Description: ""[^""]*""' $W/internal/mcp/mcp.go | head -1; grep -rl ""同一节点"" /opt/dev/worktrees /home/orca/projects 2>/dev/null | grep ""mcp.go"" | head -3; echo ""(end)""",56785,0,ok,""
13374,1794008676,"2026-11-07 07:44:36",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; cd $W; export PATH=/usr/local/go/bin:$PATH; . /etc/profile.d/vps-dev-env.sh; echo ""== uncommitted files ==""; git -c safe.directory=$W -C $W status --short; echo ""== uncommitted diff ==""; git -c safe.directory=$W -C $W diff | grep '^[-+]' | grep -v '^+++\|^---' | cut -c1-220; echo ""== gofmt/vet ==""; echo ""gofmt: [$(gofmt -l internal/mcp)]""; go vet ./internal/mcp/ && echo VET_OK; go test -count=1 -run 'TestToolMetadataComplete|TestEveryToolHasAnnotations|TestToolNam…",3390,0,ok,""
13375,1794008684,"2026-11-07 07:44:44",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; cd $W; echo ""HEAD: $(git -c safe.directory=$W -C $W rev-parse --short HEAD)""; git -c safe.directory=$W -C $W show HEAD:internal/mcp/mcp.go | cmp -s - internal/mcp/mcp.go && echo ""content identical to HEAD"" || echo ""content DIFFERS from HEAD""; echo ""sizes: HEAD=$(git -c safe.directory=$W -C $W show HEAD:internal/mcp/mcp.go | wc -c) worktree=$(wc -c < internal/mcp/mcp.go)""; echo ""mtime: $(stat -c '%y' internal/mcp/mcp.go | cut -c1-19)""; echo ""--- refresh index and r…",48,0,ok,""
13376,1794008689,"2026-11-07 07:44:49",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; G=""git -c safe.directory=$W -C $W""; $G log --format='%h %an <%ae> %ad | %s' --date=format:'%m-%d %H:%M' -4; echo ""== files changed since reviewed commit 7d4dcdc ==""; $G diff --stat 7d4dcdc HEAD | tail -6; echo ""== full diff since 7d4dcdc ==""; $G diff 7d4dcdc HEAD | grep '^[-+]' | grep -v '^+++\|^---' | cut -c1-240",30,0,ok,""
13377,1794008723,"2026-11-07 07:45:23",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; cd $W; export PATH=/usr/local/go/bin:$PATH; . /etc/profile.d/vps-dev-env.sh; echo ""gofmt: [$(gofmt -l .)]""; go vet ./... && echo ""vet rc=0""; go build ./... && echo ""build rc=0""; bash docs/process/check-task.sh T-20261101-11 65912d8 > /tmp/ct11c.log 2>&1; echo ""check-task exit=$?""; grep -E ""^(✓|✗|==)"" /tmp/ct11c.log",26792,0,ok,""
13378,1794008801,"2026-11-07 07:46:41",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; cd $W; export PATH=/usr/local/go/bin:$PATH; . /etc/profile.d/vps-dev-env.sh; go test -count=1 -race -shuffle=on ./... > /tmp/g11_s1.log 2>&1; echo ""race run1 rc=$?""; grep -E ""^(FAIL|--- FAIL|panic|WARNING: DATA RACE)"" /tmp/g11_s1.log | head -3; grep -c ""^ok"" /tmp/g11_s1.log",74803,0,ok,""
13379,1794008868,"2026-11-07 07:47:48",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; cd $W; export PATH=/usr/local/go/bin:$PATH; . /etc/profile.d/vps-dev-env.sh; go test -count=1 -race -shuffle=on ./... > /tmp/g11_s2.log 2>&1; echo ""race run2 rc=$?""; grep -E ""^(FAIL|--- FAIL|panic|WARNING: DATA RACE)"" /tmp/g11_s2.log | head -3; grep -c ""^ok"" /tmp/g11_s2.log",63439,0,ok,""
13404,1794009569,"2026-11-07 07:59:29",exec,"echo ok",2,0,ok,""
13405,1794009572,"2026-11-07 07:59:32",exec,"ls -la /opt/dev/ 2>&1 | head -20; echo ""--- existing rules files? ---""; ls /opt/dev/AGENT-RULES.md 2>&1",9,2,exit_nonzero,"stdout: 总计 32 drwxrwsr-x+  6 root dev  4096 十一月 1日 07:29 . drwxr-xr-x  12 root root 4096 十一月 2日 21:18 .. drwxrwsr-x+  2 root dev  4096 十一月 1日 07:29 bin drwxrwsr-x+  4 root dev  4096 十一月 1日 07:29 build drwxrwsr-x+  2 root dev  4096 十一月 6日 16:19 locks -rw-r--r--+  1 root dev  6005 十一月 1日 07:30 README-SHARED-ENV.md drwxrwsr-x+  4 root dev  4096 十一月 6日 16:15 worktrees --- existing rules files? --- ls: 无法访问 '/opt/dev/AGENT-RULES.md': 没有那个文件或目录"
13406,1794009582,"2026-11-07 07:59:42",exec,"cat > /opt/dev/AGENT-RULES.md <<'EOF' # Agent 操作规则（所有入口共用：root、orca、deva 及任何代理）  状态：生效（2026-11-07）。读取本文件是每次开始重负载操作前的第一步。  ## 一、为什么有这些规则  2026-11-01 到 11-07 期间多次触发连接器限流。原因都在调用方式，不是连接器本身： 1. 失败后立即连续重试，没有退避； 2. 同一个重负载检查（race 全量测试）连续跑三轮，中间没有间隔； 3. 限流后仍继续跑检查、读日志、再跑测试，等于在限流期间持续加压； 4. 教训只存在于对话上下文中，没有写进共享文件，下一次操作时不会生效。  ## 二、基本节奏  1. 单线程：同一时间只有一个调用在途。前一个返回后才发下一个。 2. 重负载独占：全量 race 测试、全量构建各自独占一个时段，不与其他调用重叠。 3. 单次调用目标时长 60 秒以内。超过的任务拆分，或放到后台运行后轮询状态文件。 4. 轮次之间留出间隔：一轮全量 race 结束后，先做一次轻量只读检查（如 `git status`、`ls`），确认连接正常，再决…",12,0,ok,""
13407,1794009586,"2026-11-07 07:59:46",exec,"R=/home/orca/projects/vps-commander; G=""git -c safe.directory=$R -C $R""; $G grep -n -E ""RateRPS|rate_rps|DefaultRate|defaultRate|MaxConcurrent|maxConcurrent|concurrent exec|burst|Burst"" main -- internal/api/token_limiter.go internal/storage/tokens.go internal/api/tokens.go 2>/dev/null | grep -v _test | cut -c1-180 | head -20",9,0,ok,""
13408,1794009594,"2026-11-07 07:59:54",exec,"python3 - <<'PYEOF' p = ""/opt/dev/AGENT-RULES.md"" s = open(p, encoding=""utf-8"").read() old4 = """"""## 四、vchub 限速参数（待核实）  - 每令牌速率上限（RateRPS）：待填 - 每令牌并发 exec 上限：待填 - 每节点远端会话上限：4（已核实，见 docs/api.md） - 注意：连接器（MCP 连接层）的限流与 vchub 的限流是两套规则，连接器的限流不由 vchub 参数决定。 """""" new4 = """"""## 四、vchub 限速参数（已核实，来源：internal/api/token_limiter.go，main 分支）  - 每令牌速率上限：默认 10 次/秒（defaultTokenRateRPS），可用 rate_rps 单独配置。 - 每令牌并发 exec 上限：未配置时默认 2（NewTokenLimiter 的默认值）。 - 超限返回 HTTP 429，错误码 rate_limited，详情为“速率”或“token concurrent exec limit reache…",30,0,ok,""
13409,1794009661,"2026-11-07 08:01:01",exec,"W=/opt/dev/worktrees/gemini/T-20261101-11; cd $W; export PATH=/usr/local/go/bin:$PATH; . /etc/profile.d/vps-dev-env.sh; go test -count=1 -race -shuffle=on ./... > /tmp/g11_s3.log 2>&1; echo ""race run rc=$?""; grep -E ""^(FAIL|--- FAIL|panic|WARNING: DATA RACE)"" /tmp/g11_s3.log | head -3; grep -c ""^ok"" /tmp/g11_s3.log",63224,0,ok,""
13410,1794009811,"2026-11-07 08:03:31",exec,"R=/home/orca/projects/vps-commander; G=""git -c safe.directory=$R -C $R""; echo ""== where concurrency limit is set (main) ==""; $G grep -n ""NewTokenLimiter("" main -- cmd internal 2>/dev/null | grep -v _test | cut -c1-170; echo ""== env/flag for it ==""; $G grep -n -i ""EXEC_CONCURRENCY\|maxConcurrent\|MaxConcurrentExec\|max-concurrent"" main -- cmd internal 2>/dev/null | grep -v _test | cut -c1-170 | head -6; echo ""(end)""",38,0,ok,""
13411,1794009819,"2026-11-07 08:03:39",exec,"python3 - <<'PYEOF' p = ""/opt/dev/AGENT-RULES.md"" s = open(p, encoding=""utf-8"").read() old = """"""- 每令牌速率上限：默认 10 次/秒（defaultTokenRateRPS），可用 rate_rps 单独配置。 - 每令牌并发 exec 上限：未配置时默认 2（NewTokenLimiter 的默认值）。"""""" new = """"""- 每令牌速率上限：默认 10 次/秒（defaultTokenRateRPS），可用 rate_rps 单独配置。2026-11-07 已将我的令牌改为 20 次/秒。 - 每令牌并发 exec 上限：2。来源是 cmd/hub/main.go，读取环境变量 VPS_COMMANDER_TOKEN_EXEC_CONCURRENCY（默认 2），其值写在生产的 /etc/vps-commander/hub.env 中。修改它需要改 hub.env 并重启 Hub，属于生产变更，必须经 OPC 批准，任何入口不得自行修改。"""""" assert s.count(old) == 1 s = …",31,0,ok,""
13412,1794009905,"2026-11-07 08:05:05",exec,"R=/home/orca/projects/vps-commander; G=""git -c safe.directory=$R -C $R""; echo ""== deploy/README.md: section headings and rollback-related lines ==""; $G show main:deploy/README.md | grep -n -E ""^#|回滚|rollback|ROLLBACK|sqlite3|restore|备份|backup|dry-run|sha256"" | cut -c1-200 | head -60",6,0,ok,""
13413,1794009923,"2026-11-07 08:05:23",exec,"R=/home/orca/projects/vps-commander; G=""git -c safe.directory=$R -C $R""; $G show main:deploy/README.md | sed -n 35,60p | cut -c1-220; echo ""== script usage line ==""; $G show main:deploy/deploy-hub-local.sh | grep -n -i ""usage\|--sha256\|--dry-run\|\$1"" | head -8 | cut -c1-180",9,0,ok,""
```

### 1.2 窗口前一小时跨界长任务检查

执行命令：

```sh
sqlite3 -readonly -header -column /opt/vps-commander/data/commander.db "SELECT id, timestamp, datetime(timestamp,'unixepoch','localtime') AS start_local, action, duration_ms, timestamp + duration_ms/1000.0 AS end_epoch, datetime(timestamp + duration_ms/1000.0,'unixepoch','localtime') AS end_local FROM audit_logs WHERE timestamp BETWEEN 1794004800 AND 1794008400 AND action IN ('exec','exec_start') AND timestamp + duration_ms/1000.0 >= 1794008400 AND (actor_name='claude-2' OR actor_id='Br51PEdY7TbD4CWE') ORDER BY timestamp;"
```

原样输出：


## 2. systemd journal 实查

执行任务卡指定的完整窗口命令，并从结果中筛选 `actor=claude-2` 的 HTTP 请求行（包括 200 与 429）：

```sh
journalctl -u vps-commander-hub --since "2026-11-07 07:40:00" --until "2026-11-07 08:10:00" --no-pager -o short-precise | grep 'actor=claude-2'
```

原样输出：

十一月 07 07:41:06.297461 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:41:06.297+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=1 bytes=102
十一月 07 07:41:07.389880 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:41:07.389+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=30 bytes=2085 mcp_tool=exec_command
十一月 07 07:41:14.446697 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:41:14.446+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=901 bytes=9260 mcp_tool=exec_command
十一月 07 07:43:26.867588 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:43:26.867+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=124997 bytes=1558 mcp_tool=exec_command
十一月 07 07:44:26.929091 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:44:26.928+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=56788 bytes=1446 mcp_tool=exec_command
十一月 07 07:44:36.741885 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:44:36.741+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=3395 bytes=1292 mcp_tool=exec_command
十一月 07 07:44:44.166721 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:44:44.166+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=51 bytes=946 mcp_tool=exec_command
十一月 07 07:44:49.287255 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:44:49.286+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=34 bytes=4435 mcp_tool=exec_command
十一月 07 07:45:23.617030 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:45:23.616+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=26795 bytes=1287 mcp_tool=exec_command
十一月 07 07:46:41.553269 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:46:41.552+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=74807 bytes=654 mcp_tool=exec_command
十一月 07 07:47:48.611203 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:47:48.611+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=63442 bytes=654 mcp_tool=exec_command
十一月 07 07:47:51.333346 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:47:51.333+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=429 duration_ms=1 bytes=32
十一月 07 07:47:54.604188 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:47:54.604+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=429 duration_ms=1 bytes=32
十一月 07 07:49:52.390000 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:49:52.389+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=429 duration_ms=1 bytes=32
十一月 07 07:53:13.363667 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:53:13.363+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=429 duration_ms=1 bytes=32
十一月 07 07:59:28.803003 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:59:28.802+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=1 bytes=101
十一月 07 07:59:29.504394 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:59:29.504+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=3 bytes=610 mcp_tool=exec_command
十一月 07 07:59:32.929351 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:59:32.929+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=11 bytes=1680 mcp_tool=exec_command
十一月 07 07:59:42.537249 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:59:42.537+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=17 bytes=792 mcp_tool=exec_command
十一月 07 07:59:46.030632 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:59:46.030+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=14 bytes=4917 mcp_tool=exec_command
十一月 07 07:59:54.450956 wjy5010u vps-commander-hub[789]: time=2026-11-07T07:59:54.450+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=35 bytes=1471 mcp_tool=exec_command
十一月 07 08:01:01.874621 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:01:01.874+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=63227 bytes=651 mcp_tool=exec_command
十一月 07 08:03:31.521055 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:03:31.521+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=41 bytes=2193 mcp_tool=exec_command
十一月 07 08:03:39.418687 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:03:39.418+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=36 bytes=2108 mcp_tool=exec_command
十一月 07 08:05:05.723379 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:05:05.723+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=10 bytes=4830 mcp_tool=exec_command
十一月 07 08:05:23.933722 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:05:23.933+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=200 duration_ms=14 bytes=3090 mcp_tool=exec_command
十一月 07 08:05:51.106578 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:05:51.106+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=429 duration_ms=3 bytes=32
十一月 07 08:06:29.814738 wjy5010u vps-commander-hub[789]: time=2026-11-07T08:06:29.814+08:00 level=INFO msg=http_request method=POST path=/mcp client_ip=192.168.20.1 actor_type=api_token actor=claude-2 status=429 duration_ms=1 bytes=32

## 3. 429 与 exec 区间逐条比对

以下起止时间由 `audit_logs.timestamp`（秒）与 `duration_ms`（毫秒）计算，HTTP 时间取自上面的 journal 原始输出。

| HTTP 429 本地时间 | HTTP duration / bytes | 对应活动审计 exec | 判断 |
|---|---:|---|---|
| 07:47:51.333 | 1ms / 32 | ID 13378：07:46:41–07:47:55；ID 13379：07:47:48–07:48:51 | 两条 exec 同时活动；与并发槽满相符 |
| 07:47:54.604 | 1ms / 32 | ID 13378：07:46:41–07:47:55；ID 13379：07:47:48–07:48:51 | 两条 exec 同时活动；与并发槽满相符 |
| 07:49:52.389 | 1ms / 32 | 无 | 不在任何窗口内已审计 exec 区间 |
| 07:53:13.363 | 1ms / 32 | 无 | 不在任何窗口内已审计 exec 区间 |
| 08:05:51.106 | 3ms / 32 | 无；最后一条 exec ID 13413 于 08:05:23.009 结束 | 不在任何窗口内已审计 exec 区间 |
| 08:06:29.814 | 1ms / 32 | 无 | 不在任何窗口内已审计 exec 区间 |

日志的 `bytes=32` 只提供响应长度，未提供响应体或 limiter 分类字段。因此不能仅凭响应长度把 6 条 429 全部归因为并发拒绝。窗口内审计记录没有 `exec_start`，20 条均为 `exec`；没有发现从 06:40–07:40 延续进入目标窗口的 exec / exec_start。

## 4. 复核边界

- 以上结论严格限定于 `claude-2`、`2026-11-07 07:40:00–08:10:00` 和当前生产 DB/journal 留存的数据。
- Dave-1 在 2026-11-08 至 2026-11-10 的 67 次 429 是另一个时间段、另一个 actor；本报告未查询该数据，不把它作为本次 T-06 结论的证据。
- 查询均为只读；没有执行生产写操作、配置修改或服务重启。
