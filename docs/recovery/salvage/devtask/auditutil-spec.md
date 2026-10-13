【命令规则】run 工具只能执行下面这几条命令，字符必须完全一致，不能加包路径或其他参数：
- gofmt -l .
- go build ./...
- go vet ./...
- go test ./...
- go test -count=1 ./...
读写文件只能在 internal/auditutil 目录内。

修复 internal/auditutil 的两个审计脱敏缺陷，并补测试。

缺陷 1（安全）：单独出现的 bearer 令牌（前面没有 Authorization:，例如 curl -H "Bearer abc123" 或日志里 Bearer eyJ...）不会被脱敏。
要求：任意位置的 bearer + 空白 + 令牌（不区分大小写）都把令牌替换为 [REDACTED]（保留原 bearer 写法），令牌在空白、逗号、分号、引号处结束。已有 Authorization: Bearer x、token=x、api_key: x 等行为保持不变，且结果不能出现重复或嵌套的 [REDACTED]。

缺陷 2：Redact 在 8192 字节处直接切片，可能切断多字节 UTF-8 字符。
要求：上限仍为 8192 字节，但向前退到最近的完整字符边界；结果 utf8.ValidString 为真且 <= 8192 字节。不要改成按字符数计。

测试（新建 internal/auditutil/redact_fix_test.go）：
- 单独 bearer 令牌（大小写变体、在引号内、在句中、后跟逗号）被脱敏，原令牌不再出现。
- Authorization: Bearer 形式结果不含重复 [REDACTED]。
- 8191 个 ASCII + 一个 3 字节汉字：结果合法 UTF-8 且 <= 8192 字节。
- 恰好 8192 字节纯 ASCII 原样保留。
- Truncate 行为不变。

完成标准：gofmt -l . 无输出；go vet ./... 和 go test -count=1 ./... 通过。不要改其他包。
