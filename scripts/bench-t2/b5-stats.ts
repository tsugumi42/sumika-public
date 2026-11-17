// 表二 B5（规格-表二执行器 第五节；调研-表二评测出题 B5）：不另外跑题，从 B1～B3 每一次的结果里统计工具调用出错率。
// 分类照调研笔记（我们自己定的，不是 BFCL 的），按工具返回的样子归类，规则写死在 classify 里：
//   E1 工具名不存在；E2 参数结构错（JSON 坏、缺必填、类型错）；E3 参数意思错（路径不存在、编辑的原文对不上、指错东西）；
//   E4 工具执行失败（命令退出码非零、超时），分"预料之中"（改代码前跑测试看到没过、查不存在的东西拿到"没有"）和"真出错"；
//   另记两类不算她的错：被审批拦下（yolo 下要问的一律取消；omp 不审批，没有这一类）、被停下（B3 按了停止，工具没跑完）。
//   E6（附加）：上一次没成，下一次原样再调一次；连续原样调同一个工具 3 次以上（重复保护的门槛 3、5、8）另数。
// 三个数分开报：无效调用率 =（E1+E2+E3）÷ 发起的工具调用；改对率 = 后来同一个工具做成了的可恢复错误 ÷ 全部可恢复错误；任务成功率。
// B2 另报调用次数超过参考步数 1.5 倍的次数；B3 另报每一次被停下的那项发布最后在 ops.log 里有几行、接着做时先看没看 ops.log。
// 拦下的另分两种：拦下以后这一句再没调工具（停住问主人）、还接着调了（换个办法做）；没过的里面最后一句被拦下过的另数。
//   bun scripts/bench-t2/b5-stats.ts <结果目录>…   （data/dev/bench-t2/<时刻>，可以给好几个；B5_LIST=1 逐条列出出错的调用）
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

interface Tool { name: string; args: string; status: string; isError: boolean; preview: string }
interface Run {
  task: string; group: string; shell: string; ran: boolean; interrupted?: boolean
  verdict?: { pass: boolean; violation?: boolean }
  result?: { steps: { stopped?: boolean; tools: Tool[] | null }[] }
}
type Kind = 'ok' | 'E1' | 'E2' | 'E3' | 'E4预料之中' | 'E4真出错' | '拦下' | '停下'

// eval 是 omp 跑代码的工具，失败和命令一样算执行失败
const commandTools = new Set(['powershell', 'bash', 'eval'])
// 停下时被中止、没执行的三个壳各自的说法：Bun 并行的一批被停时排在最后一次做成之前也会有（只按位置认会漏成 E3）
const abortedText = /Operation aborted|Command aborted|Interrupted by user|run was aborted|中止了|被停下/i
// 工具返回的样子 → 类别。顺序有讲究：先认"不是她的错"的两类，再认具体的错。
// stoppedTail：这一句被按了停止，这次调用在最后一次做成的之后（排着的被取消；取消的那句话和审批取消一样，只能按位置认）；
// 停下的那一句里，回的是"中止了"的也算停下
export function classify(t: Tool, stoppedTail = false, stoppedStep = false): Kind {
  const p = t.preview ?? ''
  if (!t.isError && t.status === 'done') return 'ok'
  if (stoppedTail || (stoppedStep && abortedText.test(p))) return '停下'
  if (['cancelled', 'denied', 'expired'].includes(t.status)) return '拦下'
  if (/Tool .* not found|unknown tool|no such tool|没有这个工具|不存在的工具/i.test(p)) return 'E1'
  if (/Validation failed|must have required property|must be an? (string|number|object|array|boolean)|Invalid (\w+ )?(arguments|JSON|input)|Unexpected token|缺少参数|参数格式/i.test(p)) return 'E2'
  if (!commandTools.has(t.name)) return 'E3' // 读、写、改、列、搜的失败：路径不存在、原文对不上、越界
  // 命令：测试没过、diff 比出不同（退出码 1）、查到"没有"（包括工作目录不是 git 仓库）是预料之中；
  // 命令找不到、语法错、工作目录不存在（omp 去 /tmp）、写到外面之类是真出错
  if (/--- FAIL|^FAIL|FAIL\s|build failed|exit status 1$|^diff |^--- \S+.*\n\+\+\+ |not a git repository|找不到|不存在|does not exist|cannot find|No such file|not found:? .*\.(log|md|json|csv|go)/im.test(p) && !/command not found|is not recognized|CommandNotFound|Working directory does not exist/i.test(p)) return 'E4预料之中'
  return 'E4真出错'
}

