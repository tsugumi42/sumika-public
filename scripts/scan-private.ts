// 隐私扫描（规格-提交前检查 第三节）：新增的行、文件名、提交说明和作者里有没有本机认识的真实号码、私人字符串（邮箱等）、像密钥的串、本机的真实昵称和群名。
// 挂在 git 钩子上（lefthook.yml），也能手动跑。
//   bun scripts/scan-private.ts --staged              暂存区（提交前）
//   bun scripts/scan-private.ts --range A..B          这段提交的新增行、提交说明、作者和提交者（推送前）
//   bun scripts/scan-private.ts --allow 编号 [编号…]   把上次报出来、核对过不是真实的那几条登记成误报（只存哈希）
//   --allow-missing                                   名单读不到时也照常跑（默认拒绝：没有名单就认不出真号码）
//
// 号码分档（2026-11-11 改，以前只看“长得像不像号码”，浏览器 bug 号、时间常量、UUID 的零一律被拦）：
//   · 拦：和本机认识的号码完全相同（5～12 位，写在哪都算）；本机不认识、但前面紧挨着 qq、uin、群、号、group、user 这类词的 8 位以上数字；11 位手机号。
//   · 提醒（不拦）：其余本机不认识、长得像号码的 8～11 位数字；名单外的邮箱。
// 名单读不到（缺聊天库、读失败、认识的号码一个都没有）就退出码 2，不悄悄降级成只提醒（复核 2026-11-11 第 1 条）。
// 所有名单只在内存里；输出只有打了码的样子和位置，位置里出现认识的号码或名字也打码。
import { Database } from 'bun:sqlite'
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

export const hash = (s: string) => createHash('sha256').update(s).digest('hex').slice(0, 16)
const mask = (s: string, keep = 2) => s.slice(0, keep) + '＊'.repeat(Math.max(1, [...s].length - keep))

// ───── 纯函数（测试直接调） ─────
export type Line = { where: string; text: string; file?: string }
export type Hit = { kind: string; value: string; where: string; file?: string; block: boolean }
export type Known = { numbers: Set<string>; names: Set<string>; secrets?: Set<string> }   // secrets：私人字符串名单（邮箱等），已转小写

