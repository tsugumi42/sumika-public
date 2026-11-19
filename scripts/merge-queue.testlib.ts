// merge-queue 测试共用的准备：临时目录、全局 git 配置、建仓库建分支的小工具、本地裸仓库当远端、假的 GitHub。三个测试文件都从这里引
// （2026-11-12 拆成三个文件，让 bun test --parallel 按文件分进程跑）。
// 在测试文件顶层 import 它，beforeAll / afterAll 就登记到那个文件上。
import { afterAll, beforeAll, setDefaultTimeout } from 'bun:test'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { git, makeCtx, type Check, type Ctx, type Hub, type Pr } from './merge-queue'

// 所有测试都在临时 git 仓库里：全局配置也指到临时文件（主人的全局配置有 autocrlf=true，可能还有签名、钩子目录），绝不碰真实仓库
// 每个测试文件顶层调一次 useQueueTestEnv()：各自登记 beforeAll / afterAll、各自一个临时目录。
// 不在模块顶层登记：不加 --parallel 时几个文件在同一个进程里跑，模块只加载一次，钩子只会挂在第一个文件上，
// 后面的文件就会在没指到临时全局配置的环境里跑。root 是活绑定，每个文件的 beforeAll 换成自己的目录
export let root = ''
export function useQueueTestEnv() {
  setDefaultTimeout(120000)   // 每条都要起好几个 git 进程，Windows 上慢
  const saved: Record<string, string | undefined> = {}
  let mine = ''
  beforeAll(() => {
    mine = root = mkdtempSync(join(tmpdir(), 'merge-queue-test-'))
    writeFileSync(join(root, 'gitconfig'), '[user]\n\tname = t\n\temail = t@example.invalid\n[core]\n\tautocrlf = false\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n')
    for (const [k, v] of Object.entries({ GIT_CONFIG_GLOBAL: join(root, 'gitconfig'), GIT_CONFIG_NOSYSTEM: '1', SUMIKA_QUEUE_STATE: '', SUMIKA_QUEUE_REMOTE: '', SUMIKA_QUEUE_REPO: '' })) { saved[k] = process.env[k]; process.env[k] = v }
  })
  afterAll(() => {
    for (const [k, v] of Object.entries(saved)) { if (v === undefined) delete process.env[k]; else process.env[k] = v }
    rmSync(mine, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
  })
}

// 起进程一律显式传 env，去掉 GIT_DIR 这些：钩子里跑时临时仓库的 git init 会落到真仓库上（2026-11-11 隐私扫描的测试出过一次，core.bare 被改成 true）。
// 只 delete process.env 不够：Bun 里父进程传进来的变量删了以后，不传 env 的子进程照样看得到
export const cleanEnv = () => Object.fromEntries(Object.entries(process.env).filter(([k]) => !/^GIT_(DIR|WORK_TREE|INDEX_FILE|PREFIX|OBJECT_DIRECTORY|ALTERNATE_OBJECT_DIRECTORIES|COMMON_DIR)$/.test(k))) as Record<string, string>

let n = 0
export const sh = (cwd: string, ...a: string[]) => { const r = git(cwd, a); if (r.code !== 0) throw new Error(`git ${a.join(' ')}：${r.err}`); return r.out }
export function mkRepo(): Ctx {
  const dir = join(root, `repo${++n}`)
  Bun.spawnSync(['git', 'init', '-q', '-b', 'main', dir], { env: cleanEnv() })
  writeFileSync(join(dir, 'base.txt'), 'line1\nline2\nline3\n')
  sh(dir, 'add', '.'); sh(dir, 'commit', '-qm', 'init')
  return makeCtx(dir)
}
// 带远端的仓库：本地裸仓库当 origin（代替 GitHub 上的公开仓库），main 已推上去
export function mkRepoWithRemote(): Ctx {
  const c = mkRepo()
  const bare = join(root, `remote${n}.git`)
  Bun.spawnSync(['git', 'init', '-q', '--bare', '-b', 'main', bare], { env: cleanEnv() })
  sh(c.mainDir, 'remote', 'add', 'origin', bare)
  sh(c.mainDir, 'push', '-q', 'origin', 'main')
  sh(c.mainDir, 'fetch', '-q', 'origin')
  return { ...c, repo: 'test/sumika' }
}
// 在主工作区里建一个分支提交一个文件，回到 main
export function mkBranch(c: Ctx, name: string, file: string, content: string) {
  sh(c.mainDir, 'checkout', '-q', '-b', name, 'main')
  writeFileSync(join(c.mainDir, file), content)
  sh(c.mainDir, 'add', '.'); sh(c.mainDir, 'commit', '-qm', `${name} 提交`)
  sh(c.mainDir, 'checkout', '-q', 'main')
  return sh(c.mainDir, 'rev-parse', name)
}
export const mainSha = (c: Ctx) => sh(c.mainDir, 'rev-parse', 'main')
export const remoteSha = (c: Ctx, ref = 'main') => sh(c.mainDir, 'ls-remote', 'origin', `refs/heads/${ref}`).split(/\s/)[0]
export const quiet = { log: () => {} }

export const WIN = 'test (windows-latest)'
export const LINUX = 'test (ubuntu-latest)'
export const ok = (name: string, at = '2026-11-19T01:00:00Z'): Check => ({ name, done: true, conclusion: 'SUCCESS', url: `https://example.invalid/run/${encodeURIComponent(name)}`, at })
export const bad = (name: string, conclusion = 'FAILURE', at = '2026-11-19T01:00:00Z'): Check => ({ name, done: true, conclusion, url: `https://example.invalid/run/${encodeURIComponent(name)}`, at })
export const running = (name: string, at = '2026-11-19T01:00:00Z'): Check => ({ name, done: false, conclusion: '', url: '', at })
export const pr = (over: Partial<Pr> = {}): Pr => ({ number: 7, state: 'OPEN', head: 'a'.repeat(40), mergeState: 'CLEAN', url: 'https://example.invalid/pull/7', checks: [ok(WIN)], ...over })

// 在远端（裸仓库）上做事：克隆到临时目录、做完推回去。代替 GitHub 网页上的合并、“更新分支”
function onRemote(c: Ctx, fn: (dir: string) => void) {
  const dir = mkdtempSync(join(root, 'gh-'))
  try {
    Bun.spawnSync(['git', 'clone', '-q', sh(c.mainDir, 'remote', 'get-url', 'origin'), dir], { env: cleanEnv() })
    fn(dir)
  } finally { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }) }
}

