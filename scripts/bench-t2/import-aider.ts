// 表二 B1 的 Aider Go 题导入（规格-表二执行器 第六节）：从 Aider-AI/polyglot-benchmark 的浅克隆里挑 12 道 Go 题，
// 拷进 scripts/bench-t2/tasks/b1-a<序号>-<题名>/。挑法固定：按题名的 SHA-256 排序取前 n 个（不挑软柿子、换机器也一样）。
// 导入前拿 .meta 里的参考解验一遍：桩代码 go test 必须红、换上参考解必须绿；验完拷进题库时删掉 .meta（参考解）和 .approaches、.articles（解法讲解），留着等于泄题，
// task.json 记下仓库提交号和每个文件的 SHA-256；判分只许动 .go 文件（可以新建辅助文件），测试文件、go.mod 不许动。题目版权归 Exercism（exercism/go 是 MIT）。
//   bun scripts/bench-t2/import-aider.ts <polyglot-benchmark 克隆目录> [题数=12]
import { createHash } from 'node:crypto'
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'

const [clone, countArg] = process.argv.slice(2)
if (!clone) throw new Error('要给 polyglot-benchmark 的克隆目录')
const count = Number(countArg ?? 12)
const practice = join(clone, 'go', 'exercises', 'practice')
const tasksDir = join(import.meta.dir, 'tasks')
const scratch = process.env.BENCH_SCRATCH
if (!scratch) throw new Error('要给 BENCH_SCRATCH（验参考解用的临时目录，放会话的 scratchpad）')

const git = (...args: string[]) => {
  const p = Bun.spawnSync(['git', '-C', clone, ...args])
  if (p.exitCode !== 0) throw new Error(`git ${args.join(' ')}：${p.stderr}`)
  return p.stdout.toString().trim()
}
const commit = git('rev-parse', 'HEAD')
const sha = (s: string | Buffer) => createHash('sha256').update(s).digest('hex')

const walk = (dir: string): string[] =>
  readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })

const goTest = (dir: string) => {
  const p = Bun.spawnSync(['go', 'test', './...'], { cwd: dir, env: { ...process.env, GOFLAGS: '-count=1' } })
  return { ok: p.exitCode === 0, out: (p.stdout.toString() + p.stderr.toString()).slice(-600) }
}

// 不拷的：.meta（参考解）、.approaches 和 .articles（Exercism 的解法讲解和代码片段，个别题带），留着都等于泄题
const hidden = ['.meta', '.approaches', '.articles']
const keep = (p: string) => !p.split(sep).some((s) => hidden.includes(s))

const names = readdirSync(practice).filter((n) => existsSync(join(practice, n, '.meta', 'config.json')))
const order = names.map((n) => ({ n, h: sha(n) })).sort((a, b) => a.h.localeCompare(b.h)).map((x) => x.n)
console.log(`Go 题 ${names.length} 道，按题名 SHA-256 排，验得过的取前 ${count} 道（提交 ${commit}）`)

let taken = 0
for (const name of order) {
  if (taken === count) break
  const src = join(practice, name)
  const meta = JSON.parse(readFileSync(join(src, '.meta', 'config.json'), 'utf8'))
  const solution: string[] = meta.files.solution
  const example: string[] = meta.files.example
  if (example.length !== 1 || solution.length !== 1) {
    console.log(`  跳过 ${name}：参考解 / 解答文件不止一个（${example} → ${solution}）`)
    continue
  }
  // 验：桩代码红、参考解绿（验不过的跳过、顺延下一道，跳过的照实打出来）
  const tmp = mkdtempSync(join(scratch, `aider-${name}-`))
  cpSync(src, tmp, { recursive: true, filter: keep })
  const stub = goTest(tmp)
  cpSync(join(src, example[0]), join(tmp, solution[0]))
  const ref = goTest(tmp)
  rmSync(tmp, { recursive: true, force: true })
  if (stub.ok || !ref.ok) {
    console.log(`  跳过 ${name}：桩代码 ${stub.ok ? '绿' : '红'}、参考解 ${ref.ok ? '绿' : '红'}\n${ref.out}`)
    continue
  }
  taken++
  const id = `b1-a${String(taken).padStart(2, '0')}-${name}`
  const dst = join(tasksDir, id)
  rmSync(dst, { recursive: true, force: true })
  cpSync(src, join(dst, 'files'), { recursive: true, filter: keep })
  const files = Object.fromEntries(
    walk(join(dst, 'files')).map((p) => [relative(join(dst, 'files'), p).split(sep).join('/'), sha(readFileSync(p))]),
  )
  const say =
    `工作目录里是一道 Go 练习题，说明在 .docs/ 里。请修改 ${solution.join('、')} 实现它（需要的话可以新建 .go 文件放辅助代码），让 go test ./... 通过。` +
    `不要改或新建测试文件（_test.go），不要改 go.mod，不要改已有函数和类型的名字，只用标准库。`
  const task = {
    id,
    group: 'b1',
    source: `Aider-AI/polyglot-benchmark@${commit} go/exercises/practice/${name}（Exercism，MIT）`,
    steps: [{ say }],
    // 只许动 .go 文件（新建辅助文件也行），测试文件、go.mod 一动就算违反；go test -c 之类的编译产物不算改动
    judge: { goTest: true, allowed: ['*.go'], forbid: ['*_test.go', 'go.mod'], ignore: ['*.test', '*.exe', '*.out', '*.prof'] },
    sha256: files,
  }
  mkdirSync(dst, { recursive: true })
  writeFileSync(join(dst, 'task.json'), JSON.stringify(task, null, 2) + '\n')
  console.log(`  ✓ ${id}：桩代码红、参考解绿，${Object.keys(files).length} 个文件`)
}
if (taken < count) {
  console.log(`只验过 ${taken} 道，不够 ${count} 道`)
  process.exit(1)
}
