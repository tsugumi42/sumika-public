// 提交说明的标题检查（lefthook.yml 的 commit-msg，参数是 git 给的说明文件路径）：上级 AGENTS「提交」第 1 条，标题 30 个汉字左右、只写一件事、不用分号。
// 主人 2026-10-27 嫌标题和说明太长、"很多有的没的也写进去了"，光靠记守不住，所以拦标题；正文写什么、不写什么靠第 2 条，这里不查。
// 字数和分号只查"类型(模块): "后面那段，按 Unicode 码点数；ADR 编号整行都查（模块名里也不写 ADR 号）。合并、回退（Merge、Revert）和 fixup! / squash! 不查。
//   bun scripts/check-commit-msg.ts .git/COMMIT_EDITMSG     手动试
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const MAX = 40
const arg = process.argv[2]
if (!arg) { console.log('用法：check-commit-msg.ts <说明文件>'); process.exit(2) }
// lefthook 把 git 给的路径不加引号拼进 sh 命令：在 omp 这类环境里 git 给的是 D:\… 反斜杠绝对路径，反斜杠被 sh 吃掉，
// 参数就成了不存在的路径（2026-11-15 连着五次拦住代理提交）。这时问 git 这个仓库（含 worktree）的说明文件在哪，照样检查；
// 两边都找不到就报错退出——绝不在拿不到说明时放行
function messageFile(p: string): string | null {
  if (existsSync(p)) return p
  // 被吃掉的路径结尾还留着文件名：合并提交是 MERGE_MSG（这时 COMMIT_EDITMSG 里是上一次的旧说明，不能拿来查），
  // 普通提交是 COMMIT_EDITMSG；两者都不是就不认（复核 2026-11-16）
  const name = /MERGE_MSG$/.test(p) ? 'MERGE_MSG' : /COMMIT_EDITMSG$/.test(p) ? 'COMMIT_EDITMSG' : ''
  // omp 的提交助手把说明写在 .git/worktrees/<名字>/.omp-commit-msg-<pid>-<时间>-0，路径被 sh 吃掉以后
  // 结尾还留着这个文件名；按结尾去 git 目录里找，找到就照常检查（2026-11-17，worktree 里连续拦住提交）
  const omp = /\.?omp-commit-msg-[0-9-]+$/.exec(p)
  if (omp) {
    const d = Bun.spawnSync(['git', 'rev-parse', '--git-dir'])
    const dir = d.exitCode === 0 ? d.stdout.toString().trim() : ''
    const f = dir ? join(dir, omp[0].startsWith('.') ? omp[0] : '.' + omp[0]) : ''
    if (f && existsSync(f)) return f
  }
  if (!name) return null
  const r = Bun.spawnSync(['git', 'rev-parse', '--git-path', name])
  const q = r.exitCode === 0 ? r.stdout.toString().trim() : ''
  return q && existsSync(q) ? q : null
}
const file = messageFile(arg)
if (!file) { console.log(`读不到提交说明：参数 ${arg} 不存在，git 也报不出说明文件。没检查，按失败处理`); process.exit(2) }
// 说明文件里 # 开头的是 git 的注释，不算
const title = readFileSync(file, 'utf8').split(/\r?\n/).find((l) => l.trim() && !l.startsWith('#'))?.trim() ?? ''
// “合并 ”是合并队列写的标题（“合并 <分支名>”），分支名里带 adr-359 这类编号时以前会被下面的 ADR 检查拦住、合并失败（2026-11-12）
if (/^(Merge |Revert |fixup! |squash! |合并 )/.test(title)) process.exit(0)
const body = title.replace(/^[a-z]+(\([^)]*\))?!?:\s*/, '')
const bad: string[] = []
const n = [...body].length
if (n > MAX) bad.push(`标题 ${n} 个字，超过 ${MAX}：只写一件事、写"现在会怎样"，编号、理由、数字放正文`)
if (/[；;]/.test(body)) bad.push('标题里有分号：一个提交只做一件事，两件就拆开')
if (/ADR-?\d/i.test(title)) bad.push('标题里有 ADR 编号：编号放正文第一行，标题写现在会怎样（docs/archive/notes/对照-提交说明写法-2026-10-27.md）')
if (bad.length) {
  console.log(`提交说明的标题：${title}\n${bad.map((b) => '  ' + b).join('\n')}`)
  process.exit(1)
}
