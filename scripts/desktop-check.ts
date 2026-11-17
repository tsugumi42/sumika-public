// 主窗口（desktop/，ADR-353）的检查：装依赖 → 准备 Electrobun 开发包 → 类型检查 → 打包一次 → bun test → 令牌 CSS 是不是最新的。
// 公开导出的仓库没有 desktop/：直接跳过、不红，结论里写明原因。
// go-verify、pre-push、合并队列经 server-verify.ts 的 runPlanned 调它；提交钩子（lefthook.yml）也跑它。
//   bun scripts/desktop-check.ts            在仓库根跑
// 退出码照实：任何一步红都是红，不吞。
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

export type DesktopStep = { name: string; ok: boolean; seconds: number; summary: string; skipped?: boolean }

type Env = Record<string, string | undefined>

function exec(cmd: string[], cwd: string, env: Env) {
  const t = Date.now()
  // 目录不在、程序找不到时 spawnSync 直接抛错：当红（退出码 -1），不让整个检查崩掉
  if (!existsSync(cwd)) return { code: -1, out: `目录不存在：${cwd}\n`, seconds: 0 }
  try {
    const p = Bun.spawnSync(cmd, { cwd, env, stdout: 'pipe', stderr: 'pipe' })
    const out = p.stdout.toString() + p.stderr.toString()
    process.stdout.write(out)
    return { code: p.exitCode, out, seconds: (Date.now() - t) / 1000 }
  } catch (e) {
    return { code: -1, out: String(e), seconds: (Date.now() - t) / 1000 }
  }
}

/** hutch 在哪：HUTCH_HOME/bin/hutch(.exe)，没有就交给 PATH。找不到就红，说明怎么装 */
export function hutchPath(env: Env): string {
  const home = env.HUTCH_HOME
  if (home) for (const n of ['hutch.exe', 'hutch']) { const f = join(home, 'bin', n); if (existsSync(f)) return f }
  return Bun.which('hutch', { PATH: env.PATH ?? env.Path ?? '' }) ?? ''
}

/** 令牌 CSS 过期：拿 tokens-css 现算的和仓库里那份比（换行统一成 \n 再比） */
export function tokensFresh(root: string, env: Env): { ok: boolean; why: string } {
  let p: ReturnType<typeof Bun.spawnSync>
  try {
    p = Bun.spawnSync(['go', 'run', './cmd/tokens-css'], { cwd: join(root, 'native'), env, stdout: 'pipe', stderr: 'pipe' })
  } catch (e) {
    return { ok: false, why: 'tokens-css 跑不起来（go 不在 PATH？）：' + String(e).slice(0, 300) }
  }
  if (p.exitCode !== 0) return { ok: false, why: 'tokens-css 跑不起来：' + (p.stderr?.toString() ?? '').trim().slice(0, 300) }
  const want = (p.stdout?.toString() ?? '').replace(/\r\n/g, '\n')
  const f = join(root, 'desktop', 'src', 'web', 'tokens.gen.css')
  const have = existsSync(f) ? readFileSync(f, 'utf8').replace(/\r\n/g, '\n') : ''
  return want === have ? { ok: true, why: '' } : { ok: false, why: '令牌改了、tokens.gen.css 没重新生成：cd native && go run ./cmd/tokens-css -out ../desktop/src/web/tokens.gen.css' }
}

export function runDesktopCheck(root: string, env: Env, bun = process.execPath): DesktopStep {
  const cwd = join(root, 'desktop')
  const t = Date.now()
  // 公开导出的仓库没有 desktop/：跳过、不红，也不假装跑过
  if (!existsSync(cwd)) return { name: 'desktop 检查', ok: true, seconds: 0, summary: 'desktop 检查：没有 desktop/，跳过', skipped: true }
  const red = (why: string): DesktopStep => ({ name: 'desktop 检查', ok: false, seconds: (Date.now() - t) / 1000, summary: `desktop 检查：红（${why}）` })
  // 专用 worktree 是新开的：按锁文件装（desktop/ 和它引类型的 server/ 都装）
  for (const dir of [cwd, join(root, 'server')]) {
    const i = exec([bun, 'install', '--frozen-lockfile'], dir, env)
    if (i.code !== 0) return red(`${dir.endsWith('server') ? 'server' : 'desktop'}/ 装依赖失败，退出码 ${i.code}`)
  }
  // Electrobun 的接口类型来自 .hutch/devkit（不进 git）：没有就让 hutch 按 hutch.config.ts 钉的版本生成（已缓存的版本不联网）
  if (!existsSync(join(cwd, '.hutch', 'devkit', 'api'))) {
    const h = hutchPath(env)
    if (!h) return red('找不到 hutch：设 HUTCH_HOME 或把 hutch 放进 PATH（规格-网页界面和Electrobun 第六节）')
    const r = exec([h, 'electrobun', 'prepare'], cwd, env)
    if (r.code !== 0) return red(`hutch electrobun prepare 失败，退出码 ${r.code}`)
  }
  const tc = exec([bun, 'run', 'typecheck'], cwd, env)
  if (tc.code !== 0) return red(`类型检查退出码 ${tc.code}`)
  // 真打包一次：开发服务器不压缩 CSS，空选择器这类错只在打包时才报（2026-11-12 设置页多一个 } 一直没人发现）
  const out = mkdtempSync(join(tmpdir(), 'sumika-desk-build-'))
  const vb = exec([bun, 'x', 'vite', 'build', '--outDir', out, '--emptyOutDir', '--logLevel', 'warn'], cwd, env)
  rmSync(out, { recursive: true, force: true })
  if (vb.code !== 0) return red(`vite 打包退出码 ${vb.code}`)
  const tt = exec([bun, 'test'], cwd, env)
  if (tt.code !== 0) return red(`bun test 退出码 ${tt.code}`)
  if (/^\s*[1-9]\d* (skip|todo)\b/m.test(tt.out)) return red('有跳过或待办的测试：检查里不许跳过')
  const fresh = tokensFresh(root, env)
  if (!fresh.ok) return red(fresh.why)
  return { name: 'desktop 检查', ok: true, seconds: (Date.now() - t) / 1000, summary: 'desktop 检查：类型、打包、测试、令牌 CSS 都过' }
}

if (import.meta.main) {
  const s = runDesktopCheck(process.cwd(), process.env)
  console.log(s.summary)
  process.exit(s.ok ? 0 : 1)
}
