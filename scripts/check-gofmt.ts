// 提交前查 Go 文件有没有 gofmt（lefthook.yml 的"Go格式"，在仓库根跑，参数是暂存的 .go 文件，路径从仓库根算，如 native/internal/turn/hub.go）。
// 原来直接在 lefthook.yml 里写 test -z "$(gofmt -l …)"，Windows 上 lefthook 拆参数会把引号拆坏、每次都红；改成脚本，不靠 shell 的引号。
// 查的是暂存区里的内容（git show :<文件>，从标准输入交给 gofmt），不是工作区的文件：这台机器开着 core.autocrlf，
// 签出来的 .go 是 CRLF，gofmt -l 会把格式没问题的文件全报出来（2026-10-26 mygo-v2 合 main 时撞上）；暂存区里存的是 LF，也正是要提交的内容。
// 必须在仓库根跑：worktree 的钩子里 git 设了 GIT_DIR、没设 GIT_WORK_TREE，当前目录就被当成工作区的根；主仓库的钩子里 GIT_INDEX_FILE 是相对路径 .git/index。
// gofmt -l 有问题时退出码照样是 0，所以要看它有没有输出。
//   bun scripts/check-gofmt.ts native/internal/tools/shell.go …     手动试（在仓库根，文件要先 git add）
const files = process.argv.slice(2)
if (files.length === 0) process.exit(0)
const bad: string[] = []
for (const f of files) {
  const blob = Bun.spawnSync(['git', 'show', `:${f.replace(/^\.\//, '')}`])
  if (blob.exitCode !== 0) {
    console.log(`读不到暂存区里的 ${f}：${blob.stderr.toString().trim()}`)
    process.exit(1)
  }
  const p = Bun.spawnSync(['gofmt', '-l'], { stdin: blob.stdout })
  if (p.exitCode !== 0) {
    console.log(`gofmt 自己报错了（${f}，退出码 ${p.exitCode}）：\n${p.stderr.toString().trim()}`)
    process.exit(1)
  }
  if (p.stdout.toString().trim()) bad.push(f)
}
if (bad.length) {
  console.log(`这些文件没 gofmt：\n${bad.join('\n')}`)
  process.exit(1)
}
export {}
