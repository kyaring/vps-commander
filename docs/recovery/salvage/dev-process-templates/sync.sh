#!/bin/sh
# 用法：sync.sh <项目仓库路径>
# 同步到项目：docs/process/DEV-PROCESS.md、docs/process/check-task.sh、AGENTS.md 中的规则段、CLAUDE.md 指针。
set -eu
src=$(cd "$(dirname "$0")" && pwd)
dst=${1:?project repo path}
[ -z "$(git -C "$src" status --porcelain)" ] || { echo "dev-process 有未提交的修改，先提交" >&2; exit 1; }
rev=$(git -C "$src" log -1 --format=%h)
hdr="副本，勿直接修改。正本：dev-process@$rev"
mkdir -p "$dst/docs/process"

{ printf '<!-- %s（%s/DEV-PROCESS.md） -->\n\n' "$hdr" "$src"; cat "$src/DEV-PROCESS.md"; } > "$dst/docs/process/DEV-PROCESS.md"
{ head -n1 "$src/templates/check-task.sh"; printf '# %s\n' "$hdr"; tail -n +2 "$src/templates/check-task.sh"; } > "$dst/docs/process/check-task.sh"
chmod 0755 "$dst/docs/process/check-task.sh"

# AGENTS.md：替换 dev-process:begin/end 之间的规则段；没有就放到文件开头，项目自己的内容保留在后面
blk=$(mktemp)
{ printf '<!-- dev-process:begin %s -->\n' "$hdr"; cat "$src/templates/AGENTS-BLOCK.md"; printf '<!-- dev-process:end -->\n'; } > "$blk"
a="$dst/AGENTS.md"
if [ -f "$a" ] && grep -q '<!-- dev-process:begin' "$a"; then
  awk -v blk="$blk" '/<!-- dev-process:begin/{while((getline l < blk)>0) print l; skip=1; next} /<!-- dev-process:end -->/{skip=0; next} !skip' "$a" > "$a.tmp" && mv "$a.tmp" "$a"
elif [ -f "$a" ]; then
  { cat "$blk"; echo; cat "$a"; } > "$a.tmp" && mv "$a.tmp" "$a"
else
  cp "$blk" "$a"
fi
rm -f "$blk"

# CLAUDE.md：只在不存在时创建，指向 AGENTS.md
[ -e "$dst/CLAUDE.md" ] || printf '@AGENTS.md\n\n本项目的 agent 规则全部在 AGENTS.md，开工前先读。\n' > "$dst/CLAUDE.md"

echo "synced dev-process@$rev -> $dst（DEV-PROCESS.md、check-task.sh、AGENTS.md 规则段、CLAUDE.md）"
