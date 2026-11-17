// 本地合并队列（ADR-320，规格-合并队列）：分支提交完排进队列，一个执行者按顺序取，在专用 worktree 里合上当前 main、跑 go-verify，
// 绿了把这个合并提交快进进 main，红了退回并写原因。每条都在前一条合完的 main 上验，不再各自追 main。
//   bun scripts/merge-queue.ts add <分支> [--title "合并标题"] [--body-file 文件]   排进队列
//   bun scripts/merge-queue.ts status                                                  排队中、在跑、最近 20 条结果
//   bun scripts/merge-queue.ts run [--watch]                                           执行者；队列空了退出，--watch 一直等新条目
//   bun scripts/merge-queue.ts remove <分支>                                           从队列里删一条（正在跑的不删）
// 状态和锁放 git 公共目录（所有 worktree 共用）：sumika-merge-queue.json、.lock（读改写的短锁）、sumika-merge-queue.runner（执行者锁）。
// 环境变量（测试和特殊情况用）：SUMIKA_QUEUE_STATE 状态文件、SUMIKA_QUEUE_WORKTREE 专用 worktree 路径、SUMIKA_QUEUE_OUT 输出目录、
// SUMIKA_QUEUE_VERIFY 验证用的 bun 脚本路径（默认专用 worktree 里的 scripts/go-verify.ts，即合并后那棵树里的版本）。
import { cpSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync, openSync, closeSync, appendFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { loadNightly, type NightlyRecord } from './nightly-record'

export type Ready = { commit: string; base: string; tip: string; conclusion: string; outFile: string; seconds: number }
export type Entry = { id: string; branch: string; title: string; body: string; addedAt: string; waiting?: string; ready?: Ready }
export type Running = { id: string; branch: string; pid: number; since: string; phase: string }
export type Result = { branch: string; title: string; at: string; ok: boolean; reason: string; seconds: number; files?: string[]; outFile?: string; commit?: string; tip?: string }
export type State = { queue: Entry[]; running: Running | null; results: Result[] }
export type Ctx = { cwd: string; main: string; common: string; mainDir: string; statePath: string; queueWt: string; outDir: string; verifyScript?: string }
export type Verify = (a: { wt: string; entry: Entry; append: (text: string) => void; timeoutMs: number }) => Promise<{ ok: boolean; code: number; timedOut?: boolean }> | { ok: boolean; code: number; timedOut?: boolean }

const KEEP = 200
const SHOW = 20
const BASE_ATTEMPTS = 3   // 验完发现 main 又被人动了：重合重验，最多这么多轮
export const nowIso = () => new Date().toISOString()
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// ---------- git ----------
export type G = { code: number; out: string; err: string }
// 去掉钩子里继承来的 GIT_DIR 等，免得在钩子里调起来指到别的仓库；core.quotepath=false 让中文文件名不被转义
export function git(cwd: string, args: string[]): G {
  const env: Record<string, string | undefined> = { ...process.env, GIT_EDITOR: 'true', GIT_TERMINAL_PROMPT: '0' }
  for (const k of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_PREFIX']) delete env[k]
  const p = Bun.spawnSync(['git', '-c', 'core.quotepath=false', ...args], { cwd, env, stdout: 'pipe', stderr: 'pipe' })
  return { code: p.exitCode ?? 1, out: p.stdout.toString().trim(), err: p.stderr.toString().trim() }
}
const rev = (cwd: string, ref: string) => { const r = git(cwd, ['rev-parse', '--verify', '--quiet', `${ref}^{commit}`]); return r.code === 0 ? r.out : '' }

// 路径都从 git 公共目录推：add 多半在别的 worktree 里敲，不能依赖当前目录
export function makeCtx(cwd = process.cwd(), over: Partial<Ctx> = {}): Ctx {
  const c = git(cwd, ['rev-parse', '--path-format=absolute', '--git-common-dir'])
  if (c.code !== 0) throw new Error(`不在 git 仓库里：${c.err}`)
  const first = /^worktree (.+)$/m.exec(git(cwd, ['worktree', 'list', '--porcelain']).out)?.[1]
  const mainDir = first ?? dirname(c.out)
  return {
    cwd, main: 'main', common: c.out, mainDir,
    statePath: process.env.SUMIKA_QUEUE_STATE || join(c.out, 'sumika-merge-queue.json'),
    queueWt: process.env.SUMIKA_QUEUE_WORKTREE || join(mainDir, '.claude', 'worktrees', '_queue'),
    outDir: process.env.SUMIKA_QUEUE_OUT || join(mainDir, 'data', 'dev', 'test-runs'),
    verifyScript: process.env.SUMIKA_QUEUE_VERIFY || undefined,
    ...over,
  }
}

// ---------- 状态文件：读改写包一把短锁，写入先写临时文件再改名 ----------
export const emptyState = (): State => ({ queue: [], running: null, results: [] })
export function loadState(path: string): State {
  try {
    const v = JSON.parse(readFileSync(path, 'utf8'))
    return { queue: Array.isArray(v.queue) ? v.queue : [], running: v.running ?? null, results: Array.isArray(v.results) ? v.results : [] }
  } catch {
    return emptyState()   // 没有或写坏了：当成空队列。写是原子的，正常不会读到半份
  }
}

// io 可替换：测试里让写到一半抛错，查原文件没被碰、临时文件没留下
export function saveState(path: string, s: State, io: { write: (p: string, body: string) => void } = { write: writeFileSync }) {
  const body = JSON.stringify({ ...s, results: s.results.slice(-KEEP) }, null, 1)
  const tmp = `${path}.${process.pid}.${Math.random().toString(36).slice(2, 8)}.tmp`
  try {
    io.write(tmp, body)
    // Windows 上目标文件正被别的进程打开（杀毒扫刚写的文件）时改名会报 EPERM，隔一会儿再试（同 go-verify 的记录文件）
    for (let i = 1; ; i++) {
      try { renameSync(tmp, path); return } catch (e) { if (i >= 8) throw e; Bun.sleepSync(25 * i) }
    }
  } finally {
    rmSync(tmp, { force: true })
  }
}

const pidAlive = (pid: number) => { try { process.kill(pid, 0); return true } catch (e) { return (e as NodeJS.ErrnoException).code === 'EPERM' } }

// 用"独占创建文件"当锁；持有者进程没了（或锁太老）就当过期、删掉重来。nightly 单实例锁也用它
export function lockFile(path: string, waitMs: number, staleMs: number): (() => void) | null {
  mkdirSync(dirname(path), { recursive: true })
  const end = Date.now() + waitMs
  for (;;) {
    try {
      const fd = openSync(path, 'wx')
      writeFileSync(fd, JSON.stringify({ pid: process.pid, at: nowIso() }))
      closeSync(fd)
      return () => rmSync(path, { force: true })
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e
      let stale = false
      try {
        const o = JSON.parse(readFileSync(path, 'utf8'))
        stale = !pidAlive(o.pid) || Date.now() - statSync(path).mtimeMs > staleMs
      } catch { stale = Date.now() - (statSafe(path) ?? Date.now()) > 2000 }   // 刚创建还没写完内容的锁不算过期
      if (stale) { rmSync(path, { force: true }); continue }
      if (Date.now() >= end) return null
      Bun.sleepSync(20)
    }
  }
}
const statSafe = (p: string) => { try { return statSync(p).mtimeMs } catch { return undefined } }

export function mutate<T>(ctx: Ctx, fn: (s: State) => T): T {
  const release = lockFile(`${ctx.statePath}.lock`, 15000, 60000)
  if (!release) throw new Error('状态文件的锁一直拿不到（15 秒），看看有没有进程卡住')
  try {
    const s = loadState(ctx.statePath)
    const r = fn(s)
    saveState(ctx.statePath, s)
    return r
  } finally { release() }
}

// 全机只有一个执行者：拿到返回释放函数，已有活着的执行者返回 { pid }
export function acquireRunner(ctx: Ctx): { release: () => void } | { pid: number } {
  const path = join(dirname(ctx.statePath), 'sumika-merge-queue.runner')
  for (let i = 0; i < 5; i++) {
    const rel = lockFile(path, 0, Number.MAX_SAFE_INTEGER)
    if (rel) return { release: rel }
    try { return { pid: JSON.parse(readFileSync(path, 'utf8')).pid } } catch { /* 锁刚被释放或还没写完：再试 */ }
    Bun.sleepSync(50)
  }
  return { pid: 0 }
}

/** 常驻执行者（run --watch）空闲时 state.running 是空的：看执行者锁文件里的 pid 还活着没有 */
export function runnerAlive(ctx: Ctx): boolean {
  try { const pid = JSON.parse(readFileSync(join(dirname(ctx.statePath), 'sumika-merge-queue.runner'), 'utf8')).pid; return typeof pid === 'number' && pid > 0 && pidAlive(pid) } catch { return false }
}

// ---------- add / remove / status ----------
export function addEntry(ctx: Ctx, branch: string, title?: string, body = ''): { position: number; reminder?: string } {
  if (branch === ctx.main) throw new Error(`不能把 ${ctx.main} 排进队列`)
  if (!rev(ctx.mainDir, `refs/heads/${branch}`)) throw new Error(`没有这个本地分支：${branch}`)
  return mutate(ctx, (s) => {
    if (s.queue.some((e) => e.branch === branch)) throw new Error(`${branch} 已经在队列里了`)
    s.queue.push({ id: `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 5)}`, branch, title: title?.trim() || `合并 ${branch}`, body: body.replace(/\r\n/g, '\n').trim(), addedAt: nowIso() })
    return { position: s.queue.length, reminder: nightlyReminder(loadNightly(nightlyPath(ctx))) }
  })
}

export function removeEntry(ctx: Ctx, branch: string) {
  return mutate(ctx, (s) => {
    const i = s.queue.findIndex((e) => e.branch === branch)
    if (i < 0) throw new Error(`队列里没有 ${branch}`)
    if (s.running?.branch === branch && pidAlive(s.running.pid)) throw new Error(`${branch} 正在跑（执行者 pid ${s.running.pid}），跑完再说；要停就先停执行者`)
    const [e] = s.queue.splice(i, 1)
    if (e.ready) git(ctx.mainDir, ['update-ref', '-d', readyRef(e)])
    if (s.running?.branch === branch) s.running = null
  })
}

const when = (iso: string) => new Date(iso).toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
export const dur = (sec: number) => (sec >= 60 ? `${Math.floor(sec / 60)} 分 ${Math.round(sec % 60)} 秒` : `${sec.toFixed(1)} 秒`)

// ---------- 每日整套（规格-每日整套 第 3 条） ----------
// 结论单独一份：验证记录的 200 条滚动数组分不出整套绿红，也不能被队列、推送的短跑挤掉（盘点坑 1、2、3）
export type { NightlyRecord } from './nightly-record'
export const NIGHTLY_MAX_GAP_MS = 36 * 60 * 60 * 1000
// 比现在晚 10 分钟以上的绿记录不算：机器时钟跳过、记录写错时，不能让一条未来时间的绿把提醒永远压住
export const NIGHTLY_FUTURE_GRACE_MS = 10 * 60 * 1000
export const nightlyPath = (ctx: Ctx) => process.env.SUMIKA_NIGHTLY_RECORD || join(ctx.common, 'sumika-nightly.json')

// 多久以前：计时用 UTC 的 at（盘点坑 12），给人看的按本机时间算间隔
function ago(at: string, now: number) {
  const s = Math.max(0, (now - Date.parse(at)) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  const h = Math.floor(s / 3600)
  return h < 48 ? `${h} 小时前` : `${Math.floor(h / 24)} 天前`
}

const latestOf = (rs: NightlyRecord[]) => rs.reduce<NightlyRecord | undefined>((a, b) => (!a || Date.parse(b.at) > Date.parse(a.at) ? b : a), undefined)

export function nightlySummary(records: NightlyRecord[], now = Date.now()) {
  const last = latestOf(records)
  if (!last) return '上次整套：还没有整套记录（bun scripts/nightly.ts 跑一次）'
  const sha = (last.commit ?? '').slice(0, 8) || '未知'
  if (last.ok) return `上次整套：${ago(last.at, now)}，绿（${sha}）`
  return `上次整套：${ago(last.at, now)}，红（${sha}，${last.failed?.length ?? 0} 个包，输出 ${last.output ?? '（没有记录输出文件）'}）`
}

// 36 小时内没有绿的整套：status、add、run 每一轮都打。"没有记录"和"有记录但过期"文案分开（盘点坑 11）
export function nightlyReminder(records: NightlyRecord[], now = Date.now()) {
  const green = latestOf(records.filter((r) => r.ok && Date.parse(r.at) <= now + NIGHTLY_FUTURE_GRACE_MS))
  if (green && now - Date.parse(green.at) < NIGHTLY_MAX_GAP_MS) return undefined
  return green
    ? `超过 36 小时没有绿的整套（上次绿：${when(green.at)}）`
    : '超过 36 小时没有绿的整套（还没有绿的整套记录）'
}

export function statusText(ctx: Ctx, now = Date.now()) {
  const s = loadState(ctx.statePath)
  const nightly = loadNightly(nightlyPath(ctx))
  const L: string[] = []
  L.push(nightlySummary(nightly, now))
  const reminder = nightlyReminder(nightly, now)
  if (reminder) L.push(reminder)
  const alive = s.running && pidAlive(s.running.pid)
  L.push(`在跑：${s.running ? `${s.running.branch}（${s.running.phase}，${when(s.running.since)} 起，执行者 pid ${s.running.pid}${alive ? '' : '，已经不在了'}）` : '没有'}`)
  L.push(`排队中（${s.queue.length}）：${s.queue.length ? '' : '空'}`)
  s.queue.forEach((e, i) => L.push(`  ${i + 1}. ${e.branch}　${e.title}　${when(e.addedAt)} 加入${e.waiting ? `　【${e.waiting}】` : ''}`))
  const rs = s.results.slice(-SHOW).reverse()
  L.push(`最近结果（${rs.length}）：${rs.length ? '' : '还没有'}`)
  for (const r of rs) {
    L.push(`  ${r.ok ? '绿' : '红'}　${r.branch}　${when(r.at)}　耗时 ${dur(r.seconds)}${r.commit ? `　合并提交 ${r.commit.slice(0, 8)}` : ''}${r.ok ? '' : `　原因：${r.reason}`}${r.files?.length ? `（${r.files.join('、')}）` : ''}${r.outFile ? `\n      输出：${r.outFile}` : ''}`)
  }
  return L.join('\n')
}

// ---------- 执行者 ----------
const readyRef = (e: { id: string }) => `refs/sumika-queue/${e.id}`
const slug = (b: string) => b.replace(/[^\w.一-鿿-]+/g, '-')
const stamp = () => { const d = new Date(), p = (n: number) => String(n).padStart(2, '0'); return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}` }

// 专用 worktree 回到干净状态并 detach 到 base：没有就建（顺手拷主工作区的 vendor/bin，新 worktree 缺 rg、fd）
function prepWorktree(ctx: Ctx, base: string) {
  const wt = ctx.queueWt
  if (!existsSync(join(wt, '.git'))) {
    git(ctx.mainDir, ['worktree', 'prune'])
    mkdirSync(dirname(wt), { recursive: true })
    const r = git(ctx.mainDir, ['worktree', 'add', '--detach', '--force', wt, base])
    if (r.code !== 0) throw new Error(`建专用 worktree 失败（${wt}）：${r.err}`)
    const vb = join(ctx.mainDir, 'vendor', 'bin')
    if (existsSync(vb) && !existsSync(join(wt, 'vendor', 'bin'))) cpSync(vb, join(wt, 'vendor', 'bin'), { recursive: true })
    return
  }
  git(wt, ['merge', '--abort'])
  git(wt, ['reset', '--hard'])
  git(wt, ['clean', '-fd'])   // 不加 -x：vendor/bin 之类被忽略的文件留着
  const r = git(wt, ['checkout', '--detach', base])
  if (r.code !== 0) throw new Error(`专用 worktree 切不到 ${base}：${r.err}`)
}

const fullMessage = (e: Entry, conclusion?: string) => [e.title, e.body, conclusion ? `合并队列验证：${conclusion}` : ''].filter(Boolean).join('\n\n') + '\n'

// 结论行：最后一个“合并说明可以写：”当主体；go-verify 另打的“合并说明另写：”（Bun 那段等）各占一行接在后面。取不到主体写“go-verify 退出码 N”
export function conclusionOf(text: string, code: number, vsec: number) {
  const main = [...text.matchAll(/合并说明可以写：([^\r\n]+)/g)].pop()?.[1]?.trim() || `go-verify 退出码 ${code}（${vsec.toFixed(0)} 秒）`
  const extra = [...text.matchAll(/合并说明另写：([^\r\n]+)/g)].map((m) => m[1].trim())
  return [main, ...extra].join('\n')
}

function redReason(out: string, code: number) {
  const lines = out.split(/\r?\n/).map((l) => l.trim()).filter(Boolean)
  const hits = lines.filter((l) => /^(--- FAIL|FAIL\s|有检查红了|panic:|工作区里有)/.test(l)).slice(0, 3)
  const what = code === 2 ? '环境错误（go-verify 退出码 2）' : '验证红'
  return `${what}：${(hits.length ? hits : lines.slice(-1)).join(' | ') || `退出码 ${code}`}`
}

// 默认的验证：在专用 worktree 里跑 go-verify（合并后那棵树里的版本），验证档名额，输出边跑边落盘
export const defaultVerify = (ctx: Ctx): Verify => async ({ wt, append, timeoutMs }) => {
  const script = ctx.verifyScript ?? join(wt, 'scripts', 'go-verify.ts')
  const p = Bun.spawn([process.execPath, script], { cwd: wt, env: { ...process.env, SUMIKA_TEST_LANE: 'verify' }, stdout: 'pipe', stderr: 'pipe' })
  let timedOut = false
  const timer = setTimeout(() => { timedOut = true; p.kill() }, timeoutMs)
  const pump = async (s: ReadableStream<Uint8Array>, to: NodeJS.WriteStream) => {
    const dec = new TextDecoder()
    for await (const c of s) { const t = dec.decode(c, { stream: true }); append(t); to.write(t) }
  }
  await Promise.all([pump(p.stdout, process.stdout), pump(p.stderr, process.stderr)])
  const code = await p.exited
  clearTimeout(timer)
  return { ok: code === 0 && !timedOut, code, timedOut }
}

function setRunning(ctx: Ctx, e: Entry | null, phase = '') {
  mutate(ctx, (s) => { s.running = e ? { id: e.id, branch: e.branch, pid: process.pid, since: s.running?.id === e.id ? s.running.since : nowIso(), phase } : null })
}
function finish(ctx: Ctx, e: Entry, r: Omit<Result, 'branch' | 'title' | 'at'>) {
  git(ctx.mainDir, ['update-ref', '-d', readyRef(e)])
  mutate(ctx, (s) => {
    s.queue = s.queue.filter((x) => x.id !== e.id)
    s.results.push({ branch: e.branch, title: e.title, at: nowIso(), ...r })
    s.running = null
  })
}

type Ff = { kind: 'ok' } | { kind: 'moved' } | { kind: 'blocked'; why: string }
// 快进 main。main 在主工作区检出着：工作区有已跟踪文件的改动就不碰；没检出：update-ref 带旧值，等于 compare-and-swap
function fastForward(ctx: Ctx, commit: string, base: string): Ff {
  const head = git(ctx.mainDir, ['symbolic-ref', '-q', 'HEAD']).out
  if (head !== `refs/heads/${ctx.main}`) {
    return git(ctx.mainDir, ['update-ref', `refs/heads/${ctx.main}`, commit, base]).code === 0 ? { kind: 'ok' } : { kind: 'moved' }
  }
  const st = git(ctx.mainDir, ['status', '--porcelain', '--untracked-files=no']).out
  if (st) return { kind: 'blocked', why: '等主工作区干净' }
  if (rev(ctx.mainDir, ctx.main) !== base) return { kind: 'moved' }
  const r = git(ctx.mainDir, ['merge', '--ff-only', commit])
  if (r.code === 0) return { kind: 'ok' }
  if (rev(ctx.mainDir, ctx.main) !== base) return { kind: 'moved' }
  return { kind: 'blocked', why: `等主工作区干净（快进失败：${r.err.split(/\r?\n/)[0]}）` }
}

export type RunOpts = { verify?: Verify; watch?: boolean; pollMs?: number; waitCleanMs?: number; verifyTimeoutMs?: number; log?: (s: string) => void; now?: () => number; sleep?: (ms: number) => Promise<void> }
export type Head = 'done' | 'blocked'

// 处理队首一条。done：出队、结果已记（绿或红）；blocked：留在队首（等主工作区干净）
export async function processHead(ctx: Ctx, entry: Entry, o: RunOpts = {}): Promise<Head> {
  const log = o.log ?? ((s: string) => console.log(s))
  const verify = o.verify ?? defaultVerify(ctx)
  const t0 = Date.now()
  const secs = () => (Date.now() - t0) / 1000
  const tip = rev(ctx.mainDir, `refs/heads/${entry.branch}`)
  if (!tip) { finish(ctx, entry, { ok: false, reason: `分支 ${entry.branch} 不存在了`, seconds: secs() }); return 'done' }
  mkdirSync(ctx.outDir, { recursive: true })
  for (let attempt = 1; attempt <= BASE_ATTEMPTS; attempt++) {
    const base = rev(ctx.mainDir, ctx.main)
    // 上一轮验完了、只是主工作区没干净：main 没动就不重验，直接快进
    let ready = entry.ready && entry.ready.base === base && entry.ready.tip === tip && rev(ctx.mainDir, entry.ready.commit) === entry.ready.commit ? entry.ready : undefined
    if (!ready) {
      if (git(ctx.mainDir, ['merge-base', '--is-ancestor', tip, base]).code === 0) {
        finish(ctx, entry, { ok: true, reason: `${entry.branch} 已经在 ${ctx.main} 里，没有要合的`, seconds: secs(), tip }); return 'done'
      }
      setRunning(ctx, entry, attempt > 1 ? `第 ${attempt} 轮：合并` : '合并')
      prepWorktree(ctx, base)
      const msgFile = join(ctx.outDir, `queue-${slug(entry.branch)}.msg.tmp`)
      writeFileSync(msgFile, fullMessage(entry))
      const m = git(ctx.queueWt, ['merge', '--no-ff', '-F', msgFile, tip])
      rmSync(msgFile, { force: true })
      if (m.code !== 0) {
        const files = git(ctx.queueWt, ['diff', '--name-only', '--diff-filter=U']).out.split(/\r?\n/).filter(Boolean)
        git(ctx.queueWt, ['merge', '--abort'])
        git(ctx.queueWt, ['reset', '--hard', base])
        finish(ctx, entry, files.length ? { ok: false, reason: '冲突', files, seconds: secs(), tip } : { ok: false, reason: `merge 没成功：${m.err.split(/\r?\n/)[0] || m.out.split(/\r?\n/)[0]}`, seconds: secs(), tip })
        return 'done'
      }
      const merged = rev(ctx.queueWt, 'HEAD')
      const outFile = join(ctx.outDir, `queue-${slug(entry.branch)}-${stamp()}.txt`)
      writeFileSync(outFile, `合并队列验证：${entry.branch}（${tip.slice(0, 8)}）合上 ${ctx.main} ${base.slice(0, 8)} 得 ${merged.slice(0, 8)}，${nowIso()}\n\n`)
      let text = ''
      const append = (t: string) => { text += t; appendFileSync(outFile, t) }
      setRunning(ctx, entry, attempt > 1 ? `第 ${attempt} 轮：验证` : '验证')
      const tv = Date.now()
      const v = await verify({ wt: ctx.queueWt, entry, append, timeoutMs: o.verifyTimeoutMs ?? 60 * 60 * 1000 })
      const vsec = (Date.now() - tv) / 1000
      appendFileSync(outFile, `\n退出码 ${v.code}，${vsec.toFixed(1)} 秒\n`)
      if (!v.ok) {
        git(ctx.queueWt, ['reset', '--hard', base])
        finish(ctx, entry, { ok: false, reason: v.timedOut ? `验证超时（${(vsec / 60).toFixed(0)} 分钟，已停掉）` : redReason(text, v.code), seconds: secs(), outFile, tip })
        return 'done'
      }
      // 结论行只能事后加：用 commit-tree 在同一棵树上重做一次合并提交（不走 amend，免得 pre-commit 在整棵树上再跑一遍）
      const conclusion = conclusionOf(text, v.code, vsec)
      const finalMsg = join(ctx.outDir, `queue-${slug(entry.branch)}.msg.tmp`)
      writeFileSync(finalMsg, fullMessage(entry, conclusion))
      const tree = git(ctx.queueWt, ['rev-parse', 'HEAD^{tree}']).out
      const ct = git(ctx.queueWt, ['commit-tree', tree, '-p', base, '-p', tip, '-F', finalMsg])
      rmSync(finalMsg, { force: true })
      if (ct.code !== 0 || git(ctx.queueWt, ['rev-parse', `${ct.out}^{tree}`]).out !== tree) throw new Error(`重做合并提交失败：${ct.err}`)
      git(ctx.queueWt, ['reset', '--hard', ct.out])
      if (rev(ctx.mainDir, ctx.main) !== base) { log(`  验证期间 ${ctx.main} 被人动了，重新合、重新验（第 ${attempt} 轮）`); continue }
      ready = { commit: ct.out, base, tip, conclusion, outFile, seconds: secs() }
      git(ctx.mainDir, ['update-ref', readyRef(entry), ct.out])   // 防止这个提交被 gc 收走
      entry.ready = ready
      mutate(ctx, (s) => { const e = s.queue.find((x) => x.id === entry.id); if (e) e.ready = ready })
    }
    setRunning(ctx, entry, '快进 main')
    const ff = fastForward(ctx, ready.commit, base)
    if (ff.kind === 'ok') {
      finish(ctx, entry, { ok: true, reason: ready.conclusion, seconds: ready.seconds, outFile: ready.outFile, commit: ready.commit, tip })
      return 'done'
    }
    if (ff.kind === 'blocked') {
      mutate(ctx, (s) => { const e = s.queue.find((x) => x.id === entry.id); if (e) e.waiting = ff.why; s.running = null })
      entry.waiting = ff.why
      return 'blocked'
    }
    log(`  快进时 ${ctx.main} 已经变了，重新合、重新验（第 ${attempt} 轮）`)
    entry.ready = undefined
    git(ctx.mainDir, ['update-ref', '-d', readyRef(entry)])
    mutate(ctx, (s) => { const e = s.queue.find((x) => x.id === entry.id); if (e) delete e.ready })
  }
  finish(ctx, entry, { ok: false, reason: `${ctx.main} 一直在变，重合重验 ${BASE_ATTEMPTS} 轮都没赶上，重新排队`, seconds: secs(), tip })
  return 'done'
}

// 执行者主循环。返回 empty（队列空了）、blocked（队首等主工作区干净且没开 --watch）、busy（已有执行者）
export async function runQueue(ctx: Ctx, o: RunOpts = {}): Promise<'empty' | 'blocked' | 'busy'> {
  const log = o.log ?? ((s: string) => console.log(s))
  const clock = o.now ?? Date.now
  const nap = o.sleep ?? sleep
  const got = acquireRunner(ctx)
  if (!('release' in got)) { log(`已经有执行者在跑（pid ${got.pid || '不明'}），这个不起了`); return 'busy' }
  try {
    // 只有一个执行者，状态里残留的 running 一定是上次死掉的
    mutate(ctx, (s) => { s.running = null })
    let reminded = 0
    for (;;) {
      // 每一轮都查，包括队列空的那一支；常驻 --watch 空闲时才看得到。process 内一小时最多打一次，免得每 3 秒刷屏
      const now = clock()
      if (now - reminded >= 60 * 60 * 1000) {
        const reminder = nightlyReminder(loadNightly(nightlyPath(ctx)), now)
        if (reminder) { log(reminder); reminded = now }
      }
      const head = loadState(ctx.statePath).queue[0]
      if (!head) {
        if (!o.watch) return 'empty'
        await nap(o.pollMs ?? 3000)
        continue
      }
      log(`合并队列：取 ${head.branch}（${head.title}）`)
      const r = await processHead(ctx, head, o)
      const last = loadState(ctx.statePath).results.at(-1)
      if (r === 'done' && last) log(`  ${last.ok ? '绿' : '红'}：${last.branch}，${dur(last.seconds)}${last.ok ? '' : `，${last.reason}${last.files?.length ? `（${last.files.join('、')}）` : ''}`}${last.outFile ? `\n  输出：${last.outFile}` : ''}`)
      if (r === 'blocked') {
        log(`  ${head.branch} 已验好，${head.waiting}；留在队首`)
        if (!o.watch) return 'blocked'
        await nap(o.waitCleanMs ?? 10000)
      }
    }
  } finally {
    got.release()
  }
}

// ---------- 命令行 ----------
function arg(a: string[], name: string) { const i = a.indexOf(name); return i >= 0 ? a[i + 1] : undefined }

if (import.meta.main) {
  const [cmd, ...rest] = process.argv.slice(2)
  const positional = rest.filter((x, i) => !x.startsWith('--') && !(i > 0 && ['--title', '--body-file'].includes(rest[i - 1])))
  try {
    const ctx = makeCtx()
    if (cmd === 'add') {
      const branch = positional[0]
      if (!branch) throw new Error('用法：add <分支> [--title "合并标题"] [--body-file 文件]')
      const bf = arg(rest, '--body-file')
      const body = bf ? readFileSync(resolve(bf), 'utf8') : ''
      const r = addEntry(ctx, branch, arg(rest, '--title'), body)
      const st = loadState(ctx.statePath)
      console.log(`排进队列了：${branch}，第 ${r.position} 位`)
      if (r.reminder) console.log(r.reminder)
      // 2026-11-12：以前只看 running，常驻执行者空闲等新条目时每次都误报“没有执行者”
      if (!(st.running && pidAlive(st.running.pid)) && !runnerAlive(ctx)) console.log('现在没有执行者在跑。要开：bun scripts/merge-queue.ts run（--watch 一直等新条目）')
    } else if (cmd === 'status') {
      console.log(statusText(ctx))
    } else if (cmd === 'remove') {
      if (!positional[0]) throw new Error('用法：remove <分支>')
      removeEntry(ctx, positional[0])
      console.log(`已从队列删掉：${positional[0]}`)
    } else if (cmd === 'run') {
      const r = await runQueue(ctx, { watch: rest.includes('--watch') })
      if (r === 'busy') process.exit(4)
      if (r === 'blocked') process.exit(3)
    } else {
      console.log('用法：bun scripts/merge-queue.ts add|status|run|remove …（见文件开头）')
      process.exit(2)
    }
  } catch (e) {
    console.log(`出错了：${(e as Error).message}`)
    process.exit(2)
  }
}