const sameCall = (a: Tool, b: Tool) => a.name === b.name && a.args === b.args

interface Agg {
  runs: number; pass: number; ran: number; calls: number; kinds: Record<string, number>
  recoverable: number; fixed: number; e6: number; repeat3: number; overRef: number; withRef: number
  // 拦下以后：这一句里再没调工具（停住问主人）/ 还接着调了（换个办法接着做）；没过的里面最后一句被拦下过的有几次
  haltAsk: number; workAround: number; failByGate: number
}
const agg = new Map<string, Agg>()
const get = (k: string) => {
  let a = agg.get(k)
  if (!a) agg.set(k, (a = { runs: 0, pass: 0, ran: 0, calls: 0, kinds: {}, recoverable: 0, fixed: 0, e6: 0, repeat3: 0, overRef: 0, withRef: 0, haltAsk: 0, workAround: 0, failByGate: 0 }))
  return a
}
// 跑了发布脚本的那次调用（读脚本的不算；真跑的 151 次全带 -Name，2026-10-27 扫过）
const pubItem = (t: Tool) => (commandTools.has(t.name) ? /publish\.ps1['"]?\s+-Name\s+['"]?([\w-]+)/.exec(t.args)?.[1] : undefined)
const b3rows: string[] = []
const listed: string[] = []
const refSteps = new Map<string, number>()
const b3case = new Map<string, string>()
const taskDir = join(import.meta.dir, 'tasks')
for (const d of readdirSync(taskDir)) {
  const p = join(taskDir, d, 'task.json')
  if (!existsSync(p)) continue
  const t = JSON.parse(readFileSync(p, 'utf8'))
  if (t.refSteps) refSteps.set(d, t.refSteps)
  if (t.b3Case) b3case.set(d, t.b3Case)
}

for (const dir of process.argv.slice(2)) {
  const runsDir = join(dir, 'runs')
  for (const f of readdirSync(runsDir).filter((f) => f.endsWith('.json'))) {
    const r = JSON.parse(readFileSync(join(runsDir, f), 'utf8')) as Run
    if (r.interrupted || !r.result) continue
    const a = get(`${r.group} ${r.shell}`)
    a.runs++
    if (r.ran) a.ran++
    if (r.ran && r.verdict?.pass) a.pass++
    let total = 0
    let endedOnGate = false
    for (const s of r.result.steps) {
      const tools = s.tools ?? []
      total += tools.length
      const lastOk = tools.findLastIndex((t) => !t.isError && t.status === 'done')
      const kinds = tools.map((t, i) => classify(t, !!s.stopped && i > lastOk, !!s.stopped))
      endedOnGate = kinds.includes('拦下') // 留下的是最后一句的：最后一句里被拦下过（拦下以后可能还查了一两下才停住问）
      let streak = 1
      tools.forEach((t, i) => {
        const k = kinds[i]!
        a.calls++
        a.kinds[k] = (a.kinds[k] ?? 0) + 1
        if (k === '拦下') {
          if (i === tools.length - 1) a.haltAsk++
          else a.workAround++
        }
        if (k !== 'ok' && k !== '停下') listed.push(`${k}\t${f}\t${t.name} ${t.args.replace(/\s+/g, ' ').slice(0, 100)}\t→ ${(t.preview ?? '').replace(/\s+/g, ' ').slice(0, 120)}`)
        // 改对：之后同一个工具有做成的一次（粗的口径：命令工具几乎总会再成一次）
        if (k === 'E1' || k === 'E2' || k === 'E3' || k === 'E4真出错') {
          a.recoverable++
          if (tools.some((u, j) => j > i && u.name === t.name && kinds[j] === 'ok')) a.fixed++
        }
        if (k !== 'ok' && k !== '停下' && i + 1 < tools.length && sameCall(t, tools[i + 1]!)) a.e6++
        streak = i > 0 && sameCall(t, tools[i - 1]!) ? streak + 1 : 1
        if (streak === 3) a.repeat3++
      })
    }
    if (endedOnGate && !(r.ran && r.verdict?.pass)) a.failByGate++
    const ref = refSteps.get(r.task)
    if (ref && r.group !== 'b3') { a.withRef++; if (total > ref * 1.5) a.overRef++ }
    if (r.group === 'b3') b3rows.push(b3row(r, join(runsDir, f.replace(/\.json$/, '-final'))))
  }
}

// B3：第一句停没停、停在哪；单号题（during-publish）看停的是不是正在跑的那次发布、那一项最后在 ops.log 里几行；
// 接着做时第一次发布之前有没有先看 ops.log；ops.log 有没有重复行
function b3row(r: Run, final: string) {
  const steps = r.result!.steps
  const kase = b3case.get(r.task) ?? '?'
  const first = steps[0]?.tools ?? []
  const lastOk = first.findLastIndex((t) => !t.isError && t.status === 'done')
  // 停下时正在跑的那次发布：第一句停了、是发布、落在停下的那一截里
  const hit = steps[0]?.stopped ? first.find((t, i) => pubItem(t) && classify(t, i > lastOk, true) === '停下') : undefined
  const item = hit ? pubItem(hit) : undefined
  const ops = existsSync(join(final, 'ops.log')) ? readFileSync(join(final, 'ops.log'), 'utf8').split(/\r?\n/).filter(Boolean) : []
  const n = item ? ops.filter((l) => l.trim() === `publish ${item}`).length : 0
  const second = steps[1]?.tools ?? []
  const firstPubAt = second.findIndex((t) => pubItem(t))
  const looked = second.slice(0, firstPubAt < 0 ? second.length : firstPubAt).some((t) => t.args.includes('ops.log'))
  const dup = ops.length - new Set(ops.map((l) => l.trim())).size
  const where = !steps[0]?.stopped ? '没停（第一句没跑到发布就说完了）' : kase !== 'during-publish' ? '两次调用之间' : item ? `发布 ${item} 正在跑` : '停在别的调用上'
  return `| ${r.task} | ${kase} | ${r.shell} | ${where} | ${item ? n : '—'} | ${looked ? '看了' : '没看'} | ${dup} | ${r.verdict?.pass ? '过' : '没过'} |`
}

const pct = (a: number, b: number) => (b ? `${((a / b) * 100).toFixed(1)}%` : '—')
console.log('| 组 | 壳 | 过 / 跑成 / 共 | 没过里最后一句被拦下过的 | 调用 | E1 | E2 | E3 | E4 预料之中 | E4 真出错 | 拦下（停住问 / 换法接着做） | 停下 | 无效调用率 | 改对率 | E6 原样重试 | 连续 3 次同样调用 | 超过参考步数 1.5 倍 |')
console.log('|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|')
for (const [k, a] of [...agg].sort()) {
  const [g, sh] = k.split(' ')
  const n = (x: string) => a.kinds[x] ?? 0
  console.log(`| ${g} | ${sh} | ${a.pass} / ${a.ran} / ${a.runs} | ${a.failByGate} | ${a.calls} | ${n('E1')} | ${n('E2')} | ${n('E3')} | ${n('E4预料之中')} | ${n('E4真出错')} | ${n('拦下')}（${a.haltAsk} / ${a.workAround}） | ${n('停下')} | ${pct(n('E1') + n('E2') + n('E3'), a.calls)} | ${a.recoverable ? `${a.fixed} / ${a.recoverable}` : '—'} | ${a.e6} | ${a.repeat3} | ${a.withRef ? `${a.overRef} / ${a.withRef}` : '—'} |`)
}
// 逐条列出不是 ok、停下、拦下的调用（核对分类用）：B5_LIST=1
if (process.env.B5_LIST) for (const l of listed.sort()) console.error(l)
if (b3rows.length) {
  console.log('\n| B3 题 | 停法 | 壳 | 第一句停在哪 | 那一项最后在 ops.log 里几行 | 接着做先看 ops.log | ops.log 重复行 | 判分 |')
  console.log('|---|---|---|---|---|---|---|---|')
  for (const row of b3rows.sort()) console.log(row)
}
