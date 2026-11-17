// 提交说明标题检查：合并队列的“合并 <分支名>”不查（分支名里带 ADR 编号也放行），普通标题照查
import { afterAll, expect, test } from 'bun:test'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const dir = mkdtempSync(join(tmpdir(), 'commit-msg-'))
afterAll(() => rmSync(dir, { recursive: true, force: true }))
let n = 0
const check = (msg: string) => {
  const f = join(dir, `msg${++n}.txt`)
  writeFileSync(f, msg)
  return Bun.spawnSync([process.execPath, join(import.meta.dir, 'check-commit-msg.ts'), f]).exitCode
}

test('合并队列的标题带 ADR 编号的分支名也放行', () => {
  expect(check('合并 docs/adr-359-go-mygo\n\n正文\n')).toBe(0)
  expect(check('Merge branch \'main\' into x\n')).toBe(0)
})

test('普通标题里有 ADR 编号、分号、太长照样拦', () => {
  expect(check('ADR-359：后台回到 Go\n')).toBe(1)
  expect(check('改了一处；又改一处\n')).toBe(1)
  expect(check('一'.repeat(41) + '\n')).toBe(1)
  expect(check('后台回到 Go、界面留 mygo\n')).toBe(0)
})

// 测试里起 git：环境里的 GIT_* 一律清掉（LESSONS：钩子里继承的 GIT_DIR 会让 git 改到真仓库）
// GIT_CEILING_DIRECTORIES 放在清完之后：git 不许往临时目录上面找仓库，免得撞上祖先目录里别的仓库
const cleanEnv = () => ({ ...Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_'))), GIT_CEILING_DIRECTORIES: tmpdir() })
const tempRepo = (title: string) => {
  const repo = mkdtempSync(join(tmpdir(), 'commit-msg-repo-'))
  Bun.spawnSync(['git', 'init', '-q', repo], { env: cleanEnv() })
  writeFileSync(join(repo, '.git', 'COMMIT_EDITMSG'), title)
  return repo
}
const checkIn = (cwd: string, arg: string) => Bun.spawnSync([process.execPath, join(import.meta.dir, 'check-commit-msg.ts'), arg], { cwd, env: cleanEnv() }).exitCode

test('参数路径的反斜杠被吃掉时，改读 git 报的说明文件并照样检查', () => {
  const bad = tempRepo('ADR-359：标题带编号\n')
  const good = tempRepo('后台回到 Go\n')
  try {
    expect(checkIn(bad, 'D:ProjectsCodesumika.gitworktreesxCOMMIT_EDITMSG')).toBe(1)
    expect(checkIn(good, 'D:ProjectsCodesumika.gitworktreesxCOMMIT_EDITMSG')).toBe(0)
  } finally { rmSync(bad, { recursive: true, force: true }); rmSync(good, { recursive: true, force: true }) }
}, 30_000) // 起两次 git init、四次 bun 子进程，本机起进程慢（安全软件会拦），没有事件可等，只放宽上限

test('参数路径不存在、git 也报不出说明文件：失败，不放行', () => {
  const notRepo = mkdtempSync(join(tmpdir(), 'commit-msg-norepo-'))
  try {
    expect(checkIn(notRepo, 'D:ProjectsCodesumika.gitworktreesxCOMMIT_EDITMSG')).toBe(2) // 结尾对，但这里不是仓库
    expect(checkIn(notRepo, 'nowhere.txt')).toBe(2) // 结尾既不是 COMMIT_EDITMSG 也不是 MERGE_MSG
  } finally { rmSync(notRepo, { recursive: true, force: true }) }
}, 30_000)

test('omp 的临时说明文件路径被吃掉时，按文件名回 git 目录找到它照样检查；找不到不放行', () => {
  const repo = tempRepo('后台回到 Go\n') // 上一次留下的合格标题：读错文件就会误放行
  writeFileSync(join(repo, '.git', '.omp-commit-msg-4242-1763390000-0'), 'ADR-359：标题带编号\n')
  writeFileSync(join(repo, '.git', '.omp-commit-msg-4242-1763390001-0'), '后台回到 Go\n')
  try {
    expect(checkIn(repo, 'D:ProjectsCodesumika.gitworktreesx.omp-commit-msg-4242-1763390000-0')).toBe(1)
    expect(checkIn(repo, 'D:ProjectsCodesumika.gitworktreesx.omp-commit-msg-4242-1763390001-0')).toBe(0)
    expect(checkIn(repo, 'D:ProjectsCodesumika.gitworktreesx.omp-commit-msg-9999-1-0')).toBe(2) // 没有这个文件：不拿 COMMIT_EDITMSG 顶替
  } finally { rmSync(repo, { recursive: true, force: true }) }
}, 30_000)

test('合并提交的路径被吃掉时读 MERGE_MSG，不拿上一次的 COMMIT_EDITMSG 来查', () => {
  const repo = tempRepo('后台回到 Go\n') // 上一次普通提交留下的合格标题
  writeFileSync(join(repo, '.git', 'MERGE_MSG'), '后台改了；又改了\n') // 这次合并的标题带分号，该拦
  try {
    expect(checkIn(repo, 'D:ProjectsCodesumika.gitworktreesxMERGE_MSG')).toBe(1)
  } finally { rmSync(repo, { recursive: true, force: true }) }
}, 30_000)
