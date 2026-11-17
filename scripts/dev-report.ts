// 开发周报（主人 2026-11-13 要）：每周一出一份，看这周比上周快了还是慢了、哪里在退步。只读：读 git、合并队列的输出、
// sumika-ctl 的统计、自检文件、待办和磁盘占用，写 data/reports/开发周报-<日期>.md（主工作区的 data/，worktree 里跑也写到那边）。
//   bun scripts/dev-report.ts [--days 7] [--out <文件>]
// 每一节自己取数，取不到写“取不到：原因”，不写 0；不碰真实数据库（统计走 sumika-ctl 的只读命令）。
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'

const args = process.argv.slice(2)
const opt = (k: string, d: string) => { const i = args.indexOf(k); return i >= 0 && args[i + 1] ? args[i + 1]! : d }
const DAYS = Number(opt('--days', '7'))
const run = (cmd: string[], cwd?: string) => {
  const p = Bun.spawnSync(cmd, { cwd, env: { ...process.env, GIT_DIR: undefined, GIT_INDEX_FILE: undefined, GIT_WORK_TREE: undefined } })
  return { ok: p.exitCode === 0, out: p.stdout.toString(), err: p.stderr.toString() }
}
const git = (a: string[]) => run(['git', '-c', 'core.quotepath=false', ...a], ROOT).out.trim()

// 主工作区根：git 公共目录的上一层（worktree 里跑也算主工作区，报告和队列输出都在那边）
const here = resolve(import.meta.dir, '..')
const common = run(['git', 'rev-parse', '--path-format=absolute', '--git-common-dir'], here).out.trim()
const ROOT = common ? dirname(common) : here
const DATA = join(ROOT, 'data')

const now = new Date()
const DAY = 86400000
const cur = { from: new Date(now.getTime() - DAYS * DAY), to: now }
const prev = { from: new Date(now.getTime() - 2 * DAYS * DAY), to: cur.from }
type Win = typeof cur
const iso = (d: Date) => d.toISOString()
const ymd = (d: Date) => d.toLocaleDateString('sv-SE')

const lines: string[] = []
const out = (s = '') => lines.push(s)
const arrow = (a: number, b: number, higherIsBetter: boolean) => {
  if (!Number.isFinite(a) || !Number.isFinite(b)) return '上周没有数据'
  if (a === b) return '持平'
  const up = a > b
  return (up === higherIsBetter ? '变好' : '变坏') + (up ? ' ↑' : ' ↓')
}
const pct = (x: number) => (Number.isFinite(x) ? `${(x * 100).toFixed(0)}%` : '—')
const median = (xs: number[]) => { if (!xs.length) return NaN; const s = [...xs].sort((a, b) => a - b); return s[Math.floor(s.length / 2)]! }
const section = (title: string, fn: () => void) => {
  out(`## ${title}`); out()
  try { fn() } catch (e) { out(`取不到：${(e as Error).message}`) }
  out()
}

// ---------- 产出 ----------
function gitCount(w: Win, merges: boolean) {
  return Number(git(['rev-list', '--count', merges ? '--merges' : '--no-merges', `--since=${iso(w.from)}`, `--until=${iso(w.to)}`, 'main']) || 'NaN')
}
// 这段时间第一次出现的 R 号：只看新增行会把挪动、改写的旧条目也算进去（11-12 整理过一次），所以和这段时间开始前的需求登记比
function reqAdded(w: Win) {
  const ids = (t: string) => new Set([...t.matchAll(/^### (R-\d+)/gm)].map((m) => m[1]!))
  const at = (d: Date) => { const rev = git(['rev-list', '-1', `--before=${iso(d)}`, 'main']); return rev ? ids(git(['show', `${rev}:docs/需求登记.md`])) : new Set<string>() }
  const before = at(w.from), after = at(w.to)
  if (!before.size) return NaN   // 这段时间开始时还没有需求登记（11-12 才建），没法分出哪些是新的
  return [...after].filter((r) => !before.has(r)).length
}

// ---------- 合并队列 ----------
type QRun = { at: Date; ok: boolean; sec: number; full: boolean; fails: string[]; slow: [string, number][] }
function queueRuns(): QRun[] {
  const dir = join(DATA, 'dev', 'test-runs')
  if (!existsSync(dir)) throw new Error(`没有 ${dir}`)
  const runs: QRun[] = []
  for (const f of readdirSync(dir)) {
    const m = /^queue-.+-(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})\.txt$/.exec(f)
    if (!m) continue
    const at = new Date(+m[1]!, +m[2]! - 1, +m[3]!, +m[4]!, +m[5]!, +m[6]!)
    if (at < prev.from) continue
    const t = readFileSync(join(dir, f), 'utf8')
    const end = /退出码 (\d+)，([\d.]+) 秒\s*$/.exec(t.trimEnd())
    runs.push({
      at, ok: end?.[1] === '0', sec: end ? Number(end[2]) : NaN, full: /^Go 验证：.*整套/m.test(t),
      fails: [...t.matchAll(/^\s*--- FAIL: (\S+)/gm)].map((x) => x[1]!.split('/')[0]!),
      slow: [...t.matchAll(/^ok\s+(\S+)\s+([\d.]+)s/gm)].map((x) => [x[1]!.replace('sumika-native/', '').replace(/^sumika-native$/, '（根包）'), Number(x[2])] as [string, number]).filter(([, s]) => s > 60),
    })
  }
  return runs
}
const inWin = (r: QRun, w: Win) => r.at >= w.from && r.at < w.to

// ---------- 测试清单 ----------
function testInventory() {
  let tests = 0, fuzz = 0, files = 0
  const walk = (d: string) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      if (e.name === 'vendor' || e.name === 'node_modules' || e.name.startsWith('.')) continue
      const p = join(d, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name.endsWith('_test.go')) {
        files++
        const t = readFileSync(p, 'utf8')
        tests += (t.match(/^func Test\w*\(t \*testing\.T\)/gm) ?? []).length
        fuzz += (t.match(/^func Fuzz\w*\(f \*testing\.F\)/gm) ?? []).length
      }
    }
  }
  walk(join(ROOT, 'native'))
  return { tests, fuzz, files }
}

