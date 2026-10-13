#!/bin/sh
# 交付前自检。用法：sh docs/process/check-task.sh <任务编号> [基线commit]
# 检查：任务书存在、改动在允许范围内、未碰禁止路径、规模上限、git diff --check、项目验收脚本。
# 副本由 dev-process/sync.sh 生成，勿直接修改。
set -u
id=${1:?用法: check-task.sh <任务编号> [基线commit]}
root=$(git rev-parse --show-toplevel) || exit 2
cd "$root"
card="docs/tasks/$id.md"
fail=0
ok()  { echo "✓ $*"; }
bad() { echo "✗ $*"; fail=1; }

[ -f "$card" ] || { echo "✗ 找不到任务书 $card"; exit 1; }
ok "任务书 $card"

base=${2:-}
if [ -z "$base" ]; then
  base=$(grep -m1 '^分支' "$card" | grep -oE '[0-9a-f]{7,40}' | head -n1)
fi
[ -n "$base" ] && git rev-parse -q --verify "$base^{commit}" >/dev/null || { echo "✗ 无法确定基线 commit（任务书「分支」行写明 sha，或作为第二个参数传入）"; exit 1; }
# 未显式传基线时：任务书、看板等通常在基线之后才提交到 main，分支从更新后的 main 分出；
# 若本分支与 main 的分叉点晚于任务书基线，从分叉点开始算改动（只算本分支自己的提交）
if [ -z "${2:-}" ] && git rev-parse -q --verify main >/dev/null; then
  fork=$(git merge-base HEAD main)
  if [ -n "$fork" ] && git merge-base --is-ancestor "$base" "$fork" && [ "$(git rev-parse "$fork")" != "$(git rev-parse HEAD)" ]; then
    base=$(git rev-parse --short "$fork")
  fi
fi
ok "基线 $base"

scope=$(grep -m1 '^改动范围' "$card")
allowed=$(printf '%s\n' "$scope" | sed -n 's/.*允许[：: ]*\([^；;]*\).*/\1/p')
forbidden=$(printf '%s\n' "$scope" | sed -n 's/.*禁止[：: ]*\([^；;]*\).*/\1/p')
[ -n "$allowed" ] || bad "任务书「改动范围」没写允许路径"

if grep -m1 '^开发者' "$card" | grep -qi flash; then maxl=300; maxf=6; else maxl=600; maxf=12; fi

changes=$(git diff --numstat "$base"...HEAD; git diff --numstat HEAD)
files=$(git diff --name-only "$base"...HEAD; git diff --name-only HEAD; git ls-files --others --exclude-standard)
files=$(printf '%s\n' "$files" | sed '/^$/d' | sort -u)
[ -n "$files" ] || bad "相对基线没有任何改动"
[ -z "$(git ls-files --others --exclude-standard)" ] || bad "有未跟踪文件未提交：$(git ls-files --others --exclude-standard | tr '\n' ' ')"
[ -z "$(git status --porcelain --untracked-files=no)" ] || bad "有未提交的修改"

out=0
for f in $files; do
  hit=0
  for p in $allowed; do case "$f" in "$p"*) hit=1;; esac; done
  [ $hit = 1 ] || { bad "越界：$f 不在允许范围（$allowed）"; out=1; }
  for p in $forbidden; do case "$f" in "$p"*) bad "触碰禁止路径：$f"; out=1;; esac; done
done
[ $out = 1 ] || ok "改动都在允许范围内"

is_test() { case "$1" in *_test.*|*/test_*|test_*|*.test.*|*/testdata/*|testdata/*|*/tests/*|tests/*) return 0;; esac; return 1; }
lines=0; nf=0
for f in $files; do
  is_test "$f" && continue
  nf=$((nf+1))
  n=$(printf '%s\n' "$changes" | awk -v f="$f" '$3==f && $1!="-" {s+=$1+$2} END{print s+0}')
  lines=$((lines+n))
done
if [ "$lines" -le "$maxl" ] && [ "$nf" -le "$maxf" ]; then ok "规模：非测试代码 $lines 行 / $nf 个文件（上限 $maxl / $maxf）"
else bad "规模超限：非测试代码 $lines 行 / $nf 个文件（上限 $maxl / $maxf），须拆分或任务书注明 OPC 同意"; fi

if git diff --check "$base"...HEAD >/dev/null 2>&1; then ok "git diff --check"; else bad "git diff --check 有空白问题"; git diff --check "$base"...HEAD | head -n 20; fi

if [ -f docs/process/acceptance.sh ]; then
  if sh docs/process/acceptance.sh; then ok "验收命令（docs/process/acceptance.sh）"; else bad "验收命令失败（docs/process/acceptance.sh）"; fi
else
  echo "! 未找到 docs/process/acceptance.sh，请手动执行项目档案中的验收命令"
fi

[ $fail = 0 ] && { echo "== 全部通过，可以提交审核"; exit 0; }
echo "== 未通过，修好再交付"; exit 1
