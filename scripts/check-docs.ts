// 文档一致性检查（ADR-065 第 7 条；对应 2026-10-17 文档体检真出过的问题，不预先造检查）。
// 只读：不开后台、不碰 data/。每日巡检跑一遍，新增的违规转进待办。
//   bun run check:docs            查一遍，有"错"就退出码 1
//   bun run check:docs --update   把旧名、长数字的现状写成基线（只在确认是有意的以后用）
//   bun scripts/check-docs.ts --staged   读暂存区里的版本、不读工作区（提交前的钩子用：别的会话改了一半没暂存的文件不拦这次提交，2026-10-26）。
//                                        D11 的记忆目录不在仓库里
// D5（示例配置对 Bun 的 config.ts）、D6（插件顺序对 daemon/main.ts）2026-11-01 分仓时删了：Bun 的代码在 sumika-bun 仓库，
// 示例配置的默认值以 Go 的 native/internal/config 为准（盘点-Bun和Go分仓-2026-11-01）
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'
import { mergedIntoMain, staleMergeNote } from './check-spec-merged'

const ROOT = resolve(import.meta.dir, '..')
const BASELINE = 'scripts/docs-baseline.json'
const { values: opts } = parseArgs({ args: process.argv.slice(2), options: { update: { type: 'boolean' }, staged: { type: 'boolean' } } })
const update = opts.update
const git = (args: string[], stdin?: Uint8Array) => Bun.spawnSync(['git', '-c', 'core.quotepath=false', ...args], { cwd: ROOT, stdin }).stdout
const tracked = (): string[] => git(['ls-files']).toString().split('\n').map((s) => s.trim()).filter(Boolean)
// 暂存区的版本一次读完（一个 cat-file --batch；一个文件起一次 git 太慢）。输出是"<哈希> <类型> <字节数>\n<内容>\n"，没有的是"<名字> missing\n"
const staged = new Map<string, string>()
if (opts.staged) {
  const want = tracked().filter((f) => /\.(md|ts|tsx|css|ya?ml|json)$/.test(f))
  const out = git(['cat-file', '--batch'], new TextEncoder().encode(want.map((f) => `:${f}\n`).join('')))
  let at = 0
  for (const f of want) {
    const nl = out.indexOf(10, at)
    const head = out.subarray(at, nl).toString().split(' ')
    at = nl + 1
    if (head.length !== 3) continue
    const size = Number(head[2])
    if (head[1] === 'blob') staged.set(f, out.subarray(at, at + size).toString('utf8'))
    at += size + 1
  }
}
const read = (p: string) => {
  if (!opts.staged) return readFileSync(join(ROOT, p), 'utf8')
  const s = staged.get(p)
  if (s === undefined) throw new Error(`暂存区里没有 ${p}`)
  return s
}

type Level = '错' | '提醒'
const found: { check: string; level: Level; where: string; what: string }[] = []
const bad = (check: string, where: string, what: string, level: Level = '错') => found.push({ check, level, where, what })

// ———— D1 被推翻 / 修正的旧 ADR，标题后要注明新 ADR 的号 ————
const adrText = read('docs/决策记录.md')
const adrs = [...adrText.matchAll(/^## ADR-(\d{3})：(.*)$/gm)].map((m, i, all) => ({
  n: Number(m[1]), title: m[2], line: adrText.slice(0, m.index).split('\n').length,
  body: adrText.slice(m.index! + m[0].length, all[i + 1]?.index ?? adrText.length),
}))
const byN = new Map(adrs.map((a) => [a.n, a]))
for (const a of adrs) {
  // 新 ADR 里写"取代 / 修正 / 推翻 / 改正 / 覆盖 ADR-xxx"（标题和正文都算）
  for (const m of `${a.title}\n${a.body}`.matchAll(/(取代|修正|推翻|改正|覆盖)了?[\s：:]*ADR-(\d{3})/g)) {
    const old = byN.get(Number(m[2]))
    if (!old || old.n >= a.n) continue
    // 标题里的号可能连写："已被 ADR-047、050 修正"
    const ids = [...old.title.matchAll(/ADR-(\d{3}(?:[、，,]\s*\d{3})*)/g)].flatMap((x) => x[1].split(/[、，,]\s*/).map(Number))
    if (!old.title.includes('已被') || !ids.includes(a.n)) bad('D1 旧 ADR 没注明', `决策记录.md:${old.line}`, `ADR-${m[2]} 被 ADR-${String(a.n).padStart(3, '0')} ${m[1]}，标题后要写"已被 ADR-${String(a.n).padStart(3, '0')} ……"`)
  }
}