// ---------- 运行健康 ----------
function ctl(a: string[]) {
  const exe = join(ROOT, 'native', process.platform === 'win32' ? 'sumika-ctl.exe' : 'sumika-ctl')
  if (!existsSync(exe)) throw new Error('没有 native/sumika-ctl.exe')
  const r = run([exe, '--json', ...a], ROOT)
  if (!r.ok) throw new Error((r.err || r.out).trim().slice(0, 200) || 'sumika-ctl 出错')
  const j = JSON.parse(r.out)
  if (!j.ok) throw new Error(String(j.error ?? 'sumika-ctl 返回失败'))
  return j.data
}
function selfcheckBad(w: Win) {
  const dir = join(DATA, 'selfcheck')
  if (!existsSync(dir)) throw new Error('没有 data/selfcheck')
  const bad: Record<string, number> = {}
  for (const f of readdirSync(dir)) {
    const m = /^(\d{4}-\d{2}-\d{2})\.json$/.exec(f)
    if (!m) continue
    const d = new Date(m[1] + 'T12:00:00')
    if (d < w.from || d >= w.to) continue
    for (const c of JSON.parse(readFileSync(join(dir, f), 'utf8')).checks ?? []) if (c.state === 'bad') bad[c.id] = (bad[c.id] ?? 0) + 1
  }
  return bad
}

// ---------- 待办 ----------
function todoAt(d: Date) {
  const rev = git(['rev-list', '-1', `--before=${iso(d)}`, 'main'])
  if (!rev) return NaN
  const t = git(['show', `${rev}:docs/待办.md`])
  return (t.match(/^\s*- \[ \]/gm) ?? []).length
}

// ---------- 磁盘 ----------
function folderGB(p: string) {
  const es = 'D:\\Programs\\Everything\\es.exe'
  if (!existsSync(es)) throw new Error('没有 Everything 的 es.exe')
  const r = run([es, '-get-folder-size', p])
  const n = Number(r.out.trim())
  return Number.isFinite(n) && n > 0 ? n / 1e9 : NaN
}

// ================= 写报告 =================
out(`# 开发周报 ${ymd(now)}`)
out()
out(`范围：${ymd(cur.from)} 到 ${ymd(cur.to)}（最近 ${DAYS} 天），和再往前 ${DAYS} 天比。数字取自 git、合并队列输出、sumika-ctl 统计、自检文件；取不到的写原因。`)
out()

section('产出', () => {
  const c = [gitCount(cur, false), gitCount(prev, false)], m = [gitCount(cur, true), gitCount(prev, true)], r = [reqAdded(cur), reqAdded(prev)]
  out('| | 本周 | 上周 | |'); out('|---|---|---|---|')
  out(`| 提交（不含合并） | ${c[0]} | ${c[1]} | |`)
  out(`| 合进 main | ${m[0]} | ${m[1]} | |`)
  const rs = (x: number) => (Number.isFinite(x) ? String(x) : '—（那时还没有需求登记）')
  out(`| 新登记的需求（R-xxx） | ${rs(r[0]!)} | ${rs(r[1]!)} | |`)
})

