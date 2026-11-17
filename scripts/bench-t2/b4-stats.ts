// 表二 B4（规格-表二执行器 第五节；规格-Go后台 8.2 B4）：按 壳 × 压缩 / 不压缩 数每问的结果，压缩那组另看摘要里还有没有答案。
// 每次的类别取执行器判的（bench/bank.go Classify：答对、漏、答错、正确地说不知道、编、要人看）；结果目录里有 labels.json 的，
// 照它把"要人看"和判错的几条改成人定的类别（{"b4-03-n-go-1": {"answer": "missed", "why": "…"}}），两套数都报。
// 没跑成的（网关断流这类）不进分母，单列。
//   bun scripts/bench-t2/b4-stats.ts <结果目录>…    （B4_LIST=1 列出每一条要人看的回复，定类用）
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

interface Step { reply?: string; compact?: { ok: boolean; summary?: string } }
interface Run { task: string; group: string; shell: string; rep: number; ran: boolean; verdict?: { answers?: string[] }; result?: { steps: Step[] } }
interface Expect { answer?: string[]; abstain?: boolean; notAnswer?: string[] }

const KINDS = ['correct', 'missed', 'wrong', 'abstained', 'fabricated', 'ambiguous'] as const
const NAMES: Record<string, string> = { correct: '答对', missed: '漏', wrong: '答错', abstained: '正确地说不知道', fabricated: '编', ambiguous: '要人看' }
const MARK: Record<string, string> = { correct: '对', missed: '漏', wrong: '错', abstained: '知', fabricated: '编', ambiguous: '?', notRun: '-' }
// 和 bank.go 的 answerNorm 一样：小写，去掉空白和常见标点
const norm = (s: string) => s.toLowerCase().replace(/[\s,.!?;:'"`，。！？；：、“”‘’（）()「」《》—\-_*~]/gu, '')

const taskDir = join(import.meta.dir, 'tasks')
const expectOf = new Map<string, Expect>()
for (const d of readdirSync(taskDir).filter((d) => d.startsWith('b4-'))) {
  const t = JSON.parse(readFileSync(join(taskDir, d, 'task.json'), 'utf8'))
  expectOf.set(d, t.steps.find((s: { expect?: Expect }) => s.expect)?.expect ?? {})
}

const rows: { key: string; q: string; variant: string; shell: string; rep: number; auto: string; hand: string; why: string; summaryHas?: boolean; reply: string }[] = []
for (const dir of process.argv.slice(2)) {
  const labels: Record<string, { answer: string; why?: string }> = existsSync(join(dir, 'labels.json')) ? JSON.parse(readFileSync(join(dir, 'labels.json'), 'utf8')) : {}
  for (const f of readdirSync(join(dir, 'runs')).filter((f) => /^b4-.*\.json$/.test(f))) {
    const r = JSON.parse(readFileSync(join(dir, 'runs', f), 'utf8')) as Run
    const [, q, variant] = /^b4-(\d+)-([cn])$/.exec(r.task) ?? []
    if (!q) continue
    const key = f.replace(/\.json$/, '')
    const auto = r.ran ? (r.verdict?.answers?.[0] ?? 'notRun') : 'notRun'
    const e = expectOf.get(r.task) ?? {}
    const steps = r.result?.steps ?? []
    const summary = steps.find((s) => s.compact)?.compact?.summary
    const summaryHas = variant === 'c' && !e.abstain && summary !== undefined ? (e.answer ?? []).some((a) => norm(summary).includes(norm(a))) : undefined
    const l = labels[key]
    rows.push({ key, q, variant, shell: r.shell, rep: r.rep, auto, hand: l?.answer ?? auto, why: l?.why ?? '', summaryHas, reply: steps.at(-1)?.reply ?? '' })
  }
}

const good = (k: string) => k === 'correct' || k === 'abstained'
function table(pick: (r: (typeof rows)[number]) => string, title: string) {
  console.log(`\n${title}\n`)
  console.log(`| 壳 | 组 | 对 / 跑成 | ${KINDS.map((k) => NAMES[k]).join(' | ')} | 没跑成 |`)
  console.log(`|---|---|---|${KINDS.map(() => '---').join('|')}|---|`)
  for (const shell of [...new Set(rows.map((r) => r.shell))].sort()) {
    for (const variant of ['c', 'n']) {
      const rs = rows.filter((r) => r.shell === shell && r.variant === variant)
      const ran = rs.filter((r) => pick(r) !== 'notRun')
      const n = (k: string) => ran.filter((r) => pick(r) === k).length
      console.log(`| ${shell} | ${variant === 'c' ? '压缩以后问' : '不压缩'} | ${ran.filter((r) => good(pick(r))).length} / ${ran.length} | ${KINDS.map(n).join(' | ')} | ${rs.length - ran.length} |`)
    }
  }
}
table((r) => r.auto, '字面判（执行器的 Classify）')
if (rows.some((r) => r.hand !== r.auto)) table((r) => r.hand, `人定以后（labels.json 改了 ${rows.filter((r) => r.hand !== r.auto).length} 条）`)

// 每问：各次的结果排成一串（人定以后的），压缩那组另记摘要里有答案的次数
console.log('\n| 问 | 种类 | go 压缩 | go 不压缩 | bun 压缩 | bun 不压缩 | 摘要里有答案（go / bun） |')
console.log('|---|---|---|---|---|---|---|')
for (const q of [...new Set(rows.map((r) => r.q))].sort()) {
  const seq = (shell: string, v: string) => rows.filter((r) => r.q === q && r.shell === shell && r.variant === v).sort((a, b) => a.rep - b.rep).map((r) => MARK[r.hand] ?? r.hand).join('')
  const has = (shell: string) => {
    const rs = rows.filter((r) => r.q === q && r.shell === shell && r.variant === 'c' && r.summaryHas !== undefined)
    return rs.length ? `${rs.filter((r) => r.summaryHas).length}/${rs.length}` : '—'
  }
  const e = expectOf.get(`b4-${q}-n`) ?? {}
  console.log(`| ${q} | ${e.abstain ? '没有答案' : e.notAnswer ? '改了主意' : '有答案'} | ${seq('go', 'c')} | ${seq('go', 'n')} | ${seq('bun', 'c')} | ${seq('bun', 'n')} | ${has('go')} / ${has('bun')} |`)
}
console.log(`\n（${Object.entries(MARK).map(([k, v]) => `${v} ${NAMES[k] ?? '没跑成'}`).join('，')}）`)

if (process.env.B4_LIST)
  for (const r of rows.filter((r) => r.auto === 'ambiguous' || r.hand !== r.auto).sort((a, b) => a.key.localeCompare(b.key)))
    console.error(`${r.key}\t字面 ${r.auto}\t人定 ${r.hand}\t${r.why}\n    ${r.reply.replace(/\s+/g, ' ').slice(0, 300)}`)
