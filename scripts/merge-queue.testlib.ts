// merge-queue 测试共用的准备：临时目录、全局 git 配置、建仓库建分支的小工具。三个测试文件都从这里引
// （2026-11-12 拆成三个文件，让 bun test --parallel 按文件分进程跑：原来一个文件 16 条串行要 70 秒）。
// 在测试文件顶层 import 它，beforeAll / afterAll 就登记到那个文件上。
import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { acquireRunner, addEntry, conclusionOf, git, loadState, makeCtx, mutate, processHead, removeEntry, runnerAlive, runQueue, saveState, statusText, type Ctx, type Verify } from './merge-queue'

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
    for (const [k, v] of Object.entries({ GIT_CONFIG_GLOBAL: join(root, 'gitconfig'), GIT_CONFIG_NOSYSTEM: '1', SUMIKA_QUEUE_STATE: '', SUMIKA_QUEUE_WORKTREE: '', SUMIKA_QUEUE_OUT: '', SUMIKA_QUEUE_VERIFY: '' })) { saved[k] = process.env[k]; process.env[k] = v }
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
// 在主工作区里建一个分支提交一个文件，回到 main
export function mkBranch(c: Ctx, name: string, file: string, content: string) {
  sh(c.mainDir, 'checkout', '-q', '-b', name, 'main')
  writeFileSync(join(c.mainDir, file), content)
  sh(c.mainDir, 'add', '.'); sh(c.mainDir, 'commit', '-qm', `${name} 提交`)
  sh(c.mainDir, 'checkout', '-q', 'main')
  return sh(c.mainDir, 'rev-parse', name)
}
export const mainSha = (c: Ctx) => sh(c.mainDir, 'rev-parse', 'main')
export const quiet = { log: () => {}, pollMs: 10, waitCleanMs: 10 }
export const green: Verify = ({ append }) => { append('ok  \tm/a\t0.1s\n合并说明可以写：go vet 过、go test 1 个包过\n'); return { ok: true, code: 0 } }
