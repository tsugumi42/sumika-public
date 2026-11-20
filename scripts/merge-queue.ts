// 合并队列（ADR-320，规格-合并队列；ADR-386、398，规格-合并走PR 第二节）：分支推到公开仓库、开好 PR 以后排进队列，一个执行者按顺序取。
// 本机不合并、不跑验证：看 PR 上 GitHub 的检查，分支保护里的必过检查都绿了就 gh pr merge（合并提交、删远端分支），
// 落后 main 就替它点“更新分支”等检查重跑，红了出队写原因，还没出结果就留在队首。合完把本机 main 快进到远端。
// 不轮询：检查还没出结果时 run 直接退出（退出码 3），PR 绑到会话后检查出结果会有事件推来，到时再跑一次 run。
//   bun scripts/merge-queue.ts add <分支> [--title "合并标题"] [--body-file 文件]   排进队列（只记本地，PR 在 run 时找）
//   bun scripts/merge-queue.ts status                                                  排队中、在跑、最近 20 条结果
//   bun scripts/merge-queue.ts run                                                     执行者；队列空了、或队首在等 GitHub 时退出
//   bun scripts/merge-queue.ts remove <分支>                                           从队列里删一条（正在跑的不删）
// 状态和锁放 git 公共目录（所有 worktree 共用）：sumika-merge-queue.json、.lock（读改写的短锁）、sumika-merge-queue.runner（执行者锁）。
// 环境变量（测试和特殊情况用）：SUMIKA_QUEUE_STATE 状态文件、SUMIKA_QUEUE_REMOTE 远端名（默认 origin）、SUMIKA_QUEUE_REPO GitHub 仓库 owner/name（默认从远端地址推）。
import { existsSync, readFileSync, renameSync, rmSync, statSync, writeFileSync, openSync, closeSync, mkdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { loadNightly, type NightlyRecord } from './nightly-record'

export type Entry = { id: string; branch: string; title: string; body: string; addedAt: string; waiting?: string }
export type Running = { id: string; branch: string; pid: number; since: string; phase: string }
// outFile、files 是本机验证时代的旧结果字段，状态文件里还留着，显示照旧
export type Result = { branch: string; title: string; at: string; ok: boolean; reason: string; seconds: number; files?: string[]; outFile?: string; commit?: string; tip?: string; pr?: string }
export type State = { queue: Entry[]; running: Running | null; results: Result[] }
export type Ctx = { cwd: string; main: string; common: string; mainDir: string; statePath: string; remote: string; repo?: string }

const KEEP = 200
const SHOW = 20
export const nowIso = () => new Date().toISOString()

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
    remote: process.env.SUMIKA_QUEUE_REMOTE || 'origin',
    repo: process.env.SUMIKA_QUEUE_REPO || undefined,
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
    s.queue.splice(i, 1)
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
    L.push(`  ${r.ok ? '绿' : '红'}　${r.branch}　${when(r.at)}　耗时 ${dur(r.seconds)}${r.commit ? `　合并提交 ${r.commit.slice(0, 8)}` : ''}${r.pr ? `　${r.pr}` : ''}${r.ok ? '' : `　原因：${r.reason}`}${r.files?.length ? `（${r.files.join('、')}）` : ''}${r.outFile ? `\n      输出：${r.outFile}` : ''}`)
  }
  return L.join('\n')
}

// ---------- GitHub ----------
// 一条检查：Actions 的 CheckRun 和外部写进来的提交状态（StatusContext）统一成一种
export type Check = { name: string; done: boolean; conclusion: string; url: string; at: string }
export type Pr = { number: number; state: string; head: string; mergeState: string; url: string; checks: Check[]; draft?: boolean }
// 分支保护里的必过检查；strict 是“要求分支是最新的”
export type Required = { names: string[]; strict: boolean }
// 跟 GitHub 打交道的全在这里，测试换成假的
export type Hub = {
  findPr: (branch: string, base: string) => Pr | null
  required: (base: string) => Required
  updateBranch: (pr: Pr) => void
  merge: (pr: Pr, subject: string, body: string) => { ok: boolean; err: string; commit?: string }
}