// ———— D2 ADR 编号连续、日期不晚于今天、不早于前一条 ————
const today = new Date().toLocaleDateString('sv-SE')   // 本地日期 YYYY-MM-DD
let prevDate = ''
const no = (n: number) => `ADR-${String(n).padStart(3, '0')}`
adrs.forEach((a, i) => {
  if (i > 0 && a.n !== adrs[i - 1].n + 1) bad('D2 ADR 编号', `决策记录.md:${a.line}`, `${no(a.n)} 前面是 ${no(adrs[i - 1].n)}，编号不连续`)
  const d = /日期：(\d{4}-\d{2}-\d{2})/.exec(a.body)?.[1]
  if (!d) return
  if (d > today) bad('D2 ADR 日期', `决策记录.md:${a.line}`, `${no(a.n)} 写的是 ${d}，比今天（${today}）晚——看系统日期再写`)
  if (prevDate && d < prevDate) bad('D2 ADR 日期', `决策记录.md:${a.line}`, `${no(a.n)} 的日期 ${d} 比前一条 ${prevDate} 早`, '提醒')
  prevDate = d
})

// ———— D4 文档都进了索引；待办里没有删除线条目 ————
const files = tracked()
const index = read('docs/README.md')
for (const f of files.filter((f) => /^docs\/[^/]+\.(md|html)$/.test(f) && f !== 'docs/README.md')) {
  const name = f.slice('docs/'.length)
  if (!index.includes(`](${name})`)) bad('D4 文档没进索引', f, '在 docs/README.md 的"文件"表里加一行')
}
read('docs/待办.md').split('\n').forEach((l, i) => { if (/^\s*- \[x\]/.test(l)) bad('D4 待办', `待办.md:${i + 1}`, '做完的条目要删掉，不留删除线') })
for (const t of ['会话临时目录']) {
  for (const f of files.filter((f) => f.startsWith('docs/') && f.endsWith('.md') && !f.startsWith('docs/notes/') && !f.startsWith('docs/archive/') && f !== 'docs/决策记录.md')) {
    read(f).split('\n').forEach((l, i) => { if (l.includes(t) && !/找不回|没进仓库|不引用/.test(l)) bad('D10 引用找不回的出处', `${f}:${i + 1}`, `"${t}"会话一关就没了；结论落进 docs/notes/ 再引用`, '提醒') })
  }
}