// 任意位置的 5～12 位数字串（两边不是数字）：认识的号码不管写在文件名、下划线后、JSON 里、网址里都拦
const digits = /(?<!\d)(\d{5,12})(?!\d)/g
// 不认识的长数字只在“像号码”时提醒：8～11 位、不以 0 开头、不是小数版本号、不在网址里，前后不是 bug、issue、#、max-age 这类
const looksLikeNumber = /(?<![\d.\-/#A-Za-z])([1-9]\d{7,10})(?!\d|\.\d)/g
const notNumberBefore = /(?:bug|issue|issues|crbug|pr|cve-\d{4}-|max-age=|port|0x|v)\s*[#:=]?\s*$/i
// 前面紧挨着这些词的不认识数字当号码拦：AI 会话读了本机 QQ 记录把样本贴进文档，Sumika 库里没有这些人（复核 2026-11-11 第五节第 4 条）
const numberContext = /(?:qq|uin|user_?id|group_?id|gid|uid|群|号|账号|好友)\s*[:：=＝]?\s*["'`]?\s*$/i
const mobile = /(?<![\d.\-/#A-Za-z])(1[3-9]\d{9})(?!\d|\.\d)/g
const keys: [string, RegExp][] = [
  ['sk-', /(?<![A-Za-z0-9])sk-[A-Za-z0-9_-]{16,}/g], ['GitHub 令牌', /gh[pousr]_[A-Za-z0-9]{20,}/g], ['Bearer', /Bearer\s+[A-Za-z0-9._-]{20,}/g],
  ['赋值的密钥', /(?:api[_-]?key|secret|token|password)\s*[:=]\s*['"]?[A-Za-z0-9_\-./+]{16,}/gi], ['私钥', /-----BEGIN [A-Z ]*PRIVATE KEY-----/g],
]
// 邮箱：名单里的（主人的真实邮箱等）照私人字符串拦，比较前去掉 +tag、解开 %40；别的邮箱只提醒，noreply、示例域名不提醒
const email = /[A-Za-z0-9._%+-]+(?:@|%40)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/gi
const harmlessMail = /noreply|no-reply|@(?:[a-z0-9-]+\.)*(?:example\.(?:com|org|net)|invalid|test|localhost|local)$|@anthropic\.com$/
export const normMail = (e: string) => e.toLowerCase().replace(/%40/g, '@').replace(/\+[^@]*@/, '@')
// 英文名字要整词对上（名单里一个四个字母的昵称，正好是 OpenCode 中间那几个字母，10-26 第一次试跑就误报 31 处；
// 边界情况清单 ⑮，eb17e497 记忆别名踩过同一个坑）；中文名字照旧按包含算
const latin = (c: string | undefined) => !!c && /[A-Za-z0-9_]/.test(c)
function wholeWord(text: string, name: string) {
  if (!/[A-Za-z0-9]/.test(name)) return text.includes(name)
  for (let i = text.indexOf(name); i >= 0; i = text.indexOf(name, i + 1))
    if (!(latin(name[0]) && latin(text[i - 1])) && !(latin(name.at(-1)) && latin(text[i + name.length]))) return true
  return false
}

export function scan(lines: Line[], known: Known): Hit[] {
  const hits: Hit[] = []
  for (const { where, text, file } of lines) {
    if (/(?:^|\/)(?:bun\.lock|go\.sum|hutch\.lock)$/.test(file ?? '')) continue   // 锁文件里全是哈希
    const push = (kind: string, value: string, block: boolean) => hits.push({ kind, value, where, file, block })
    for (const m of text.matchAll(digits)) if (known.numbers.has(m[1]!)) push('号码', m[1]!, true)
    const flagged = new Set<string>()
    for (const m of text.matchAll(mobile)) if (!known.numbers.has(m[1]!) && !/https?:\/\/\S*$/.test(text.slice(0, m.index))) { push('手机号', m[1]!, true); flagged.add(m[1]!) }
    for (const m of text.matchAll(looksLikeNumber)) {
      const s = m[1]!
      if (known.numbers.has(s) || flagged.has(s)) continue                                   // 上面已经拦了
      const before = text.slice(0, m.index)
      if (/https?:\/\/\S*$/.test(before)) continue                                           // 网址里的
      if (numberContext.test(before)) { push('像号码的数字', s, true); continue }           // qq: 12345678、群号 12345678
      if (notNumberBefore.test(before)) continue                                             // bug 1882408、max-age=31536000
      if (s.length === 10 && /^17[89]/.test(s)) continue                                     // 2026 年前后的秒级时间戳
      if (s.length === 8 && /^20[2-3]\d(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])$/.test(s)) continue   // 日期 20261026
      push('没见过的长数字', s, false)
    }
    for (const [label, re] of keys) for (const m of text.matchAll(re)) push(label, m[0], true)
    const lower = text.toLowerCase()
    for (const s of known.secrets ?? []) if (lower.includes(s)) push('私人字符串', s, true)
    for (const m of text.matchAll(email)) {
      const e = normMail(m[0])
      if (known.secrets?.has(e)) { if (!lower.includes(e)) push('私人字符串', e, true); continue }   // +tag、%40 写法：原文里找不到整串时补拦
      if (harmlessMail.test(e)) continue
      push('邮箱', e, false)
    }
    for (const n of known.names) if (wholeWord(text, n)) push('真实名字', n, true)
  }
  return hits
}

// 把 git diff 读成要扫的行。“+++ ”只在每个文件的块头（“diff --git”之后、第一个“@@”之前）算文件名；正文里以“++ ”开头的新增行照样扫
// （复核 2026-11-11 第 2 条：以前那一行被当成文件头吞掉，号码没扫，还把它当文件名打出来）。文件名本身也作为一行扫。
export function collect(diff: string, label: (file: string, n: number) => string): Line[] {
  const out: Line[] = []
  let file = '', n = 0, inHeader = false
  for (const l of diff.split(/\r?\n/)) {
    if (l.startsWith('diff --git ')) { inHeader = true; file = ''; continue }
    if (inHeader) {
      if (l.startsWith('+++ ')) { file = l.startsWith('+++ b/') ? l.slice(6) : ''; if (file) out.push({ where: `${label(file, 0).replace(/:0$/, '')} 文件名`, text: file, file }); continue }
      if (l.startsWith('rename to ')) { file = l.slice(10); out.push({ where: `${file} 文件名`, text: file, file }); continue }
      if (!l.startsWith('@@')) continue
      inHeader = false
    }
    const h = /^@@ -\d+(?:,\d+)? \+(\d+)/.exec(l)
    if (h) { n = Number(h[1]) - 1; continue }
    if (l.startsWith('+')) { n++; out.push({ where: label(file, n), text: l.slice(1), file }) }
    else if (!l.startsWith('-') && !l.startsWith('\\')) n++
  }
  return out
}

// 输出的位置里也可能有号码或名字（文件名带号码时），打码再打印
export function maskWhere(where: string, known: Known): string {
  let w = where.replace(digits, (d) => (known.numbers.has(d) ? mask(d) : d))
  for (const n of known.names) if (w.includes(n)) w = w.split(n).join(mask(n, 1))
  return w
}

// 误报登记：认识的号码、私人字符串只能按“值 + 文件”登记，不全局放行（复核 2026-11-11 第 5 条：先登了不认识的数字，后来它进了名单也永远不拦）；
// 其余按值登记，和以前一样。旧登记（只有值的哈希）照旧只对按值登记的那几类生效。
const perFile = (kind: string) => kind === '号码' || kind === '私人字符串'
export const allowKey = (h: Hit) => (perFile(h.kind) ? `file:${hash(`${h.kind}|${h.value}|${h.file ?? ''}`)}` : hash(h.value))

// ───── 本机名单（只读） ─────
// privateJSON：主工作区 data/dev/private-strings.json（不进 git），一个字符串数组，写主人的真实邮箱、别处的账号名这类
export type Sources = { qqDB: string; maibotDB: string; groupsJSON: string; configYML: string; privateJSON?: string }
export class SourceError extends Error {}
export function loadKnown(src: Sources): { known: Known; notes: string[] } {
  const numbers = new Set<string>(), names = new Set<string>(), secrets = new Set<string>(), notes: string[] = []
  const addNums = (v: unknown) => { for (const m of String(v ?? '').matchAll(digits)) numbers.add(m[1]!) }
  const addName = (v: unknown) => { const s = typeof v === 'string' ? v.trim() : ''; if ([...s].length >= 3 && [...s].length <= 20 && !/^\d+$/.test(s)) names.add(s) }
  // 读失败只报是哪个来源、什么类，不带出文件内容（复核第 8 条：JSON 报错会把名单片段打到终端）
  const guard = <T>(what: string, f: () => T): T => { try { return f() } catch (e) { throw new SourceError(`${what}读不了（${(e as Error).name}）`) } }
  const tables = (db: Database) => new Set((db.query("SELECT name FROM sqlite_master WHERE type='table'").all() as { name: string }[]).map((r) => r.name))
  const cols = (db: Database, t: string) => new Set((db.query(`PRAGMA table_info(${t})`).all() as { name: string }[]).map((r) => r.name))
  if (src.privateJSON && existsSync(src.privateJSON)) {
    const v = guard('私人字符串名单', () => JSON.parse(readFileSync(src.privateJSON!, 'utf8')))
    if (!Array.isArray(v)) throw new SourceError('私人字符串名单不是数组')
    for (const s of v) if (typeof s === 'string' && s.trim().length >= 4) secrets.add(normMail(s.trim()))
  }
  if (existsSync(src.qqDB)) guard('Go 聊天库', () => {
    const db = new Database(src.qqDB, { readonly: true })
    try {
      const t = tables(db)
      for (const r of db.query("SELECT DISTINCT actor_id a FROM events WHERE actor_kind != 'character'").all() as { a: string }[]) addNums(r.a)
      for (const r of db.query('SELECT DISTINCT conversation c FROM events').all() as { c: string }[]) addNums(r.c)
      // 只取号码不取昵称：聊天库里几百个昵称有不少是常见英文词，加进名字名单全仓误报四千多处（2026-11-11 试过）；名字照旧用 MaiBot 和群名表
      for (const r of db.query('SELECT uid FROM qq_people').all() as { uid: string | null }[]) addNums(r.uid)
      for (const r of db.query('SELECT gid, name FROM qq_groups').all() as Record<string, string | null>[]) { addNums(r.gid); addName(r.name) }
      // 她自己的号（复核第 4 条：配置里 `- id: <号>` 那行没有关键字）：这几张表的 account 列
      for (const tb of ['qq_sync', 'outbound_plans', 'qq_alerts', 'qq_backfill_continuity', 'channel_alignment_latest'])
        if (t.has(tb) && cols(db, tb).has('account')) for (const r of db.query(`SELECT DISTINCT account a FROM ${tb}`).all() as { a: unknown }[]) addNums(r.a)
    } finally { db.close() }
  }); else notes.push('缺 Go 聊天库')
  if (existsSync(src.maibotDB)) guard('MaiBot 库', () => {
    const db = new Database(src.maibotDB, { readonly: true })
    try {
      const t = tables(db)
      for (const r of db.query('SELECT user_id, person_name, user_nickname, group_cardname FROM person_info').all() as Record<string, string | null>[]) {
        addNums(r.user_id); addName(r.person_name); addName(r.user_nickname); addName(r.group_cardname)
      }
      const nums: [string, string[]][] = [['chat_sessions', ['group_id', 'user_id', 'account_id']], ['mai_messages', ['user_id', 'group_id', 'account_id']], ['bot_platform_accounts', ['account_id']]]
      for (const [tb, want] of nums) {
        if (!t.has(tb)) { notes.push(`MaiBot 库没有 ${tb}`); continue }
        const have = want.filter((c) => cols(db, tb).has(c))
        if (have.length) for (const r of db.query(`SELECT DISTINCT ${have.join(', ')} FROM ${tb}`).all() as Record<string, unknown>[]) Object.values(r).forEach(addNums)
      }
      if (t.has('chat_sessions') && cols(db, 'chat_sessions').has('group_name')) for (const r of db.query('SELECT DISTINCT group_name g FROM chat_sessions').all() as { g: string | null }[]) addName(r.g)
    } finally { db.close() }
  }); else notes.push('缺 MaiBot 库')
  if (existsSync(src.groupsJSON)) {
    const walk = (v: unknown): void => { if (typeof v === 'string' || typeof v === 'number') { addNums(v); addName(v) } else if (v && typeof v === 'object') { Object.keys(v).forEach(addNums); Object.values(v).forEach(walk) } }
    walk(guard('群名表', () => JSON.parse(readFileSync(src.groupsJSON, 'utf8'))))
  } else notes.push('缺群名表')
  if (existsSync(src.configYML)) {
    // 配置的 qq 子树里的数字全收（她的号在 `- id:` 那行，多行列表也能读到）；别的段只收写在 QQ 相关键上的
    const cfg = guard('配置文件', () => Bun.YAML.parse(readFileSync(src.configYML, 'utf8'))) as Record<string, unknown> | null
    const walk = (v: unknown, qq: boolean): void => {
      // qq 段里的端口、超时、上限这类数字不是号（键名认）
      // 只看键名的开头和结尾：中间含 count 的 accounts 是号（2026-11-11 误伤过一次）
      const notId = (k: string) => /(?:port|timeout|interval|limit|seconds|minutes|delay|ttl|size|after)$/i.test(k) || /^(?:max|min|retr)/i.test(k) || /(?:Count|Ms)$/.test(k)
      if (v && typeof v === 'object') { for (const [k, x] of Object.entries(v)) if (!notId(k)) walk(x, qq || /qq|uin|owner|group|account|conversation/i.test(k)) }
      else if (qq && (typeof v === 'string' || typeof v === 'number')) addNums(v)
    }
    walk(cfg, false)
  } else notes.push('缺配置文件')
  return { known: { numbers, names, secrets }, notes }
}

// ───── 命令行 ─────
if (import.meta.main) {
  const ROOT = resolve(import.meta.dir, '..')
  // 误报登记和名单都在主工作区的 data/（worktree 里没有 data/）。--git-common-dir 在主工作区返回相对路径 .git，要按 ROOT 解开，
  // 不能按当前目录（复核第 1 条：从子目录跑时找不到聊天库，号码全降成提醒）
  const common = Bun.spawnSync(['git', 'rev-parse', '--git-common-dir'], { cwd: ROOT }).stdout.toString().trim()
  const MAIN = resolve(ROOT, common, '..')
  const ALLOW = join(MAIN, 'data/dev/private-scan-allow.json')
  const LAST = join(MAIN, 'data/dev/private-scan-last.json')
  const allowed = new Set<string>(existsSync(ALLOW) ? JSON.parse(readFileSync(ALLOW, 'utf8')) : [])
  const args = process.argv.slice(2).filter((a) => a !== '--allow-missing')
  const allowMissing = process.argv.includes('--allow-missing')

  if (args[0] === '--allow') {
    const last: Record<string, string> = existsSync(LAST) ? JSON.parse(readFileSync(LAST, 'utf8')) : {}
    for (const id of args.slice(1)) {
      if (!last[id]) { console.error(`没有编号 ${id}（只认上一次扫描报出来的编号）`); process.exit(1) }
      allowed.add(last[id]!)
    }
    writeFileSync(ALLOW, JSON.stringify([...allowed].sort(), null, 1))
    console.log(`登记了 ${args.length - 1} 条误报，一共 ${allowed.size} 条（${ALLOW}）`)
    process.exit(0)
  }

  let loaded: ReturnType<typeof loadKnown>
  try {
    loaded = loadKnown({
      // SUMIKA_SCAN_MAIBOT 只给测试换成编的库；它只影响名字和号码的一部分来源，聊天库缺了照样拒绝
      qqDB: join(MAIN, 'data/go/chat/qq.db'), maibotDB: process.env.SUMIKA_SCAN_MAIBOT ?? '',
      groupsJSON: join(MAIN, 'data/dev/human-replies/groups.json'), configYML: join(MAIN, 'data/config/sumika.yml'),
      privateJSON: join(MAIN, 'data/dev/private-strings.json'),
    })
  } catch (e) {
    console.error(`隐私扫描：名单${e instanceof SourceError ? e.message : '读不了'}，没扫。修好名单再提交；确实要跳过名单就加 --allow-missing`)
    process.exit(2)
  }
  const { known, notes } = loaded
  if (!allowMissing && (notes.includes('缺 Go 聊天库') || known.numbers.size === 0)) {
    console.error(`隐私扫描：没有本机号码名单（${notes.join('、') || '认识的号码 0 个'}），认不出真号码，没扫。数据目录：${MAIN}\\data；确实要跳过名单就加 --allow-missing`)
    process.exit(2)
  }

  const lines: Line[] = []
  const git = (a: string[]) => { const p = Bun.spawnSync(['git', '-c', 'core.quotepath=false', ...a], { cwd: ROOT }); if (p.exitCode) { console.error(p.stderr.toString()); process.exit(2) } return p.stdout.toString() }
  if (args[0] === '--staged') lines.push(...collect(git(['diff', '--cached', '-U0', '--no-color', '-M']), (f, n) => `${f}:${n}`))
  else if (args[0] === '--range') {
    // 合并提交也扫（复核第 12 条：冲突解决改的内容只有这里查得到）：--remerge-diff 只给出“自动合并的结果”到“实际提交”之间的改动，格式和普通 diff 一样
    for (const c of git(['rev-list', args[1]!]).split(/\s+/).filter(Boolean)) {
      const short = c.slice(0, 8)
      for (const l of git(['log', '-1', '--format=%B', c]).split(/\r?\n/)) lines.push({ where: `${short} 提交说明`, text: l })
      lines.push({ where: `${short} 作者和提交者`, text: git(['log', '-1', '--format=%an <%ae> %cn <%ce>', c]).trim() })
      const merge = git(['rev-list', '--parents', '-n', '1', c]).trim().split(' ').length > 2
      lines.push(...collect(git(['show', '--format=', '-U0', '--no-color', '-M', ...(merge ? ['--remerge-diff'] : []), c]), (f, n) => `${short} ${f}:${n}`))
    }
  } else { console.error('用法：--staged | --range A..B | --allow 编号… [--allow-missing]'); process.exit(2) }

  const real = scan(lines, known).filter((h) => !allowed.has(allowKey(h)))
  const groups = new Map<string, Hit[]>()
  for (const h of real) { const k = allowKey(h); groups.set(k, [...(groups.get(k) ?? []), h]) }
  console.log(`隐私扫描：${lines.length} 行；认识的号码 ${known.numbers.size} 个、名字 ${known.names.size} 个、私人字符串 ${known.secrets?.size ?? 0} 条${notes.length ? `（${notes.join('、')}）` : ''}；登记过的误报 ${allowed.size} 条`)
  const ids: Record<string, string> = {}
  let i = 0, blocking = 0
  for (const [k, hs] of groups) {
    const id = String(++i); ids[id] = k
    const h = hs[0]!
    if (h.block) blocking++
    const digitsLike = /号码|数字|手机号/.test(h.kind)
    console.log(`  [${id}] ${h.block ? '拦' : '提醒'} ${h.kind} ${mask(h.value, digitsLike ? 2 : 1)}（${[...h.value].length} 个字符）  ${hs.length} 处，例：${maskWhere(h.where, known)}`)
  }
  writeFileSync(LAST, JSON.stringify(ids))
  if (blocking) {
    console.log(`有 ${blocking} 个要拦的：本机认识的号码、像号码的数字、私人字符串、真实名字或像密钥的串。是真实的就改掉再提交；核对过不是就 bun scripts/scan-private.ts --allow 编号（认识的号码只对这个文件放行）`)
    process.exit(1)
  }
  if (groups.size) console.log(`只有提醒：${groups.size} 个本机不认识的长数字或邮箱，看一眼不是号码就行（不想再看到就 --allow 编号）`)
  else console.log('没发现')
}