type RawCheck = { __typename?: string; name?: string; context?: string; status?: string; conclusion?: string; state?: string; detailsUrl?: string; targetUrl?: string; startedAt?: string; completedAt?: string; createdAt?: string }
export function normChecks(raw: RawCheck[] | null | undefined): Check[] {
  return (raw ?? []).map((r) => {
    if (r.__typename === 'StatusContext' || (r.context !== undefined && r.name === undefined)) {
      const st = (r.state ?? '').toUpperCase()
      return { name: r.context ?? '', done: !['', 'PENDING', 'EXPECTED'].includes(st), conclusion: st, url: r.targetUrl ?? '', at: r.startedAt ?? r.createdAt ?? '' }
    }
    return { name: r.name ?? '', done: (r.status ?? '').toUpperCase() === 'COMPLETED', conclusion: (r.conclusion ?? '').toUpperCase(), url: r.detailsUrl ?? '', at: r.startedAt ?? r.completedAt ?? '' }
  })
}

const isGreen = (c: Check) => c.done && c.conclusion === 'SUCCESS'
// 时间解析成毫秒；空的、解析不了的、gh 给的零值时间（0001-01-01，排队中的检查还没开始）一律当最新，宁可多等不放过
const timeOf = (at: string) => { const t = Date.parse(at); return Number.isFinite(t) && t > 0 ? t : Number.POSITIVE_INFINITY }
// 同名的取最新一次（重跑会留下旧记录）；最新时刻有几份时，有一份不绿就算不绿
function latest(checks: Check[], name: string): Check | undefined {
  const same = checks.filter((c) => c.name === name)
  if (!same.length) return undefined
  const top = Math.max(...same.map((c) => timeOf(c.at)))
  const newest = same.filter((c) => timeOf(c.at) === top)
  return newest.find((c) => !isGreen(c)) ?? newest[0]
}

export type Decision =
  | { kind: 'merged' }
  | { kind: 'red'; why: string }
  | { kind: 'wait'; why: string }
  | { kind: 'update' }
  | { kind: 'merge'; summary: string }

// 能不能合：只认分支保护里的必过检查，每一条最新一次都是 SUCCESS 才合；没有必过检查、读不到都不合（规格-合并走PR 第二节第 5 条）
export function decide(pr: Pr, required: string[]): Decision {
  if (pr.state === 'MERGED') return { kind: 'merged' }
  if (pr.state !== 'OPEN') return { kind: 'red', why: `PR #${pr.number} 已关闭，没合` }
  if (pr.mergeState === 'DIRTY') return { kind: 'red', why: `PR #${pr.number} 和 main 有冲突：本地合上 main、解决了推上去再排` }
  if (pr.draft || pr.mergeState === 'DRAFT') return { kind: 'wait', why: `PR #${pr.number} 还是草稿，转成正式的再合` }
  if (!required.length) return { kind: 'wait', why: 'main 的分支保护里没有必过检查，不合（先在 GitHub 上设好）' }
  const pending: string[] = []
  const lines: string[] = []
  for (const name of required) {
    const c = latest(pr.checks, name)
    if (!c || !c.done) { pending.push(name); continue }
    if (!isGreen(c)) return { kind: 'red', why: `GitHub 检查红：${name}（${c.conclusion || '没有结论'}）${c.url ? ` ${c.url}` : ''}` }
    lines.push(`${name}：绿${c.url ? ` ${c.url}` : ''}`)
  }
  // 分支保护要求分支是最新的：落后了先更新，旧头上的结果作废
  if (pr.mergeState === 'BEHIND') return { kind: 'update' }
  if (pending.length) return { kind: 'wait', why: `等 GitHub 检查：${pending.join('、')}` }
  if (!pr.mergeState || pr.mergeState === 'UNKNOWN') return { kind: 'wait', why: '等 GitHub 算出能不能合' }
  // 必过都绿了 GitHub 还说挡着：不交给 gh 去试（管理员账号服务端不拦，gh 的预检只看这个状态）
  if (pr.mergeState === 'BLOCKED') return { kind: 'wait', why: `PR #${pr.number} 必过检查都绿了，GitHub 还说不能合（BLOCKED），去 PR 页面看看` }
  // 不挡合并的检查也写进合并说明，红了看得见
  for (const name of [...new Set(pr.checks.map((c) => c.name))].filter((n) => n && !required.includes(n))) {
    const c = latest(pr.checks, name)!
    lines.push(`${name}：${!c.done ? '没跑完' : isGreen(c) ? '绿' : `红（${c.conclusion}）`}，不挡合并${c.url ? ` ${c.url}` : ''}`)
  }
  return { kind: 'merge', summary: lines.join('\n') }
}