// ———— D13 旧 ADR 又被新 ADR 修正了：别的文档里写着"已被旧 ADR 改为……"的地方，要回头看还对不对 ————
// 2026-11-07 主人同意（复盘-11-06：LESSONS「后一个提交推翻了前一个提交改的行为……」满 3 天，不往 AGENTS 加字，改成这条检查）。
// 证据：ADR-280 改了六份界面文档开头（"已被 ADR-280 改为照 Grok"），当天 ADR-283 又改回一部分，那六处没人回头改（5c5cbba4 补）。
// 只提醒：文档里有一行说"已被 / 被 ADR-旧 改为、取代、修正"，而整份文档没提到修正它的新 ADR。
{
  const fix = new Map<number, number[]>()   // 旧 ADR → 后来修正它的新 ADR
  for (const a of adrs) for (const m of `${a.title}\n${a.body}`.matchAll(/(取代|修正|推翻|改正|覆盖)了?[\s：:]*ADR-(\d{3})/g)) {
    const old = Number(m[2])
    if (old < a.n && byN.has(old)) fix.set(old, [...new Set([...(fix.get(old) ?? []), a.n])])
  }
  const docs = files.filter((f) => f.endsWith('.md') && (f.startsWith('docs/') || f === 'AGENTS.md') && !f.startsWith('docs/inbox/') && f !== 'docs/决策记录.md')
  for (const f of docs) {
    const text = read(f)
    text.split('\n').forEach((l, i) => {
      for (const m of l.matchAll(/(?:已被|被)\s*\[?ADR-(\d{3})|ADR-(\d{3})\]?(?:\([^)]*\))?\s*(?:改为|取代|修正|推翻)/g)) {
        const old = Number(m[1] ?? m[2])
        const newer = (fix.get(old) ?? []).filter((n) => !text.includes(no(n)))
        if (newer.length) bad('D13 旧 ADR 又被修正', `${f}:${i + 1}`, `这里说被 ${no(old)} 改过，${no(old)} 后来又被 ${newer.map(no).join('、')} 修正；看这句还对不对，对的话在这份文档里提一下新 ADR`, '提醒')
      }
    })
  }
}

// ———— D12 调研笔记要挂到用处上（2026-10-20：调研做完只留在 notes/ 里，做那块时没人翻）————
// docs/notes/ 下的笔记，要能从 notes 以外的文档（规格、待办、ADR、设计语言、参考页、AGENTS、LESSONS）顺着链接走到：
// 被外面直接链到的算挂上了，被挂上了的笔记再链到的也算（拆解笔记的分册由总览链，总览由待办链）；
// 确实不挂的，笔记前 5 行里写一句"不挂：原因"（比如只是一次性的排查记录）。拆解笔记、调研笔记不要用"不挂"躲这条
{
  const notes = files.filter((f) => /^docs\/notes\/[^/]+\.md$/.test(f))
  const mentions = (text: string, f: string) => { const n = f.slice('docs/notes/'.length); return text.includes(n) || text.includes(encodeURI(n)) }
  // 历史层（docs/archive/，2026-11-12 起）里的链接不算挂上：挪进历史的文档已经不是现行用处
  const outside = files.filter((f) => f.endsWith('.md') && !f.startsWith('docs/notes/') && !f.startsWith('docs/inbox/') && !f.startsWith('docs/archive/')).map(read).join('\n')
  const hung = new Set(notes.filter((f) => mentions(outside, f)))
  for (let grew = true; grew;) {
    grew = false
    for (const f of notes) if (!hung.has(f) && [...hung].some((h) => mentions(read(h), f))) { hung.add(f); grew = true }
  }
  for (const f of notes) {
    if (hung.has(f)) continue
    if (read(f).split('\n').slice(0, 5).some((l) => l.includes('不挂：'))) continue
    bad('D12 调研没挂到用处上', f, '在对应的待办条目、规格或 ADR 里链上它（写"参考：……"），或者从已经挂上的笔记链过来；不挂就在笔记前 5 行写"不挂：原因"', '提醒')
  }
}

// ———— D14 规格状态头还写着“待合并 / 在分支上”，头里点名的分支却已合进 main（或已删）————
// 2026-11-13 主人确认改成工具层（证据存档「做完一步，同一天把规格开头的状态……」：11-02、11-03、11-06、11-12 四天复发）。只提醒。
for (const f of files.filter((f) => /^docs\/规格[^/]*\.md$/.test(f))) {
  const branches = staleMergeNote(read(f), (b) => mergedIntoMain(ROOT, b))
  if (branches) bad('D14 规格状态头没跟上合并', `${f}:3`, `状态头还写着待合并 / 在分支上，但 ${branches.join('、')} 已合进 main 或已删；把状态改成“已合 main（提交号）”`, '提醒')
}

