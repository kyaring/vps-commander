【命令规则】run 工具只能执行：gofmt -l . / go build ./... / go vet ./... / go test ./... / go test -count=1 ./...，字符完全一致。
读写只能在：cmd/devtask-runner、internal/storage、docs/devtask-runner.md。internal/storage 下只允许修改 devtask.go 和 devtask_test.go（可新建 devtask_redact_test.go）。注意：读不存在的路径会让任务直接失败，请先 list_files 目录再读文件。

DevTask 平台三个小问题，一次修完。

一、本轮无改动仍报 needs_review（cmd/devtask-runner/runner.go）
现状：模型一行没改时，runner 仍以 needs_review 完成，浪费一次人工审核。
要求：在本轮模型开始前记录 worktree 的 HEAD；收尾时如果本轮没有暂存改动、且 HEAD 与本轮开始时相同（注意第 2 轮以后 HEAD 可能已不等于 BaseSHA，要和本轮开始时比，不是和 BaseSHA 比），则任务以 status=failed、error_code=no_changes 结束，error_detail 写一句英文说明并附模型最后总结的前 300 个字符。有改动时行为不变。
测试：首轮无改动 → failed/no_changes；首轮有改动 → needs_review；第 2 轮无新改动（HEAD 有上一轮提交）→ failed/no_changes。

二、脱敏过度（internal/storage/devtask.go 的 devTaskSecretTextPatterns / redactDevTaskText）
现状：
1. 单独 bearer 规则会把普通英文也打码，例如任务标题里的「redact standalone Bearer tokens」变成 Bearer 后接 [REDACTED]。
2. 关键字赋值规则允许不带前缀的单独 KEY，导致「Primary key: id」这类普通文本被打码。
要求：
1. 单独 bearer 后的值只有在「长度 >= 8 且至少含一个数字或 . _ ~ + / - 之一」时才脱敏（Go regexp 无断言，可用 ReplaceAllStringFunc 判断）。Authorization 冒号 Bearer 值 的规则保持不变，照旧总是脱敏。
2. 关键字规则里去掉不带前缀的单独 KEY；API_KEY、PRIVATE_KEY、xxx_KEY、xxx-KEY 等带前缀的照旧匹配。其它关键字（TOKEN、SECRET、PASSWORD 等）保持不变。
3. sk-、vpc_、ghp_ 等固定前缀规则不变。
测试：普通英文「Bearer tokens」「the bearer of」不变；bearer 后跟形如 JWT 的长值、或含数字的 8 位以上值被脱敏；Authorization 冒号 Bearer 短值仍被脱敏；「Primary key: id」不变；API_KEY 赋值、PRIVATE_KEY 赋值仍被脱敏。测试里只用明显的假值。

三、文档（docs/devtask-runner.md）
补一节「命令环境」：runner 只向任务命令传 GOROOT、GOPATH、GOMODCACHE、GOCACHE、GOTOOLCHAIN，每条命令有独立临时 HOME/TMPDIR；GOPROXY、GOFLAGS 等不会传入，生产靠预下载的模块缓存离线构建，新增依赖需管理员先在镜像里预下载模块；所以不要让任务修改 go.mod/go.sum。

完成标准：gofmt -l . 无输出，go vet ./... 和 go test -count=1 ./... 通过。不改其它文件。