let runs: QRun[] = []
section('合并队列', () => {
  runs = queueRuns()
  const stat = (w: Win) => {
    const rs = runs.filter((r) => inWin(r, w))
    const ok = rs.filter((r) => r.ok).length
    const secs = rs.map((r) => r.sec).filter(Number.isFinite)
    return { n: rs.length, okRate: rs.length ? ok / rs.length : NaN, med: median(secs) / 60, max: secs.length ? Math.max(...secs) / 60 : NaN, full: rs.filter((r) => r.full).length }
  }
  const a = stat(cur), b = stat(prev)
  out('| | 本周 | 上周 | |'); out('|---|---|---|---|')
  out(`| 跑了几次 | ${a.n} | ${b.n || '—（队列 11-08 才开始用）'} | |`)
  out(`| 一次就过的比例 | ${pct(a.okRate)} | ${pct(b.okRate)} | ${arrow(a.okRate, b.okRate, true)} |`)
  out(`| 用时中位数（分钟） | ${a.med.toFixed(1)} | ${b.med.toFixed(1)} | ${arrow(a.med, b.med, false)} |`)
  out(`| 最长一次（分钟） | ${a.max.toFixed(1)} | ${b.max.toFixed(1)} | ${arrow(a.max, b.max, false)} |`)
  out(`| 全跑整套的次数 | ${a.full} | ${b.full} | ${arrow(a.full, b.full, false)} |`)
  const fails: Record<string, number> = {}
  for (const r of runs.filter((r) => inWin(r, cur) && !r.ok)) for (const f of new Set(r.fails)) fails[f] = (fails[f] ?? 0) + 1
  const top = Object.entries(fails).sort((x, y) => y[1] - x[1]).slice(0, 10)
  out(); out('本周红灯里出现最多的测试（同一个测试红过几次；反复出现、又没有对应修复的多半是偶发红）：'); out()
  out(top.length ? top.map(([k, v]) => `- ${k}：${v} 次`).join('\n') : '- 没有')
})

section('测试', () => {
  const t = testInventory()
  out(`- 测试函数 ${t.tests} 个（${t.files} 个测试文件），模糊测试目标 ${t.fuzz} 个（只数当前代码，不和上周比）。`)
  const slow: Record<string, { n: number; max: number }> = {}
  for (const r of runs.filter((r) => inWin(r, cur))) for (const [p, s] of r.slow) { const x = (slow[p] ??= { n: 0, max: 0 }); x.n++; x.max = Math.max(x.max, s) }
  const top = Object.entries(slow).sort((a, b) => b[1].n - a[1].n).slice(0, 10)
  out('- 本周单包超过 60 秒最多的包（次数、最长）：'); out()
  out(top.length ? top.map(([p, x]) => `  - ${p}：${x.n} 次，最长 ${x.max.toFixed(0)} 秒`).join('\n') : '  - 没有')
})

section('运行健康', () => {
  const rows: string[] = []
  try {
    const u = ctl(['stats', 'usage', '--days', String(2 * DAYS), '--by', 'week'])
    const ws = (u.rows ?? u.weeks ?? u.buckets ?? []) as Array<Record<string, any>>
    if (ws.length) for (const w of ws.slice(-2)) rows.push(`- ${w.date ?? w.label ?? w.key} 起的一周：${w.runs} 次模型调用，失败 ${w.failed}，缓存命中 ${pct(w.cacheHit)}（目标 95%），输入 ${w.input}、输出 ${w.output} token`)
    else { const t = ctl(['stats', 'usage', '--days', String(DAYS)]).totals; rows.push(`- 最近 ${DAYS} 天：${t.runs} 次模型调用，失败 ${t.failed}，缓存命中 ${pct(t.cacheHit)}（目标 95%）`) }
  } catch (e) { rows.push(`- 用量：取不到：${(e as Error).message}`) }
  try {
    const bad = selfcheckBad(cur)
    const ks = Object.entries(bad)
    rows.push(`- 自检异常：${ks.length ? ks.map(([k, v]) => `${k} ${v} 天`).join('，') : '没有'}`)
  } catch (e) { rows.push(`- 自检：取不到：${(e as Error).message}`) }
  rows.push('- 崩溃和被意外结束、看门狗拉起：未接入（看门狗事故记录的位置还没接进周报）')
  out(rows.join('\n'))
})

section('待办', () => {
  const a = todoAt(now), b = todoAt(cur.from)
  const t = git(['show', 'main:docs/待办.md'])
  const wait = (t.match(/【等主人定】|等主人定/g) ?? []).length
  out(`- 还挂着 ${a} 条（${DAYS} 天前 ${b} 条，${arrow(a, b, false)}）；条目里写着“等主人定”的约 ${wait} 处。`)
})

section('磁盘', () => {
  const parts = [['Go 编译缓存', 'D:\\Cache\\go-build'], ['数据目录', join(DATA)], ['换 exe 的备份', join(DATA, 'backup')]] as const
  out(parts.map(([n, p]) => { try { const g = folderGB(p); return `- ${n}：${Number.isFinite(g) ? g.toFixed(1) + ' GB' : '取不到'}` } catch (e) { return `- ${n}：取不到：${(e as Error).message}` } }).join('\n'))
})

out('## 给主会话'); out()
out('没有')
out()

const file = opt('--out', join(DATA, 'reports', `开发周报-${ymd(now)}.md`))
mkdirSync(dirname(file), { recursive: true })
writeFileSync(file, lines.join('\n'))
console.log(file)
