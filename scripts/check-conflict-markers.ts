// 提交前查暂存区新增的行里有没有没解决的合并冲突标记（lefthook.yml 的"冲突标记"，LESSONS「合并解决冲突以后」那条）。
// 2026-10-30 合 ui-go-2 时 80062b2c 把待办和装配规格里各一处冲突连标记一起提交了，check:docs 不查这种标记。
// 只看 git diff --cached 加进来的行：二进制文件自动跳过，也不用经 lefthook 传文件名（Windows 上会拆坏引号，见 check-gofmt.ts）。
// <<<<<<< 和 >>>>>>> 单独出现就算；=======、|||||||（diff3）可能是 Markdown 标题下划线这类正常内容，同一个文件里还有前两种才算。
//   bun scripts/check-conflict-markers.ts     手动试（在仓库根，文件要先 git add）
const diff = Bun.spawnSync(['git', '-c', 'core.quotepath=false', 'diff', '--cached', '-U0', '--no-color', '--no-ext-diff', '--diff-filter=ACMR'])
if (diff.exitCode !== 0) {
  console.log(`读不到暂存区的改动：${diff.stderr.toString().trim()}`)
  process.exit(1)
}
const strong = /^(<{7}|>{7})( |\r?$)/
const weak = /^(={7}|\|{7})( |\r?$)/
const hits = new Map<string, { strong: boolean; lines: string[] }>()
let file = ''
let line = 0
let header = false // diff --git 到第一个 @@ 之间是文件头；新增的行本身以 "++ " 开头时不能当成文件头
for (const raw of diff.stdout.toString().split('\n')) {
  if (raw.startsWith('diff --git ')) {
    header = true
    continue
  }
  if (header) {
    if (raw.startsWith('+++ ')) file = raw.replace(/^\+\+\+ (b\/)?/, '')
    if (!raw.startsWith('@@')) continue
    header = false
  }
  const hunk = /^@@ -\S+ \+(\d+)/.exec(raw)
  if (hunk) {
    line = Number(hunk[1])
    continue
  }
  if (!raw.startsWith('+')) continue
  const text = raw.slice(1)
  const isStrong = strong.test(text)
  if (isStrong || weak.test(text)) {
    const h = hits.get(file) ?? { strong: false, lines: [] }
    h.strong ||= isStrong
    h.lines.push(`${file}:${line}: ${text.trimEnd()}`)
    hits.set(file, h)
  }
  line++
}
const bad = [...hits.values()].filter((h) => h.strong).flatMap((h) => h.lines)
if (bad.length) {
  console.log(`这些地方还有没解决的合并冲突：\n${bad.join('\n')}`)
  process.exit(1)
}
export {}