// 假的 GitHub：PR 按分支名存；合并、更新分支真的改裸仓库，让本机对齐那一段走真的 git
export class FakeHub implements Hub {
  prs = new Map<string, Pr>()
  requiredNames: string[] = [WIN]
  requiredErr = ''
  findErr = ''
  merges: { number: number; head: string; subject: string; body: string }[] = []
  updates: number[] = []
  private next = 1
  constructor(private c: Ctx) {}

  // 把本地分支推上去、开 PR
  open(branch: string, checks: Check[] = [ok(WIN)], mergeState = 'CLEAN') {
    sh(this.c.mainDir, 'push', '-q', 'origin', `${branch}:${branch}`)
    const number = this.next++
    const p: Pr = { number, state: 'OPEN', head: remoteSha(this.c, branch), mergeState, url: `https://example.invalid/pull/${number}`, checks }
    this.prs.set(branch, p)
    return p
  }
  set(branch: string, over: Partial<Pr>) { Object.assign(this.prs.get(branch)!, over) }

  findPr(branch: string) {
    if (this.findErr) throw new Error(this.findErr)
    const p = this.prs.get(branch)
    return p ? { ...p, checks: [...p.checks] } : null
  }
  required() {
    if (this.requiredErr) throw new Error(this.requiredErr)
    return [...this.requiredNames]
  }
  updateBranch(p: Pr) {
    this.updates.push(p.number)
    const [branch, cur] = [...this.prs].find(([, x]) => x.number === p.number)!
    onRemote(this.c, (d) => {
      sh(d, 'checkout', '-q', branch)
      sh(d, 'merge', '-q', '--no-ff', '-m', `Merge branch 'main' into ${branch}`, 'origin/main')
      sh(d, 'push', '-q', 'origin', branch)
    })
    Object.assign(cur, { head: remoteSha(this.c, branch), mergeState: 'BLOCKED', checks: [running(WIN, '2026-11-19T02:00:00Z')] })
  }
  merge(p: Pr, subject: string, body: string) {
    const [branch, cur] = [...this.prs].find(([, x]) => x.number === p.number)!
    if (cur.head !== p.head) return { ok: false, err: 'Head branch was modified. Review and try the merge again.' }
    let commit = ''
    onRemote(this.c, (d) => {
      writeFileSync(join(d, '.msg'), `${subject}\n\n${body}\n`)
      sh(d, 'merge', '-q', '--no-ff', '-F', '.msg', `origin/${branch}`)
      sh(d, 'push', '-q', 'origin', 'main', `:${branch}`)
      commit = sh(d, 'rev-parse', 'HEAD')
    })
    this.merges.push({ number: p.number, head: p.head, subject, body })
    cur.state = 'MERGED'
    // 别的 PR 跟着落后 main（分支保护要求分支是最新的）
    for (const x of this.prs.values()) if (x.state === 'OPEN') x.mergeState = 'BEHIND'
    return { ok: true, err: '', commit }
  }
}