// ———— D8 旧名只减不增（号码交给 scripts/scan-private.ts：按本机认识的真实号码拦，2026-11-11 起这里不再猜长数字）————
const OLD_NAMES = ['家里', '数据中心', '出门', '在家', '控制台', 'ctx.language', '/api/language', '心声', '--w-heart', 'su-app-heart', '/api/qq/heart', 'qq-heart', 'language.ts', 'language.reviewBatches', 'memory.confirmOccasions', 'mewtype', 'bilibiliFormat']   // ADR 里"旧名 → 新名"的旧名（copy-twemoji、@twemoji/svg 是 Twemoji 下载脚本的旧名，脚本 2026-11-01 随网页分到 sumika-bun，主仓库不再查；"暂停 → 睡眠模式"不查：视频播放器的暂停是正常用法；"控制台 → 主窗口"是 ADR-081，留下的一处是 Koishi 控制台；ctx.language、/api/language 是 ADR-143 改名前的；心声是 ADR-146 改名前的；memory.confirmOccasions 是 ADR-228 改名前的；mewtype 是 ADR-250 改名前的，bilibiliFormat 是链接解析模板（待占号 ADR）改名前的（旧值 card / cover-text 现在是 templates 里的 @卡片 / @封面文字），规格和决策记录里解释改名的几处有意保留，规格里有意保留的几处是在解释改名）
const scope = files.filter((f) => /\.(md|ts|tsx|css)$/.test(f) && !f.startsWith('docs/notes/') && !f.startsWith('docs/archive/') && !['docs/决策记录.md', 'LESSONS.md', 'scripts/check-docs.ts'].includes(f))   // 这个脚本自己写着旧名表
const names: Record<string, Record<string, number>> = {}
for (const f of scope) {
  const s = read(f)
  for (const w of OLD_NAMES) { const n = s.split(w).length - 1; if (n) (names[w] ??= {})[f] = n }
}
const hasBase = opts.staged ? staged.has(BASELINE) : existsSync(join(ROOT, BASELINE))
const base = hasBase ? JSON.parse(read(BASELINE)) as { names: typeof names } : { names: {} }
if (update) {
  writeFileSync(join(ROOT, BASELINE), JSON.stringify({ names }, null, 2) + '\n')
  console.log(`基线已写到 scripts/docs-baseline.json：旧名 ${Object.values(names).reduce((a, o) => a + Object.values(o).reduce((x, y) => x + y, 0), 0)} 处`)
} else {
  for (const w of OLD_NAMES) for (const [f, n] of Object.entries(names[w] ?? {})) {
    const was = base.names[w]?.[f] ?? 0
    if (n > was) bad('D8 旧名变多了', f, `"${w}" ${was} → ${n} 处（改名见 ADR-061、065；确实有意保留就 --update）`)
  }
}

// ———— D11 记忆里的 [[链接]] 指向的文件要在（记忆目录在本机，不在就跳过）————
const memDir = join(homedir(), '.claude/projects/D--Projects-Code-sumika/memory')
if (existsSync(memDir)) {
  // 直接读目录（以前为了列个目录起 PowerShell，每次 1 秒左右；2026-10-18 选型体检）
  for (const f of readdirSync(memDir).filter((n) => n.endsWith('.md'))) {
    const s = readFileSync(join(memDir, f), 'utf8')
    for (const m of s.matchAll(/\[\[([\w-]+)\]\]/g)) if (!existsSync(join(memDir, `${m[1]}.md`))) bad('D11 记忆链接', `记忆/${f}`, `[[${m[1]}]] 指向的文件不在`, '提醒')
  }
}

// ———— 汇报 ————
if (update) process.exit(0)
const errors = found.filter((x) => x.level === '错')
for (const x of found) console.log(`${x.level === '错' ? '✗' : '·'} [${x.check}] ${x.where}：${x.what}`)
console.log(found.length ? `${errors.length} 个错、${found.length - errors.length} 个提醒` : '文档检查都过了')
process.exit(errors.length ? 1 : 0)