const firstLine = (s: string) => s.split(/\r?\n/).find((l) => l.trim())?.trim() ?? ''

function gh(ctx: Ctx, args: string[]): G {
  const env: Record<string, string | undefined> = { ...process.env, GH_PROMPT_DISABLED: '1', GIT_TERMINAL_PROMPT: '0' }
  for (const k of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_PREFIX']) delete env[k]
  const p = Bun.spawnSync(['gh', ...args], { cwd: ctx.mainDir, env, stdout: 'pipe', stderr: 'pipe' })
  return { code: p.exitCode ?? 1, out: p.stdout.toString().trim(), err: p.stderr.toString().trim() }
}

// 显式带 -R：本机有 origin 和 private 两个远端，不让 gh 自己猜
export function repoOf(ctx: Ctx): string {
  if (ctx.repo) return ctx.repo
  const url = git(ctx.mainDir, ['remote', 'get-url', ctx.remote]).out
  const m = /github\.com[:/]([^/\s]+\/[^/\s]+?)(?:\.git)?\/?$/.exec(url)
  if (!m) throw new Error(`远端 ${ctx.remote} 不是 GitHub 仓库：${url || '没有这个远端'}`)
  return m[1]
}

const PR_FIELDS = 'number,state,headRefOid,mergeStateStatus,url,statusCheckRollup,isCrossRepository,baseRefName,isDraft'
export type RawPr = { number: number; state: string; headRefOid: string; mergeStateStatus?: string; url: string; statusCheckRollup?: RawCheck[]; isCrossRepository?: boolean; baseRefName?: string; isDraft?: boolean }
const toPr = (v: RawPr): Pr => ({ number: v.number, state: v.state, head: v.headRefOid, mergeState: v.mergeStateStatus ?? '', url: v.url, checks: normChecks(v.statusCheckRollup), draft: v.isDraft === true })

// 从 gh pr list --head 的结果里挑这个分支的 PR：--head 不分仓库，fork 里同名分支的 PR 也会列出来，
// 只认本仓库、合进 base 的；同时开着两个以上说不清合哪个，报错留在队首
export function pickPr(all: RawPr[], base: string): Pr | null {
  const mine = all.filter((v) => v.isCrossRepository === false && v.baseRefName === base)
  const open = mine.filter((v) => v.state === 'OPEN')
  if (open.length > 1) throw new Error(`这个分支有 ${open.length} 个开着的 PR（${open.map((v) => `#${v.number}`).join('、')}），关掉多余的再合`)
  const pick = open[0] ?? mine.find((v) => v.state === 'MERGED') ?? mine[0]
  return pick ? toPr(pick) : null
}

export const ghHub = (ctx: Ctx): Hub => ({
  findPr(branch, base) {
    const r = gh(ctx, ['pr', 'list', '-R', repoOf(ctx), '--head', branch, '--base', base, '--state', 'all', '--json', PR_FIELDS, '--limit', '100'])
    if (r.code !== 0) throw new Error(`gh pr list 失败：${firstLine(r.err)}`)
    return pickPr(JSON.parse(r.out || '[]') as RawPr[], base)
  },
  required(base) {
    const r = gh(ctx, ['api', `repos/${repoOf(ctx)}/branches/${base}/protection/required_status_checks`])
    if (r.code !== 0) throw new Error(`读不到 ${base} 的分支保护（${firstLine(r.err)}）`)
    const v = JSON.parse(r.out) as { strict?: boolean; contexts?: string[]; checks?: { context: string }[] }
    return { names: [...new Set([...(v.contexts ?? []), ...(v.checks ?? []).map((c) => c.context)])].filter(Boolean), strict: v.strict === true }
  },
  updateBranch(pr) {
    const r = gh(ctx, ['pr', 'update-branch', String(pr.number), '-R', repoOf(ctx)])
    if (r.code !== 0) throw new Error(firstLine(r.err) || `退出码 ${r.code}`)
  },
  merge(pr, subject, body) {
    // --match-head-commit：判断时看的是这个头，合并前 PR 又推了新提交 GitHub 会拒绝；带 -R 时 gh 只删远端分支，本地分支和 worktree 不动
    const r = gh(ctx, ['pr', 'merge', String(pr.number), '-R', repoOf(ctx), '--merge', '--delete-branch', '--match-head-commit', pr.head, '--subject', subject, '--body', body])
    if (r.code !== 0) return { ok: false, err: firstLine(r.err) || `退出码 ${r.code}` }
    const v = gh(ctx, ['pr', 'view', String(pr.number), '-R', repoOf(ctx), '--json', 'mergeCommit', '--jq', '.mergeCommit.oid'])
    return { ok: true, err: '', commit: v.code === 0 ? v.out : undefined }
  },
})

// ---------- 本机 main 跟上远端 ----------
const isAncestor = (cwd: string, a: string, b: string) => git(cwd, ['merge-base', '--is-ancestor', a, b]).code === 0
const remoteMain = (ctx: Ctx) => rev(ctx.mainDir, `refs/remotes/${ctx.remote}/${ctx.main}`)

const fetchMain = (ctx: Ctx) => git(ctx.mainDir, ['fetch', '-q', ctx.remote, `+refs/heads/${ctx.main}:refs/remotes/${ctx.remote}/${ctx.main}`])

// main 检出在哪个 worktree（可能不是主工作区）；没检出返回空。登记着但目录已经没了的 worktree 不算（在那里起 git 会直接抛）
function checkedOutAt(ctx: Ctx): { path: string } | null {
  let path = ''
  for (const line of git(ctx.mainDir, ['worktree', 'list', '--porcelain']).out.split(/\r?\n/)) {
    if (line.startsWith('worktree ')) path = line.slice(9)
    else if (line === `branch refs/heads/${ctx.main}` && existsSync(path)) return { path }
  }
  return null
}

// 合完把本机 main 快进到远端。只快进：本机 main 有远端没有的提交、工作区不干净都不动，返回说明（空串是对齐了）。
// 合并已经成功了，这里出什么错都只写说明，不能让队列卡在这一步
export function syncMain(ctx: Ctx): string {
  try {
    const f = fetchMain(ctx)
    if (f.code !== 0) return `本机 ${ctx.main} 没对齐：fetch 失败（${firstLine(f.err)}）`
    const remote = remoteMain(ctx)
    const local = rev(ctx.mainDir, `refs/heads/${ctx.main}`)
    if (local === remote) return ''
    if (!isAncestor(ctx.mainDir, local, remote)) return `本机 ${ctx.main} 有远端没有的提交（${local.slice(0, 8)}），没对齐`
    const wt = checkedOutAt(ctx)
    if (!wt) return git(ctx.mainDir, ['update-ref', `refs/heads/${ctx.main}`, remote, local]).code === 0 ? '' : `本机 ${ctx.main} 没对齐：被别人抢先改了`
    if (git(wt.path, ['status', '--porcelain', '--untracked-files=no']).out) return `本机 ${ctx.main} 没对齐：${wt.path} 有没提交的改动，干净后 git pull --ff-only`
    const r = git(wt.path, ['merge', '--ff-only', '-q', remote])
    return r.code === 0 ? '' : `本机 ${ctx.main} 没对齐：${firstLine(r.err)}`
  } catch (e) {
    return `本机 ${ctx.main} 没对齐：${(e as Error).message}`
  }
}

// ---------- 执行者 ----------
function setRunning(ctx: Ctx, e: Entry | null, phase = '') {
  mutate(ctx, (s) => { s.running = e ? { id: e.id, branch: e.branch, pid: process.pid, since: s.running?.id === e.id ? s.running.since : nowIso(), phase } : null })
}
function finish(ctx: Ctx, e: Entry, r: Omit<Result, 'branch' | 'title' | 'at'>) {
  mutate(ctx, (s) => {
    s.queue = s.queue.filter((x) => x.id !== e.id)
    s.results.push({ branch: e.branch, title: e.title, at: nowIso(), ...r })
    s.running = null
  })
}
function hold(ctx: Ctx, e: Entry, why: string): Head {
  mutate(ctx, (s) => { const x = s.queue.find((y) => y.id === e.id); if (x) x.waiting = why; s.running = null })
  e.waiting = why
  return 'blocked'
}

export const mergeBody = (e: Entry, pr: Pr, summary: string) => [e.body, `GitHub 检查（PR #${pr.number}，头 ${pr.head.slice(0, 8)}）：\n${summary}`].filter(Boolean).join('\n\n')

export type RunOpts = { hub?: Hub; log?: (s: string) => void; now?: () => number }
export type Head = 'done' | 'blocked'

// 处理队首一条。done：出队、结果已记（绿或红）；blocked：留在队首（等 GitHub、等推送、gh 出错）
export async function processHead(ctx: Ctx, entry: Entry, o: RunOpts = {}): Promise<Head> {
  const hub = o.hub ?? ghHub(ctx)
  const t0 = Date.now()
  const secs = () => (Date.now() - t0) / 1000
  const tip = rev(ctx.mainDir, `refs/heads/${entry.branch}`)
  if (!tip) { finish(ctx, entry, { ok: false, reason: `分支 ${entry.branch} 不存在了`, seconds: secs() }); return 'done' }
  setRunning(ctx, entry, '看 PR')
  let pr: Pr | null, required: Required = { names: [], strict: false }
  try {
    pr = hub.findPr(entry.branch, ctx.main)
    if (!pr) { finish(ctx, entry, { ok: false, reason: `没找到 ${entry.branch} 合进 ${ctx.main} 的 PR：先推到 ${ctx.remote}、gh pr create 再排`, seconds: secs(), tip }); return 'done' }
    if (pr.state === 'OPEN') required = hub.required(ctx.main)
  } catch (e) { return hold(ctx, entry, `问 GitHub 出错：${(e as Error).message}`) }

  if (pr.state === 'OPEN') {
    // 分支保护不要求“分支是最新的”时，下面这道“包含最新 main”的检查就成了唯一的一道，不合（规格第四节）
    if (!required.strict) return hold(ctx, entry, `${ctx.main} 的分支保护没开“要求分支是最新的”，不合（先在 GitHub 上设好）`)
    // PR 头和最新 main 都取到本地，下面两道检查自己用 git 判断，不只信 GitHub 异步算的状态
    git(ctx.mainDir, ['fetch', '-q', ctx.remote, `+refs/heads/${entry.branch}:refs/remotes/${ctx.remote}/${entry.branch}`])
    fetchMain(ctx)
    // 本地有没推的提交：PR 上的检查测的不是要合的东西。PR 头比本地新（点过“更新分支”）没关系
    if (tip !== pr.head && !isAncestor(ctx.mainDir, tip, pr.head)) return hold(ctx, entry, `本地 ${entry.branch}（${tip.slice(0, 8)}）有没推上去的提交，PR 头是 ${pr.head.slice(0, 8)}：推上去再合`)
  }

  let d = decide(pr, required.names)
  // GitHub 说能合，但 PR 头其实还没包含最新 main（刚合完上一条，GitHub 还没重算）：当落后处理。
  // 管理员账号合并时服务端不拦“落后”，这一道是队列自己的
  if (d.kind === 'merge') {
    const m = remoteMain(ctx)
    if (!m || !isAncestor(ctx.mainDir, m, pr.head)) d = { kind: 'update' }
  }
  if (d.kind === 'merged') {
    fetchMain(ctx)
    // 同名分支以前合过一个 PR、后来又有新提交：不算合了
    if (!isAncestor(ctx.mainDir, tip, remoteMain(ctx))) { finish(ctx, entry, { ok: false, reason: `${entry.branch} 的 PR #${pr.number} 以前合过，之后的提交不在 ${ctx.main} 里：再开一个 PR`, seconds: secs(), tip, pr: pr.url }); return 'done' }
    const note = syncMain(ctx)
    finish(ctx, entry, { ok: true, reason: [`PR #${pr.number} 已经在 GitHub 上合了`, note].filter(Boolean).join('；'), seconds: secs(), tip, pr: pr.url })
    return 'done'
  }
  if (d.kind === 'red') { finish(ctx, entry, { ok: false, reason: d.why, seconds: secs(), tip, pr: pr.url }); return 'done' }
  if (d.kind === 'wait') return hold(ctx, entry, d.why)
  if (d.kind === 'update') {
    setRunning(ctx, entry, '更新分支')
    try { hub.updateBranch(pr) } catch (e) { return hold(ctx, entry, `PR #${pr.number} 落后 ${ctx.main}，点“更新分支”失败：${(e as Error).message}`) }
    return hold(ctx, entry, `PR #${pr.number} 落后 ${ctx.main}，已点“更新分支”，等 GitHub 检查重跑`)
  }
  setRunning(ctx, entry, '合并')
  const m = hub.merge(pr, entry.title, mergeBody(entry, pr, d.summary))
  if (!m.ok) return hold(ctx, entry, `gh pr merge 没成功：${m.err}`)
  const note = syncMain(ctx)
  finish(ctx, entry, { ok: true, reason: [d.summary, note].filter(Boolean).join('\n'), seconds: secs(), tip, pr: pr.url, commit: m.commit })
  return 'done'
}

// 执行者主循环：一条条处理，队首在等就退出（不轮询，等 PR 事件再跑）。返回 empty、blocked、busy（已有执行者）
export async function runQueue(ctx: Ctx, o: RunOpts = {}): Promise<'empty' | 'blocked' | 'busy'> {
  const log = o.log ?? ((s: string) => console.log(s))
  const got = acquireRunner(ctx)
  if (!('release' in got)) { log(`已经有执行者在跑（pid ${got.pid || '不明'}），这个不起了`); return 'busy' }
  try {
    // 只有一个执行者，状态里残留的 running 一定是上次死掉的
    mutate(ctx, (s) => { s.running = null })
    const reminder = nightlyReminder(loadNightly(nightlyPath(ctx)), (o.now ?? Date.now)())
    if (reminder) log(reminder)
    for (;;) {
      const head = loadState(ctx.statePath).queue[0]
      if (!head) return 'empty'
      log(`合并队列：取 ${head.branch}（${head.title}）`)
      const r = await processHead(ctx, head, o)
      if (r === 'blocked') { log(`  ${head.branch}：${head.waiting}；留在队首`); return 'blocked' }
      const last = loadState(ctx.statePath).results.at(-1)
      if (last) log(`  ${last.ok ? '绿' : '红'}：${last.branch}${last.pr ? `（${last.pr}）` : ''}，${last.reason.split('\n')[0]}`)
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
      console.log(`排进队列了：${branch}，第 ${r.position} 位。分支要先推上去、开好 PR；执行：bun scripts/merge-queue.ts run`)
      if (r.reminder) console.log(r.reminder)
    } else if (cmd === 'status') {
      console.log(statusText(ctx))
    } else if (cmd === 'remove') {
      if (!positional[0]) throw new Error('用法：remove <分支>')
      removeEntry(ctx, positional[0])
      console.log(`已从队列删掉：${positional[0]}`)
    } else if (cmd === 'run') {
      if (rest.includes('--watch')) throw new Error('不再有 --watch：GitHub 检查出结果时 PR 绑会话的事件会推来，收到再跑 run（规格-合并走PR 第五节不轮询）')
      const r = await runQueue(ctx)
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
