// 表二 B4 的题目导入（规格-表二执行器 第五、六节；调研-表二评测出题 B4）：从 LongMemEval（cleaned 版 oracle，MIT）裁 10 问，
// 每问拼成一段长历史存进她的库（Task.seed），压缩一次再问（-c），另出一道不压缩直接问的对照（-n）。
// 挑法写死在下面：明确的事实 3、后来改了主意 2、要把几轮合起来 2、历史里没有答案 3（按 _abs 挑）；只挑答案短而唯一的，
// 每问写几种说法（她可能用中文答），归一化以后包含匹配。证据会话在前，后面接两段别的题的会话当无关的历史（不含这一问的答案），
// 压缩只留最近 1000 token 原文（Bun 最小就是 1000）——证据一定落在被压缩的那一段。裁过的只能叫 Sumika-B4，不是 LongMemEval 的官方分。
// 建实例读最近 200 条（recentMessages）：历史有三四十条，照默认的 30 条最前面的证据两组都看不到（2026-10-27 第一轮就是这样）。
// "改了主意"的两问另写被改掉的旧值（notAnswer），回复里说到它判"要人看"。
//   bun scripts/bench-t2/import-longmemeval.ts <longmemeval_oracle.json>
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

interface Turn { role: 'user' | 'assistant'; content: string }
interface Question {
  question_id: string; question_type: string; question: string; answer: unknown
  haystack_dates: string[]; haystack_sessions: Turn[][]
}

const PICKS: { id: string; kind: string; answer?: string[]; notAnswer?: string[] }[] = [
  { id: '1e043500', kind: '事实', answer: ['Summer Vibes'] },
  { id: '4c36ccef', kind: '事实', answer: ['Roscioli'] },
  { id: '51a45a95', kind: '事实', answer: ['Target'] },
  { id: '852ce960', kind: '改了主意', answer: ['400000', '40万', '四十万'], notAnswer: ['350000', '35万', '三十五万'] },
  { id: '0e4e4c46', kind: '改了主意', answer: ['132'], notAnswer: ['124'] },
  { id: '2311e44b', kind: '合起来', answer: ['190'] },
  { id: '9aaed6a3', kind: '合起来', answer: ['0.75', '75美分', '七毛五'] },
  { id: '0862e8bf_abs', kind: '没有答案' },
  { id: '2311e44b_abs', kind: '没有答案' },
  { id: 'f685340e_abs', kind: '没有答案' },
]

const [oracleFile] = process.argv.slice(2)
if (!oracleFile) throw new Error('要给 longmemeval_oracle.json 的路径')
const raw = readFileSync(oracleFile)
const sha = createHash('sha256').update(raw).digest('hex')
const all = JSON.parse(raw.toString('utf8')) as Question[]
const byId = new Map(all.map((q) => [q.question_id, q]))
const picked = new Set(PICKS.map((p) => p.id.replace(/_abs$/, '')))
// 无关的历史：文件里排在前面的单会话题（不在挑中的这几道里），按先后轮着用
const pool = all.filter((q) => q.question_type === 'single-session-user' && !q.question_id.endsWith('_abs') && !picked.has(q.question_id)).slice(0, 12)

// "2023/05/20 (Sat) 02:21" → 毫秒
const when = (s: string) => {
  const m = s.match(/(\d+)\/(\d+)\/(\d+)\s*\(\w+\)\s*(\d+):(\d+)/)
  if (!m) throw new Error(`读不懂的日期 ${s}`)
  return Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5])
}
const lines = (sessions: Turn[][], starts: number[]) =>
  sessions.flatMap((s, i) => s.map((t, j) => ({ who: t.role === 'user' ? 'owner' : 'character', text: t.content, at: starts[i] + j * 60_000 })))

const tasksDir = join(import.meta.dir, 'tasks')
PICKS.forEach((p, i) => {
  const q = byId.get(p.id)
  if (!q) throw new Error(`oracle 里没有 ${p.id}`)
  const abstain = p.id.endsWith('_abs')
  const starts = q.haystack_dates.map(when)
  const last = Math.max(...starts)
  // 两段无关的会话：答案的任何一种说法都不能在里面（不然"没压缩丢掉"和"从别处看到"分不清）
  const needles = (p.answer ?? []).map((a) => a.toLowerCase())
  const extra: Turn[][] = []
  for (let k = 0; extra.length < 2 && k < pool.length; k++) {
    const s = pool[(i * 2 + k) % pool.length].haystack_sessions[0]
    const text = s.map((t) => t.content).join('\n').toLowerCase()
    if (!needles.some((n) => text.includes(n))) extra.push(s)
  }
  if (extra.length < 2) throw new Error(`${p.id}：找不到两段不含答案的无关会话`)
  const seed = [...lines(q.haystack_sessions, starts), ...lines(extra, [last + 86_400_000, last + 2 * 86_400_000])]
  const evidenceChars = q.haystack_sessions.flat().reduce((n, t) => n + t.content.length, 0)
  const extraChars = extra.flat().reduce((n, t) => n + t.content.length, 0)
  const expect = abstain ? { abstain: true } : { answer: p.answer, ...(p.notAnswer ? { notAnswer: p.notAnswer } : {}) }
  const no = String(i + 1).padStart(2, '0')
  for (const [suffix, steps] of [
    ['c', [{ compact: true }, { say: q.question, expect }]],
    ['n', [{ say: q.question, expect }]],
  ] as const) {
    const id = `b4-${no}-${suffix}`
    const dir = join(tasksDir, id)
    rmSync(dir, { recursive: true, force: true })
    mkdirSync(dir, { recursive: true })
    const task = {
      id, group: 'b4', source: `LongMemEval cleaned（MIT）oracle sha256 ${sha.slice(0, 16)} ${q.question_id}（${p.kind}${suffix === 'c' ? '，压缩以后问' : '，不压缩的对照'}）`,
      keepTokens: 1000, recentMessages: 200, seed, steps, judge: {},
    }
    writeFileSync(join(dir, 'task.json'), JSON.stringify(task, null, 2) + '\n')
  }
  console.log(`b4-${no}  ${q.question_id.padEnd(14)} ${p.kind.padEnd(4)} 证据 ${q.haystack_sessions.length} 段 ${evidenceChars} 字 + 无关 ${extraChars} 字（约 ${Math.round((evidenceChars + extraChars) / 4)} token）  ${q.question.slice(0, 60)}`)
})
