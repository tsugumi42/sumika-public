// 质检第一步：开发侧摩擦报告（规格：docs/规格-质检.md）。
// 只读两类现成数据：Sumika 的 trace 库（先复制再读副本）和 Claude Code 的会话记录；全部用规则数，不调模型。
// 报告只写类别、归一化特征、次数和打码后的位置：不出现聊天原文、命令全文、完整路径。
import { Database } from 'bun:sqlite'
import { copyFileSync, createReadStream, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, dirname, join, resolve } from 'node:path'
import { createConnection } from 'node:net'

const DAY = 86_400_000
const CACHE_GAP_MS = 5 * 60_000      // 相邻两轮间隔超过 5 分钟，缓存已过期，命中率掉了不算异常
const MIN_TOTAL = 20_000             // 单轮输入总量低于 20k 的不报“超过中位数 3 倍”
const PIT_DRIVE = '`/c/` 式盘符路径'
const PIT_HEREDOC = 'heredoc 写含反斜杠的内容'

// ---------- 小工具 ----------

/** 主工作区：在 worktree 里跑时 data/ 不在本目录，往上找到主仓库。 */
export function mainRoot(): string {
  return resolve(import.meta.dir, '..').replace(/[\\/]\.claude[\\/]worktrees[\\/][^\\/]+$/, '')
}

/** 临时目录的根：SUMIKA_SCRATCH，没设就用主工作区 data/dev/friction-tmp。不写系统临时目录。 */
export function scratchBase(): string {
  return process.env.SUMIKA_SCRATCH || join(mainRoot(), 'data', 'dev', 'friction-tmp')
}

/** “24h”“3d”“90m” 或 ISO 时间。 */
export function parseSince(s: string, now: number): number {
  const m = /^(\d+(?:\.\d+)?)\s*([mhd])$/i.exec(s.trim())
  if (m) return now - Number(m[1]) * { m: 60_000, h: 3_600_000, d: DAY }[m[2].toLowerCase() as 'm' | 'h' | 'd']
  const t = Date.parse(s)
  if (Number.isNaN(t)) throw new Error(`--since 看不懂：${s}（写 24h、3d 或 ISO 时间）`)
  return t
}

/** 放进特征名的命令形状：只留前两段、最多 40 字，长复合命令不把同一类拆成很多条。 */
const head = (sh: string) => sh.split(' | ').slice(0, 2).join(' | ').slice(0, 40)

