# 项目档案：vps-commander

| 项 | 内容 |
|----|------|
| 主仓库 | wjyhk:/root/.openclaw/workspace/vps-commander（主线 main） |
| 语言 | Go |
| 开发者 | GPT（经 MCP）、DeepSeek flash（DevTask runner，项目名 vps-commander） |
| 审核 | Hark |
| 指定模型 | 仅 global:deepseek-v4.1-flash |
| 当前阶段 | 快速迭代期：只提交本地 main，不 push/tag/发版 |

## 验收命令
```
gofmt -l .            # 无输出
go vet ./...
go build ./...
go test -count=1 ./...
go test -race <相关包>
git diff --check
```
DevTask 允许命令：go build ./...、go vet ./...、go test ./...、go test -count=1 ./...、gofmt -l .

## 受保护路径
deploy/、scripts/ 中的部署脚本、.github/、data/、bin/、go.mod、go.sum

## DevTask
- 镜像：/srv/devtask/repos/vps-commander（派发前同步 main）
- runner：devtask-runner@vps-commander；命令只传 GOROOT/GOPATH/GOMODCACHE/GOCACHE/GOTOOLCHAIN，每条命令独立 HOME/TMPDIR，模块缓存 /srv/devtask/go

## 构建与发布
1. 在 /root/vc-main 构建 Hub 与 runner；生产 DB 副本迁移 dry-run（healthz 200）。
2. 产物放 /root/vc-release-<sha>/，记录 SHA256。
3. /root/vc-deploy/deploy.sh 部署：自动备份到 /opt/vps-commander/backup-pre-<sha>-*，健康检查，在线节点数核对，失败自动回滚；日志出现 DEPLOY OK 才算上线。
4. runner 变更：旧二进制备份为 /usr/local/bin/devtask-runner.pre-<sha>，替换后重启所有 runner 实例，同步镜像。

## 通知
- 完成通知：development.completed → 企业微信开发群
- 审核结论：review-completed 事件（/api/v1/events）