const pad = (n: number) => String(n).padStart(2, '0')
export const localDate = (ms: number) => { const d = new Date(ms); return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` }
const localTime = (ms: number) => { const d = new Date(ms); return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }

/** 编号只露末两位。 */
export const maskId = (id: string) => `***${String(id).slice(-2)}`

/** 最后一道保险：用户名路径、长数字一律打码。报告里的每一段文字都要过它。 */
export function scrub(text: string): string {
  return text
    .replace(/([A-Za-z]:[\\/]+Users[\\/]+)[^\\/\s|`)\]]+/gi, '$1***')
    .replace(/(\/(?:Users|home)\/)[^/\s|`)\]]+/g, '$1***')
    .replace(/\d{5,}/g, (d) => `***${d.slice(-2)}`)
}

function editDistance(a: string, b: string): number {
  if (a === b) return 0
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i)
  for (let i = 1; i <= a.length; i++) {
    const cur = [i]
    for (let j = 1; j <= b.length; j++) cur[j] = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1))
    prev = cur
  }
  return prev[b.length]
}

/** 归一化后编辑距离不超过 30%。 */
export function similar(a: string, b: string): boolean {
  const n = Math.max(a.length, b.length)
  if (n === 0) return true
  return editDistance(a, b) / n <= 0.3
}

// ---------- 命令归一化 ----------

const PATHISH = /[\\/]|^[A-Za-z]:|^~|^\.{1,2}$|\.[A-Za-z0-9]{1,5}$/
const PATTERN_PROGS = new Set(['rg', 'grep', 'fd', 'findstr', 'select-string', 'sls', 'egrep'])

/** 命令只留程序名和参数形状，如 `rg <模式> <路径>`。 */
export function shape(cmd: string): string {
  const lines = cmd.split(/\r?\n/)
  const kept: string[] = []
  for (const l of lines) {
    if (!l.trim()) continue
    kept.push(l)
    if (l.includes('<<') || kept.length >= 6) break
  }
  const segs: string[] = []
  for (const line of kept) {
    const toks = line.match(/"(?:[^"\\]|\\.)*"|'[^']*'|\|\||&&|[|;]|[^\s|;&"']+/g) ?? []
    let cur: string[] = []
    const flush = () => { if (cur.length) segs.push(shapeOne(cur)); cur = [] }
    for (const t of toks) { if (t === '|' || t === '||' || t === '&&' || t === ';') flush(); else cur.push(t) }
    flush()
  }
  return segs.slice(0, 5).join(' | ').slice(0, 100)
}

function shapeOne(toks: string[]): string {
  let i = 0
  while (i < toks.length && /^[A-Za-z_$][\w:.]*=/.test(toks[i])) i++     // FOO=1 cmd / $env:X=1
  const first = toks[i]
  if (!first) return ''
  const prog = basename(first.replace(/^["']|["']$/g, '')).replace(/\.(exe|cmd|bat|ps1|sh|ts|js)$/i, '')
  const out = [/^[A-Za-z][\w.-]{0,30}$/.test(prog) ? prog : '<程序>']
  let seenArg = false
  for (const t of toks.slice(i + 1)) {
    const flag = /^--?[A-Za-z0-9][\w-]{0,20}/.exec(t)
    if (flag && !t.startsWith('"') && !t.startsWith("'")) { out.push(flag[0]); continue }
    const bare = !/^["']/.test(t)
    if (!seenArg && PATTERN_PROGS.has(prog.toLowerCase())) out.push('<模式>')
    else if (bare && PATHISH.test(t)) out.push('<路径>')
    else if (!seenArg && bare && /^[a-z][a-z0-9-]{0,14}$/.test(t) && !PATTERN_PROGS.has(prog.toLowerCase())) out.push(t)   // git status / bun test 这类子命令
    else out.push('<参数>')
    seenArg = true
  }
  return out.join(' ')
}

/** 已知的坑（规格第一节 工具摩擦 ④）。只看命令文本，不看结果。 */
export function pitsOf(cmd: string): string[] {
  const p: string[] = []
  if (/(^|[\s"'=(])\/tmp(\/|\b)/.test(cmd)) p.push('`/tmp` 路径')
  if (/>\s*nul\b/i.test(cmd)) p.push('`> nul` 重定向')
  if (/\bcmd(\.exe)?\s+\/c\s+start\b/i.test(cmd)) p.push('`cmd /c start`')
  if (/(^|[\s"'=(])\/[A-Za-z]\/[\w.~-]/.test(cmd)) p.push(PIT_DRIVE)
  const hd = /<<-?\s*['"]?\w+/.exec(cmd)
  if (hd && cmd.slice(hd.index).includes('\n') && cmd.slice(hd.index).split('\n').slice(1).join('\n').includes('\\')) p.push(PIT_HEREDOC)
  return p
}

const normPath = (x: string) => x.replace(/\\/g, '/').toLowerCase()

/** 盘符路径（/d/x）是不是交给了 Windows 程序：同一段命令（按 | ; && 换行切开）的程序是 .exe、powershell、pwsh、cmd、go、python、py，且这一段里带盘符路径。 */
export function winProgIn(cmd: string): boolean {
  return cmd.split(/\r?\n|\|\||&&|[|;]/).some((seg) => {
    const toks = seg.trim().split(/\s+/)
    const prog = (toks[0] ?? '').replace(/^["']|["']$/g, '').split(/[\\/]/).pop()!.toLowerCase()
    if (!/\.(exe|cmd|bat)$/.test(prog) && !/^(powershell|pwsh|cmd|go|python3?|py)$/.test(prog)) return false
    return toks.slice(1).some((t) => /^["']?\/[A-Za-z]\/[\w.~-]/.test(t) || /=\/[A-Za-z]\/[\w.~-]/.test(t))
  })
}

/** heredoc 写入的目标文件（归一化），认不出写空。 */
export function heredocTarget(cmd: string): string {
  const first = cmd.split('\n')[0]
  const m = /<<-?\s*['"]?\w+['"]?[^\n]*?(?:>>?|\|\s*tee(?:\s+-a)?)\s*["']?([^\s"'|;&<]+)/.exec(first) ?? /(?:>>?|\btee\b(?:\s+-a)?)\s*["']?([^\s"'|;&<]+)[^\n]*<</.exec(first)
  return m ? normPath(m[1]) : ''
}

// ---------- 命中与汇总 ----------

export interface Hit {
  cat: string          // 工具摩擦 / 成本异常 / 插件和网络
  feature: string      // 归一化后的特征，不含原文
  ts: number
  where: string        // 来源 + 打码 id + 时间，不带原文
  est: number | null   // 估计多花的 token，算不出为 null
  advice: string
  noRepeat?: boolean   // 不进“反复出现”（只是提示性的常规噪声）
}

interface Row { cat: string; feature: string; n: number; est: number | null; where: string; advice: string; ts: number }

export function aggregate(hits: Hit[]): Row[] {
  const m = new Map<string, Row>()
  for (const h of hits) {
    const k = `${h.cat}\u0000${h.feature}`
    const r = m.get(k)
    if (!r) { m.set(k, { cat: h.cat, feature: h.feature, n: 1, est: h.est, where: h.where, advice: h.advice, ts: h.ts }); continue }
    r.n++
    if (h.est != null) r.est = (r.est ?? 0) + h.est
    if (h.ts < r.ts) { r.ts = h.ts; r.where = h.where }
  }
  return [...m.values()].sort((a, b) => (b.est ?? -1) - (a.est ?? -1) || b.n - a.n)
}

// ---------- 会话记录 ----------

interface Usage { out: number; total: number; hit: number; ts: number; order: number; skip: boolean }
interface Call {
  name: string; sig: string; path: string; cmdKey: string; isCmd: boolean
  msgId: string; ts: number; round: number
  err: boolean; notFound: boolean; crlf: boolean; pits: string[]; shape: string; winProg: boolean; hdTarget: string
}
interface Track { calls: Call[]; byId: Map<string, Call>; msgs: Map<string, Usage>; round: number; compacted: boolean }

async function* lines(path: string): AsyncGenerator<string> {
  const dec = new TextDecoder()
  let rest = ''
  for await (const chunk of createReadStream(path)) {
    rest += dec.decode(chunk as Buffer, { stream: true })
    let i: number
    while ((i = rest.indexOf('\n')) >= 0) { yield rest.slice(0, i); rest = rest.slice(i + 1) }
  }
  rest += dec.decode()
  if (rest) yield rest
}

const CMD_TOOLS = new Set(['Bash', 'PowerShell'])
const textOf = (c: unknown): string =>
  typeof c === 'string' ? c : Array.isArray(c) ? c.map((b: any) => (typeof b?.text === 'string' ? b.text : '')).join('\n') : ''

function describeCall(name: string, input: any): Pick<Call, 'sig' | 'path' | 'cmdKey' | 'isCmd' | 'pits' | 'shape' | 'winProg' | 'hdTarget'> {
  input = input && typeof input === 'object' ? input : {}
  if (CMD_TOOLS.has(name) && typeof input.command === 'string') {
    const sh = shape(input.command)
    return { sig: sh, path: '', cmdKey: input.command.replace(/\s+/g, ' ').trim(), isCmd: true, pits: pitsOf(input.command), shape: sh, winProg: winProgIn(input.command), hdTarget: heredocTarget(input.command) }
  }
  const path = typeof input.file_path === 'string' ? input.file_path : typeof input.path === 'string' ? input.path : ''
  const vals = Object.keys(input).sort().map((k) => (typeof input[k] === 'string' ? input[k] : '')).join(' ')
  return { sig: vals.replace(/\d+/g, 'N').slice(0, 200), path, cmdKey: '', isCmd: false, pits: [], shape: '', winProg: false, hdTarget: '' }
}

function track(): Track { return { calls: [], byId: new Map(), msgs: new Map(), round: 0, compacted: false } }

function feed(tracks: Map<string, Track>, j: any, start: number) {
  const ts = Date.parse(j?.timestamp)
  if (!Number.isFinite(ts) || ts < start) return
  const key0 = `${j.sessionId ?? ''}${j.isSidechain ? `#${j.agentId ?? 's'}` : ''}`
  if (j.isCompactSummary === true || (j.type === 'system' && j.subtype === 'compact_boundary')) { (tracks.get(key0) ?? tracks.set(key0, track()).get(key0)!).compacted = true; return }
  const msg = j.message
  if (!msg || typeof msg !== 'object') return
  const key = `${j.sessionId ?? ''}${j.isSidechain ? `#${j.agentId ?? 's'}` : ''}`
  let t = tracks.get(key)
  if (!t) tracks.set(key, (t = track()))
  if (j.type === 'assistant') {
    const id = String(msg.id ?? j.uuid ?? '')
    const u = msg.usage
    if (id && u && typeof u === 'object') {
      const total = (u.input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0) + (u.cache_read_input_tokens ?? 0)
      const old = t.msgs.get(id)
      const out = Math.max(old?.out ?? 0, u.output_tokens ?? 0)
      if (!old) { t.msgs.set(id, { out, total, hit: total ? (u.cache_read_input_tokens ?? 0) / total : 0, ts, order: t.msgs.size, skip: t.msgs.size === 0 || t.compacted }); t.compacted = false }
      else old.out = out
    } else if (id && !t.msgs.has(id)) t.msgs.set(id, { out: NaN, total: NaN, hit: NaN, ts, order: t.msgs.size, skip: true })
    for (const b of Array.isArray(msg.content) ? msg.content : []) {
      if (b?.type !== 'tool_use' || typeof b.id !== 'string') continue
      const c: Call = { name: String(b.name ?? ''), ...describeCall(String(b.name ?? ''), b.input), msgId: id, ts, round: t.round, err: false, notFound: false, crlf: false }
      t.calls.push(c)
      t.byId.set(b.id, c)
    }
  } else if (j.type === 'user') {
    const content = msg.content
    if (typeof content === 'string') { if (!j.isMeta) t.round++; return }
    let real = false
    for (const b of Array.isArray(content) ? content : []) {
      if (b?.type === 'tool_result') {
        const c = t.byId.get(b.tool_use_id)
        if (!c) continue
        c.err = b.is_error === true
        const txt = textOf(b.content)
        c.notFound = c.err && /string to replace not found|not found in file|找不到|未找到/i.test(txt)
        c.crlf = /\b(LF|CRLF) will be replaced by (CRLF|LF)\b/i.test(txt)     // 只留标记，结果原文立刻丢掉
      } else if (b?.type === 'text' && !j.isMeta) real = true
    }
    if (real) t.round++
  }
}

/** 失败那一步到后来那一步之间：中间各轮的输出 + 输入总量的增量。任一端没有用量就算不出。 */
function estSpan(t: Track, from: string, to: string): number | null {
  const a = t.msgs.get(from), b = t.msgs.get(to)
  if (!a || !b || Number.isNaN(a.total) || Number.isNaN(b.total)) return null
  let out = 0
  if (b.order > a.order) for (const m of t.msgs.values()) if (m.order > a.order && m.order <= b.order) out += Number.isNaN(m.out) ? 0 : m.out
  return out + Math.max(0, b.total - a.total)
}

const ADVICE = { tool: '观察', pit: '写 LESSONS', fix: '修工具' }

/** 已知的坑里要有佐证才算的两类：盘符路径要同一次调用报错或交给 Windows 程序；heredoc 要同一次报错或 3 步内又写同一个文件。 */
function keptPits(calls: Call[], i: number): string[] {
  const c = calls[i]
  return c.pits.filter((p) => {
    if (p === PIT_DRIVE) return c.err || c.winProg
    if (p === PIT_HEREDOC) {
      if (c.err) return true
      if (!c.hdTarget) return false
      return calls.slice(i + 1, i + 4).some((d) => (/^(write|edit)/i.test(d.name) && normPath(d.path) === c.hdTarget) || d.hdTarget === c.hdTarget)
    }
    return true
  })
}

function sessionHits(key: string, t: Track, ctx: { nameOf: (k: string) => string }): Hit[] {
  const hits: Hit[] = []
  const id = ctx.nameOf(key)
  const where = (ts: number) => `会话 ${maskId(id)} ${localTime(ts)}`
  const { calls } = t
  const pitEst = new Map<number, number | null>()

  // ① 失败后 3 步内同名、参数相似（或同一路径）的调用
  calls.forEach((c, i) => {
    if (!c.err) return
    for (let k = i + 1; k <= i + 3 && k < calls.length; k++) {
      const d = calls[k]
      if (d.name !== c.name || d.round !== c.round) continue
      if (!((c.path && c.path === d.path) || similar(c.sig, d.sig))) continue
      const est = estSpan(t, c.msgId, d.msgId)
      pitEst.set(i, est)
      hits.push({ cat: '工具摩擦', feature: `${c.name}：失败后换参数重试${c.isCmd && c.shape ? `（${head(c.shape)}）` : ''}`, ts: c.ts, where: where(c.ts), est, advice: ADVICE.tool })
      break
    }
  })

  // ② 同一命令一轮里跑 ≥ 2 次
  const seen = new Map<string, Call>()
  for (const c of calls) {
    if (!c.isCmd || c.cmdKey.length < 4) continue
    const k = `${c.round}\u0000${c.name}\u0000${c.cmdKey}`
    const first = seen.get(k)
    if (!first) { seen.set(k, c); continue }
    hits.push({ cat: '工具摩擦', feature: `${c.name}：同一命令一轮里重复执行（${head(c.shape)}）`, ts: c.ts, where: where(c.ts), est: estSpan(t, first.msgId, c.msgId), advice: ADVICE.tool })
  }

  // ③ Edit 找不到原文，随后又读同一文件
  calls.forEach((c, i) => {
    if (c.name !== 'Edit' || !c.notFound || !c.path) return
    for (let k = i + 1; k <= i + 3 && k < calls.length; k++) {
      if (calls[k].name === 'Read' && calls[k].path === c.path) {
        hits.push({ cat: '工具摩擦', feature: 'Edit：找不到原文后重读同一文件', ts: c.ts, where: where(c.ts), est: estSpan(t, c.msgId, calls[k].msgId), advice: ADVICE.tool })
        break
      }
    }
  })

  // ④ 已知的坑
  calls.forEach((c, i) => {
    for (const p of keptPits(calls, i)) hits.push({ cat: '工具摩擦', feature: `${c.name}：${p}`, ts: c.ts, where: where(c.ts), est: pitEst.get(i) ?? null, advice: ADVICE.pit })
    if (c.crlf) hits.push({ cat: '工具摩擦', feature: `${c.name}：输出里有 CRLF 换行警告`, ts: c.ts, where: where(c.ts), est: null, advice: ADVICE.tool, noRepeat: true })
  })

  // 成本异常 ①②
  hits.push(...costHits([...t.msgs.values()].filter((m) => m.total > 0), id, '会话'))
  return hits
}

interface CostRow { total: number; hit: number; ts: number; out: number; skip?: boolean }

/** 成本异常 ①命中率骤降 ②单轮输入总量超过中位数 3 倍。会话记录和 trace 的 model_runs 共用。 */
function costHits(rows: CostRow[], id: string, src: string): Hit[] {
  const hits: Hit[] = []
  const where = (ts: number) => `${src} ${maskId(id)} ${localTime(ts)}`
  for (let i = 1; i < rows.length; i++) {
    const a = rows[i - 1], b = rows[i]
    if (b.ts - a.ts > CACHE_GAP_MS || b.skip) continue
    if (a.total >= 2000 && a.hit - b.hit >= 0.3 && Math.abs(b.total - a.total) / a.total <= 0.5) {
      hits.push({ cat: '成本异常', feature: `${src}：相邻两轮缓存命中率掉 30 个百分点以上`, ts: b.ts, where: where(b.ts), est: Math.round((a.hit - b.hit) * b.total), advice: '观察（查前缀有没有被改）' })
    }
  }
  if (rows.length >= 5) {
    const s = rows.map((r) => r.total).sort((x, y) => x - y)
    const med = s.length % 2 ? s[(s.length - 1) / 2] : (s[s.length / 2 - 1] + s[s.length / 2]) / 2
    for (const r of rows) {
      if (!r.skip && r.total >= MIN_TOTAL && med > 0 && r.total > med * 3) hits.push({ cat: '成本异常', feature: `${src}：单轮输入总量超过本${src === '会话' ? '会话' : '对话'}中位数 3 倍`, ts: r.ts, where: where(r.ts), est: Math.round(r.total - med), advice: '观察' })
    }
  }
  return hits
}

export async function scanSessions(dir: string, start: number): Promise<Hit[]> {
  if (!existsSync(dir)) return []
  const files = readdirSync(dir).filter((n) => n.endsWith('.jsonl')).map((n) => join(dir, n)).filter((f) => statSync(f).mtimeMs >= start)
  const hits: Hit[] = []
  for (const f of files) {
    const tracks = new Map<string, Track>()
    for await (const line of lines(f)) {
      if (!line || line.charCodeAt(0) !== 123) continue
      let j: any
      try { j = JSON.parse(line) } catch { continue }
      if (j.type === 'assistant' || j.type === 'user' || j.type === 'system') feed(tracks, j, start)
    }
    for (const [key, t] of tracks) hits.push(...sessionHits(key, t, { nameOf: (k) => k.split('#')[0] || basename(f, '.jsonl') }))
  }
  return hits
}

// ---------- trace 库 ----------

/** 把正在被写的库连同 -wal、-shm 复制到临时目录，返回副本路径和清理函数。 */
export function copyTrace(src: string): { path: string; done: () => void } {
  const base = scratchBase()
  mkdirSync(base, { recursive: true })
  const dir = mkdtempSync(join(base, 'trace-'))
  const dst = join(dir, 'trace.db')
  copyFileSync(src, dst)
  for (const ext of ['-wal', '-shm']) if (existsSync(src + ext)) copyFileSync(src + ext, dst + ext)
  return { path: dst, done: () => rmSync(dir, { recursive: true, force: true }) }
}

function openCopy(path: string): Database {
  try { const d = new Database(path, { readonly: true }); d.query('SELECT 1 FROM sqlite_master LIMIT 1').get(); return d } catch { /* 带 WAL 的只读副本打不开时，退到可写的副本（副本是自己的，不碰原库） */ }
  return new Database(path)
}

const q = <T>(db: Database, sql: string, ...args: any[]): T[] => db.query(sql).all(...args) as T[]

interface StepRow { turn_id: string | null; name: string | null; args_json: string | null; is_error: number; started_at: number | null }

function stepCall(r: StepRow): Call {
  let a: any = {}
  try { a = r.args_json ? JSON.parse(r.args_json) : {} } catch { /* 参数不是 JSON，当没有 */ }
  const name = r.name ?? ''
  const cmd = typeof a?.command === 'string' ? a.command : typeof a?.cmd === 'string' ? a.cmd : undefined
  const d = cmd !== undefined ? describeCall('Bash', { command: cmd }) : describeCall(name, a)
  return { name, ...d, msgId: '', ts: r.started_at ?? 0, round: 0, err: r.is_error === 1, notFound: /edit/i.test(name) && r.is_error === 1, crlf: false }
}

export interface TraceResult { hits: Hit[]; plugins: PluginRow[]; notes: string[] }
export interface PluginRow { module: string; cls: string; n: number; base: number; expected: number }

export function scanTrace(db: Database, start: number, since: number, now: number): TraceResult {
  const hits: Hit[] = []
  const notes: string[] = []
  const tryQ = <T>(what: string, f: () => T, fallback: T): T => { try { return f() } catch (e) { notes.push(`${what}：查不到（${String((e as Error).message).slice(0, 60)}）`); return fallback } }

  // 工具调用（steps 表）：规则 ①②③④，没有每步用量，估计写“未估”
  const steps = tryQ('工具调用 steps', () => q<StepRow>(db, `SELECT turn_id, name, args_json, is_error, started_at FROM steps WHERE kind = 'tool' AND started_at >= ? ORDER BY turn_id, started_at, rowid`, start), [])
  const byTurn = new Map<string, Call[]>()
  for (const r of steps) { const k = r.turn_id ?? ''; (byTurn.get(k) ?? byTurn.set(k, []).get(k)!).push(stepCall(r)) }
  for (const [turn, calls] of byTurn) {
    const where = (ts: number) => `trace 回合 ${maskId(turn)} ${localTime(ts)}`
    calls.forEach((c, i) => {
      if (c.err) for (let k = i + 1; k <= i + 3 && k < calls.length; k++) {
        const d = calls[k]
        if (d.name === c.name && ((c.path && c.path === d.path) || similar(c.sig, d.sig))) {
          hits.push({ cat: '工具摩擦', feature: `${c.name}：失败后换参数重试${c.isCmd && c.shape ? `（${head(c.shape)}）` : ''}`, ts: c.ts, where: where(c.ts), est: null, advice: ADVICE.tool }); break
        }
      }
      if (/edit/i.test(c.name) && c.err && c.path) for (let k = i + 1; k <= i + 3 && k < calls.length; k++) {
        if (/read/i.test(calls[k].name) && calls[k].path === c.path) { hits.push({ cat: '工具摩擦', feature: 'Edit：找不到原文后重读同一文件', ts: c.ts, where: where(c.ts), est: null, advice: ADVICE.tool }); break }
      }
      for (const p of keptPits(calls, i)) hits.push({ cat: '工具摩擦', feature: `${c.name}：${p}`, ts: c.ts, where: where(c.ts), est: null, advice: ADVICE.pit })
    })
    const seen = new Map<string, number>()
    for (const c of calls) {
      const key = c.isCmd ? c.cmdKey : `${c.name}\u0000${c.sig}\u0000${c.path}`
      if (c.isCmd ? c.cmdKey.length < 4 : !c.sig && !c.path) continue
      if (seen.has(key)) hits.push({ cat: '工具摩擦', feature: `${c.name}：同一命令一轮里重复执行${c.shape ? `（${head(c.shape)}）` : ''}`, ts: c.ts, where: where(c.ts), est: null, advice: ADVICE.tool })
      seen.set(key, 1)
    }
  }

  // 成本异常（model_runs）：总量 = input + cacheRead，命中率 = cacheRead / 总量（LESSONS）
  type Run = { id: string; character_id: string; conversation: string; stage: string; started_at: number; input_tokens: number; cache_read_tokens: number; output_tokens: number }
  const runs = tryQ('模型调用 model_runs', () => q<Run>(db, `SELECT id, character_id, conversation, stage, started_at, input_tokens, cache_read_tokens, output_tokens FROM model_runs WHERE started_at >= ? AND error IS NULL ORDER BY character_id, conversation, stage, started_at`, start), [])
  const groups = new Map<string, Run[]>()
  for (const r of runs) { const k = `${r.character_id}\u0000${r.conversation}\u0000${r.stage}`; (groups.get(k) ?? groups.set(k, []).get(k)!).push(r) }
  for (const g of groups.values()) {
    const rows: CostRow[] = g.map((r, i) => { const total = r.input_tokens + r.cache_read_tokens; return { total, hit: total ? r.cache_read_tokens / total : 0, ts: r.started_at, out: r.output_tokens, skip: i === 0 } }).filter((r) => r.total > 0)   // trace 认不出压缩，只排每组第一轮
    hits.push(...costHits(rows, g[0].conversation, 'trace'))
  }

  // 成本异常 ③：群聊里调了回复模型，但前 10 分钟内同角色同群没有任何放行记录（enter / forced / merged）。
  // 不能拿“某条 skip 之后又有调用”来配对：2026-11-11 核实，那样配出的都是同一批消息里随后放行的那次（见规格第七节）。
  type Pass = { character_id: string; conversation: string; at: number }
  const passes = tryQ('门槛 gate_decisions', () => q<Pass>(db, `SELECT character_id, conversation, at FROM gate_decisions WHERE at >= ? AND conversation LIKE '%group%' AND verdict IN ('enter','forced','merged') ORDER BY at`, start - 600_000), [])
  const passAt = new Map<string, number[]>()
  for (const p of passes) { const k = `${p.character_id}\u0000${p.conversation}`; (passAt.get(k) ?? passAt.set(k, []).get(k)!).push(p.at) }
  for (const r of runs) {
    if (!/^reply/.test(r.stage) || !/group/.test(r.conversation)) continue
    const ok = (passAt.get(`${r.character_id}\u0000${r.conversation}`) ?? []).some((at) => at <= r.started_at + 1000 && at >= r.started_at - 600_000)
    if (ok) continue
    hits.push({ cat: '成本异常', feature: '群聊：回复模型被调用，但前 10 分钟内没有任何放行记录', ts: r.started_at, where: `trace 对话 ${maskId(r.conversation)} ${localTime(r.started_at)}`, est: r.input_tokens + r.output_tokens, advice: '观察（待核对来源）' })
  }

  // 插件和网络 ①：task_runs 失败和退回次数，和前 7 天平均比
  const bad = `outcome IN ('failed','timeout','degraded','skipped')`
  const win = Math.max(now - since, 60_000)
  const cur = tryQ('任务记录 task_runs', () => q<{ module: string; class: string; n: number }>(db, `SELECT module, class, count(*) n FROM task_runs WHERE at >= ? AND at <= ? AND ${bad} GROUP BY module, class`, since, now), [])
  const base = tryQ('任务记录 task_runs（前 7 天）', () => q<{ module: string; class: string; n: number }>(db, `SELECT module, class, count(*) n FROM task_runs WHERE at >= ? AND at < ? AND ${bad} GROUP BY module, class`, since - 7 * DAY, since), [])
  const baseMap = new Map(base.map((r) => [`${r.module}\u0000${r.class}`, r.n]))
  const plugins: PluginRow[] = []
  for (const r of cur) {
    const b = baseMap.get(`${r.module}\u0000${r.class}`) ?? 0
    const expected = (b * win) / (7 * DAY)
    if (r.n >= 3 && r.n >= expected * 2) plugins.push({ module: r.module, cls: r.class, n: r.n, base: b, expected })
  }
  plugins.sort((a, b) => b.n - a.n)
  return { hits, plugins, notes }
}

// ---------- 连通性（只连本机，只读，不发协议帧） ----------

function tcpProbe(host: string, port: number, ms = 2000): Promise<boolean> {
  return new Promise((res) => {
    const s = createConnection({ host, port })
    const end = (ok: boolean) => { s.destroy(); res(ok) }
    s.setTimeout(ms, () => end(false))
    s.once('connect', () => end(true))
    s.once('error', () => end(false))
  })
}

const LOCAL = new Set(['127.0.0.1', 'localhost', '::1', '0.0.0.0'])

export async function checkNetwork(snowlumaDir: string): Promise<string[]> {
  const out: string[] = []
  try {
    const r = await fetch('http://127.0.0.1:3425/v1/models', { signal: AbortSignal.timeout(2000) })
    out.push(`Magpie（本机 3425）：${r.ok ? '通' : `有响应但状态 ${r.status}`}`)
  } catch (e) { out.push(`Magpie（本机 3425）：连不上（${(e as Error).name === 'TimeoutError' ? '2 秒超时' : '拒绝连接'}）`) }
  out.push(`代理（本机 7897）：${(await tcpProbe('127.0.0.1', 7897)) ? '通' : '连不上'}`)
  const targets: { name: string; host: string; port: number }[] = []
  try {
    for (const f of readdirSync(snowlumaDir).filter((n) => /^onebot_.*\.json$/.test(n))) {
      const cfg = JSON.parse(readFileSync(join(snowlumaDir, f), 'utf8'))
      for (const kind of ['httpServers', 'wsServers']) for (const s of cfg?.networks?.[kind] ?? []) {
        if (typeof s?.port === 'number') targets.push({ name: kind === 'wsServers' ? 'WS' : 'HTTP', host: String(s.host ?? '127.0.0.1'), port: s.port })
      }
    }
  } catch { /* 读不到配置 */ }
  if (!targets.length) out.push('SnowLuma：未查（读不到配置里的地址）')
  for (const [i, t] of targets.entries()) {
    if (!LOCAL.has(t.host)) { out.push(`SnowLuma 服务 ${i + 1}（${t.name}）：未查（不是本机地址）`); continue }
    const ok = await tcpProbe(t.host === '0.0.0.0' ? '127.0.0.1' : t.host, t.port)
    out.push(`SnowLuma 服务 ${i + 1}（${t.name}，本机 ${t.port}）：${ok ? '通' : '连不上'}`)
  }
  return out
}

// ---------- 报告 ----------

const fmtTok = (n: number | null) => (n == null ? '未估' : n >= 10_000 ? `${(n / 1000).toFixed(1)}k` : String(n))
const cell = (s: string) => s.replace(/\|/g, '/')

export interface Options { since: number; now: number; trace: string; sessions: string; out: string; network: boolean; snowluma: string }

export function render(o: { since: number; now: number; hits: Hit[]; all: Hit[]; trace?: TraceResult; net: string[] | null; notes: string[] }): string {
  // CRLF 换行警告是 git 的常规提示，不算摩擦：不进榜，只在榜下记个数（复盘 2026-11-11：一天占 26 次、纯噪音）
  const allRows = aggregate(o.hits)
  const crlf = allRows.filter((r) => r.feature.includes('CRLF 换行警告'))
  const rows = allRows.filter((r) => !crlf.includes(r))
  const top = rows.slice(0, 5)
  const L: string[] = []
  L.push(`# 摩擦报告 ${localDate(o.now)}`, '', `范围：${localTime(o.since)} 起到 ${localTime(o.now)}。只用规则数，不调模型；位置只给打码后的编号和时间。`, '')
  L.push('## 一、摩擦榜（按估计多花的 token 排前五）', '')
  if (!top.length) L.push('没有', '')
  else {
    L.push('| 类别 | 特征 | 次数 | 约多花 token | 例子位置 | 建议 |', '|---|---|---|---|---|---|')
    for (const r of top) L.push(`| ${r.cat} | ${cell(r.feature)} | ${r.n} | ${fmtTok(r.est)} | ${r.where} | ${r.advice} |`)
    if (rows.length > 5) L.push('', `另有 ${rows.length - 5} 条没上榜。`)
    L.push('')
  }
  if (crlf.length) L.push(`（git 的 CRLF 换行提示 ${crlf.reduce((n, r) => n + r.n, 0)} 次，不算摩擦、不进榜。）`, '')

  L.push('## 二、成本和网络', '')
  const cost = rows.filter((r) => r.cat === '成本异常')
  L.push('成本异常：', ...(cost.length ? cost.map((r) => `- ${r.feature}，${r.n} 次，约多花 ${fmtTok(r.est)} token，例 ${r.where}`) : ['- 没有']), '')
  L.push('插件和站点（失败和退回次数，和前 7 天平均比，高一倍以上）：')
  if (!o.trace) L.push('- 未查（没有 trace 库）')
  else if (!o.trace.plugins.length) L.push('- 没有')
  else for (const p of o.trace.plugins) L.push(`- ${p.module} · ${p.cls}：本期 ${p.n} 次，前 7 天折合同期约 ${p.expected.toFixed(1)} 次（共 ${p.base}）`)
  L.push('- 站点维度：task_runs 没有稳定的站点字段，按“模块 · 类别”合并。', '')
  L.push('连通性（只连本机）：', ...(o.net ? o.net.map((x) => `- ${x}`) : ['- 未查（--no-network）']), '')
  if (o.notes.length) L.push('未能检查的项目：', ...o.notes.map((n) => `- ${n}`), '')

  L.push('## 三、反复出现', '')
  const today0 = new Date(o.now); today0.setHours(0, 0, 0, 0)
  const weekStart = today0.getTime() - 6 * DAY     // 含今天共 7 个日历日
  const days = new Map<string, { days: Set<string>; row: Hit }>()
  for (const h of o.all) {
    if (h.ts < weekStart) continue
    const k = `${h.cat}\u0000${h.feature}`
    const e = days.get(k) ?? { days: new Set(), row: h }
    e.days.add(localDate(h.ts))
    days.set(k, e)
  }
  const rep = [...days.values()].filter((e) => e.days.size >= 3 && !e.row.noRepeat).sort((x, y) => y.days.size - x.days.size)
  const resident = rep.filter((e) => e.days.size >= 6)      // 几乎天天出现的“常驻”，只占两行
  const others = rep.filter((e) => e.days.size < 6)
  const line = (e: { days: Set<string>; row: Hit }) => `- ${e.row.cat}：${e.row.feature}，最近 7 天里 ${e.days.size} 天出现，建议修到工具层`
  if (!rep.length) L.push('没有')
  else {
    for (const e of resident.slice(0, 2)) L.push(line(e))
    if (resident.length > 2) L.push(`- 另有 ${resident.length - 2} 条常驻（6 天以上出现）`)
    for (const e of others) L.push(line(e))
  }
  L.push('')
  return scrub(L.join('\n'))
}

export async function run(o: Options): Promise<{ report: string; hits: Hit[] }> {
  const start = Math.min(o.since, o.now - 7 * DAY)     // “反复出现”要看最近 7 天
  const all: Hit[] = [...(await scanSessions(o.sessions, start))]
  const notes: string[] = []
  let trace: TraceResult | undefined
  if (existsSync(o.trace)) {
    const copy = copyTrace(o.trace)
    try {
      const db = openCopy(copy.path)
      try { trace = scanTrace(db, start, o.since, o.now); all.push(...trace.hits); notes.push(...trace.notes) } finally { db.close() }
    } catch (e) { notes.push(`trace 库：打不开（${String((e as Error).message).slice(0, 60)}）`) } finally { copy.done() }
  } else notes.push('trace 库：不存在，没查')
  const net = o.network ? await checkNetwork(o.snowluma) : null
  const hits = all.filter((h) => h.ts >= o.since)
  return { report: render({ since: o.since, now: o.now, hits, all, trace, net, notes }), hits }
}

// ---------- 入口 ----------

function parseArgs(argv: string[]): Options {
  const now = Date.now()
  const v: Record<string, string> = {}
  let network = true
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--no-network') network = false
    else if (a.startsWith('--')) v[a.slice(2)] = argv[++i] ?? ''
    else throw new Error(`不认识的参数：${a}`)
  }
  const main = mainRoot()
  return {
    since: parseSince(v.since ?? '24h', now),
    now,
    trace: v.trace ?? join(main, 'data', 'go', 'trace.db'),
    sessions: v.sessions ?? join(homedir(), '.claude', 'projects', 'D--Projects-Code-sumika'),
    out: v.out ?? join(main, 'data', 'reports', `摩擦-${localDate(now)}.md`),
    network,
    snowluma: v.snowluma ?? 'D:\\Dev\\SnowLuma\\config',
  }
}

if (import.meta.main) {
  const o = parseArgs(process.argv.slice(2))
  const { report, hits } = await run(o)
  mkdirSync(dirname(o.out), { recursive: true })
  writeFileSync(o.out, report)
  console.log(`摩擦报告已写入 ${o.out}（命中 ${hits.length} 次）`)
}
