// 合并前跑受影响的 Go 包，过了一个记一个："这份代码上哪些包验过了"（ADR-223，规格-提交前检查 第二节）。推送钩子见到
// 同一份代码、同一套 go，就不再跑这些包。
//   bun scripts/go-verify.ts [基准，默认 main]    在要合进 main 的分支上、提交完以后跑（分支先合上最新的 main，合并后的代码才和测过的一样）
//   bun scripts/go-verify.ts --full              整套
//   bun scripts/go-verify.ts --full --by nightly 每日整套：记录里写 by=nightly，和手动、队列的短跑分开认
// 已经验过的包跳过：红了几个，改好（或机器闲下来）再跑一遍，只跑没过的那几个。
// 一律 -count=1：受影响的包测试程序必然变了，go 的测试缓存命中不了；开着缓存跑完还要按测试日志逐行重新查路径、算哈希，
// 这台机器上历史导入工具光查缓存就 558 秒，比真跑一遍（277 秒）还久（2026-11-01 实测，工具现已退役）。同一份代码不重复跑，靠这份记录。
import { existsSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describePick, pick, readGraph } from './go-affected'
import { addStepRecord, cleanTree, FULL_PLAN, loadStepRecords, needsRunPlanned, PLANNED_PATHS, plan, runPlanned, stepScope } from './server-verify'

// tree 是"Go 会读到的那几处"的树（见 goTree），不是整个提交的树
// short：这次是 go test -short 跑的（慢的对拍、压力、规模测试跳过或少跑，ADR-225）；整套（--full、SUMIKA_PUSH_FULL=1）不认这种记录
export type Verified = { tree: string; commit: string; pkgs: string[]; go: string; at: string; seconds: number; by: string; short?: boolean }

// Go 的测试会读到的地方，和 go-affected.ts 里"改了就整套跑"的那批对应（那边认定改别处不影响任何 Go 测试，这边同一个口径）：
// 记录只认这几处的内容，验完以后改文档不用重跑；这几处有没提交的改动，测出来的就不是这份代码
// 后半是执行测试的脚本：挑哪些包、带什么参数、怎么分进程都由它们定，改了它们，旧的绿记录就不能当成新跑法也绿（2026-11-12 研究铃音指出；
// 以前改 go-verify.ts 会被 go-affected 判成整套，但指纹没变，整套又全被记录认成验过、一个包也没跑）。和 go-affected.ts 的 FULL 里的脚本对应
export const RUNNERS = ['scripts/go-verify.ts', 'scripts/go-affected.ts', 'scripts/go-shard.ts', 'scripts/go-shard.cmd', 'scripts/run-low.ps1', 'scripts/server-verify.ts', 'scripts/pre-push.ts', 'scripts/merge-queue.ts', 'scripts/gen-rpc.ts', 'scripts/nightly.ts']
const BASE = ['native', 'prompts', 'characters', 'sumika.example.yml', '.github/workflows/race.yml', 'scripts/bench-t2', 'testdata/shared', 'protocol']
export const WATCH = [...BASE, ...RUNNERS]
const KEEP = 200

// core.quotepath=false：中文文件名不转义成八进制，按前缀挑包、挑检查才认得出（不靠本机 git 设置）
export const git = (args: string[]) => Bun.spawnSync(['git', '-c', 'core.quotepath=false', ...args]).stdout.toString().trim()

// 一个提交里 WATCH 那几处的树合起来的指纹：git ls-tree 列出它们各自的对象号（没有的就不列），整段取哈希
// env 只给测试用：在临时仓库里算，不让钩子带进来的 GIT_DIR 指回真仓库
export function goTree(commit: string, env?: Record<string, string | undefined>) {
  const p = Bun.spawnSync(['git', 'ls-tree', commit, '--', ...WATCH], env ? { env } : {})
  if (p.exitCode !== 0) throw new Error(`git ls-tree ${commit}：${p.stderr.toString()}`)
  return new Bun.CryptoHasher('sha1').update(p.stdout.toString()).digest('hex')
}

// git 调钩子时把自己的 libexec/git-core 插到 PATH 最前面；去掉这一段，钩子里起的测试和终端里手跑的看到同一个 PATH
export function goEnv() {
  const norm = (p: string) => p.replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase()
  const execPath = norm(git(['--exec-path']))
  const pathKey = Object.keys(process.env).find((k) => k.toUpperCase() === 'PATH') ?? 'PATH'
  const parts = (process.env[pathKey] ?? '').split(';')
  while (execPath && parts.length > 1 && norm(parts[0]) === execPath) parts.shift()
  return { ...process.env, [pathKey]: parts.join(';') }
}

// 同一套 go：版本和会改变编出来的测试程序的几个设置。换了任何一样，记录就不认
export function goFingerprint(env: Record<string, string | undefined>) {
  const p = Bun.spawnSync(['go', 'env', 'GOVERSION', 'GOOS', 'GOARCH', 'GOFLAGS', 'CGO_ENABLED', 'GOEXPERIMENT'], { cwd: 'native', env })
  if (p.exitCode !== 0) throw new Error(`go env：${p.stderr.toString()}`)
  return p.stdout.toString().trim().split(/\r?\n/).join('|')
}

// 记录放在 git 的公共目录里：worktree 里跑的，主工作区推送时也找得到。测试用 SUMIKA_GO_VERIFIED 指到别处
export const recordPath = () => process.env.SUMIKA_GO_VERIFIED || join(git(['rev-parse', '--path-format=absolute', '--git-common-dir']), 'sumika-go-verified.json')

export function loadVerified(path = recordPath()): Verified[] {
  try {
    const v = JSON.parse(readFileSync(path, 'utf8'))
    return Array.isArray(v) ? v.filter((r) => r && typeof r.tree === 'string' && typeof r.go === 'string' && Array.isArray(r.pkgs)) : []
  } catch {
    return []   // 没有或写坏了：当成什么都没验过，只会多跑，不会少跑
  }
}

// 先写临时文件再改名，读的人不会读到半份；两个会话同时写，后写的盖掉先写的，丢的那条只会让下次多跑一遍。
// 记录是尽力而为：写不进去只提醒、返回 false，不让检查变红。Windows 上目标文件正被别的进程打开（杀毒扫刚写的文件）时
// 改名会报 EPERM（2026-11-01 单元测试里连着撞上两次），所以隔一会儿再试几次，最后退回直接写（写坏了读的时候当没验过）
export function addVerified(r: Verified, path = recordPath()) {
  const body = JSON.stringify([...loadVerified(path), r].slice(-KEEP), null, 1)
  const tmp = `${path}.${process.pid}.tmp`
  try {
    writeFileSync(tmp, body)
    for (let i = 1; ; i++) {
      try {
        renameSync(tmp, path)
        return true
      } catch (e) {
        if (i >= 8) throw e
        Bun.sleepSync(25 * i)
      }
    }
  } catch {
    try {
      writeFileSync(path, body)
      return true
    } catch (e) {
      console.log(`  没记下验证记录（${(e as Error).message}），下次会再跑一遍`)
      return false
    } finally {
      rmSync(tmp, { force: true })
    }
  }
}

// 给人看的时间用本地时间（记录里存的是 UTC 的 ISO 串，早上 8 点前直接看会差一天）
export const when = (iso: string) => new Date(iso).toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })

// 这次要跑的包里，还没在同一份代码、同一套 go 上验过的（几条记录合起来算）
export function uncovered(records: Verified[], tree: string, go: string, pkgs: string[], full = false) {
  const done = new Set(records.filter((r) => r.tree === tree && r.go === go && !(full && r.short)).flatMap((r) => r.pkgs))
  return pkgs.filter((p) => !done.has(p))
}

// go test 的输出里过了的包：`ok  包 …`、`?  包 [no test files]`（vet 过了、没有测试）。只认这次要跑的包，别的行不算
export function passedPkgs(out: string, pkgs: string[]) {
  const ok = new Set<string>()
  for (const line of out.split(/\r?\n/)) {
    const m = /^(?:ok|\?)\s+(\S+)/.exec(line)
    if (m) ok.add(m[1])
  }
  return pkgs.filter((p) => ok.has(p))
}

// 工作区就是这份代码：HEAD 的那几处对得上，那几处也没有没提交的改动（含没跟踪的文件）
export const dirty = () => git(['status', '--porcelain', '--untracked-files=all', '--', ...WATCH])
export const worktreeIs = (tree: string) => goTree('HEAD') === tree && dirty() === ''

export type VerificationRun = { ok: boolean; seconds: number; out: string }

// 经 run-low 低优先级跑（AGENTS「验收」），退出码原样带回；一律走验证档（SUMIKA_TEST_LANE=verify，ADR-320：合并队列、推送钩子共用这一个独占名额）。capture 时标准输出先攒着、跑完再原样打出来（要从里面认出哪些包过了）
export function runLow(label: string, cmd: string[], env: Record<string, string | undefined>, capture = false) {
  const t = Date.now()
  const p = Bun.spawnSync(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', '../scripts/run-low.ps1', ...cmd], { cwd: 'native', env: { ...env, SUMIKA_TEST_LANE: 'verify' }, stdout: capture ? 'pipe' : 'inherit', stderr: 'inherit' })
  const out = capture ? (p.stdout?.toString() ?? '') : ''
  if (capture) process.stdout.write(out)
  const seconds = (Date.now() - t) / 1000
  console.log(`  ${p.exitCode === 0 ? '过' : '红'}（退出码 ${p.exitCode}，${seconds.toFixed(1)} 秒）：${label}`)
  return { ok: p.exitCode === 0, seconds, out }
}

// mygo vet 自己先执行整个模块的 go vet；只有根包界面实现或依赖变了才加这道寿命检查。
export const needsMygoVet = (files: string[]) => files.some((f) => /^native\/[^/]+\.go$/.test(f) && !f.endsWith('_test.go') || /^native\/go\.(mod|sum)$/.test(f))

// 同一份 native/、同一套 go 上 mygo vet 过了就不再跑（2026-11-12：队列跑过、推送又跑一遍 27 秒）。
// 只有调用方给了 commit、工作区就是这个提交时才认和记（cleanTree）；SUMIKA_PUSH_FULL=1 照跑。run 只给测试替换
export function verifyMygo(files: string[], env: Record<string, string | undefined>, o: { commit?: string; by?: string; records?: string; run?: () => VerificationRun } = {}) {
  if (!needsMygoVet(files)) return undefined
  const tree = o.commit && env.SUMIKA_PUSH_FULL !== '1' ? cleanTree(process.cwd(), o.commit, env) : undefined
  const key = tree ? stepScope(process.cwd(), tree, 'mygo vet', env) : undefined
  const go = goFingerprint(env)
  const seen = key ? loadStepRecords(o.records).find((r) => r.tree === key && r.step === 'mygo vet' && r.bun === go) : undefined
  if (seen) { console.log(`  过（0.0 秒）：mygo vet：这份代码已验过（${seen.by}，${seen.at.slice(5, 16).replace('T', ' ')}），不再跑`); return { ok: true, seconds: 0, out: '' } }
  const r = (o.run ?? (() => runLow('mygo vet 原生界面寿命', ['go', 'tool', 'mygo', 'vet', '.'], env)))()
  if (r.ok && key) addStepRecord({ tree: key, bun: go, step: 'mygo vet', at: new Date().toISOString(), by: o.by ?? '?' }, o.records)
  return r
}

// 最慢的几个包要和别的包分开跑：分进程的同时再和一百多个包挤在一起，internal/app 从 161 秒拖到 467 秒，对时间敏感的测试还会误红（2026-11-09 实测）。
// turn、memory 2026-11-12 包内加了并行后十几秒，不再单独排（实测分 10 份 14 秒、不分 13 秒），和别的包一起跑
export const SLOW_PKG = /(^|\/)sumika-native(\/internal\/app)?$/
export const splitSlow = (pkgs: string[]) => ({ slow: pkgs.filter((p) => SLOW_PKG.test(p)), rest: pkgs.filter((p) => !SLOW_PKG.test(p)) })

// overBudget 从 go test 的输出里挑出超过预算（规范-测试耗时：一个包 60 秒）的包，只提醒，不算红。
export const PACKAGE_BUDGET_SECONDS = 60
export function overBudget(out: string) {
  return [...out.matchAll(/^ok\s+(\S+)\s+([\d.]+)s/gm)].filter((m) => Number(m[2]) > PACKAGE_BUDGET_SECONDS).map((m) => `${m[1]} ${Number(m[2]).toFixed(0)} 秒`)
}

// runTests 先跑别的包，再一个一个跑最慢的几个包，每个按记下的耗时分成“核数减 2”份并行跑（scripts/go-shard.ts；2026-11-12 以前是按名字轮流分六份）。
// 整套不分，保持串行。.cmd 是因为 run-low 传参时带空格的参数会被拆开，-exec 这里只能是一个不带空格的路径。
function runTests(pkgs: string[], flags: string[], short: boolean, env: Record<string, string | undefined>): VerificationRun {
  const label = (n: number, what: string) => `go test ${short ? '-short ' : ''}-count=1 -timeout=20m ${n} 个包${what}`
  const { slow, rest } = short ? splitSlow(pkgs) : { slow: [], rest: pkgs }
  const first = rest.length ? runLow(label(rest.length, ''), ['go', 'test', ...flags, ...rest], env, true) : { ok: true, seconds: 0, out: '' }
  if (!slow.length) return first
  const shard = resolve('scripts/go-shard.cmd').replaceAll('\\', '/')
  const second = runLow(label(slow.length, '（最慢的几个，分进程）'), ['go', 'test', ...flags, '-p', '1', '-exec', shard, ...slow], env, true)
  const over = overBudget(first.out + second.out)
  if (over.length) console.log(`  提醒：这些包超过 ${PACKAGE_BUDGET_SECONDS} 秒（规范-测试耗时）：${over.join('，')}`)
  return { ok: first.ok && second.ok, seconds: first.seconds + second.seconds, out: first.out + second.out }
}

// vet 过了再 test；过了的包一个记一个（vet 红了一个都不记）。工作区从头到尾都是这份代码才记
// short：合并前、推送前默认 go test -short；整套（--full、SUMIKA_PUSH_FULL=1）不加，和每天巡检一样跑满
// opts 只给测试换掉真跑和写记录，不改上面的判定
export type VerifyOpts = { test?: VerificationRun; add?: (r: Verified) => boolean }
export function verify(pkgs: string[], tree: string, commit: string, by: string, env: Record<string, string | undefined>, short = true, checkedVet?: VerificationRun, opts: VerifyOpts = {}) {
  const go = goFingerprint(env)
  const before = worktreeIs(tree)
  // vet 整个模块（ADR-225 第 1 条）：只测改到的包和直接导入方（ADR-362），更远的引用方编译不过（含测试代码）由这一步当场拦；记录只在 vet 过了才写
  const vet = checkedVet ?? runLow('go vet 整个模块', ['go', 'vet', './...'], env)
  // -timeout=20m：Go 默认 10 分钟，几路测试并行时 app、turn、根包会被负载拖过线而误红（2026-11-08 连续三次）；测试一条不少，只放宽墙钟上限
  // -vet=off：go test 默认每个包先跑一遍精简的 vet；上面已经 vet 过整个模块、过了才走到这里，再跑一遍是重复的（2026-11-12）
  const flags = short ? ['-short', '-count=1', '-timeout=20m', '-vet=off'] : ['-count=1', '-timeout=20m', '-vet=off']
  const test = vet.ok ? (opts.test ?? runTests(pkgs, flags, short, env)) : { ok: false, seconds: 0, out: '' }
  const passed = vet.ok ? (test.ok ? pkgs : passedPkgs(test.out, pkgs)) : []
  const same = before && worktreeIs(tree)
  const recorded = passed.length > 0 && same && (opts.add ?? addVerified)({ tree, commit, pkgs: passed, go, at: new Date().toISOString(), seconds: Math.round(vet.seconds + test.seconds), by, ...(short ? { short: true } : {}) })
  if (passed.length && !same) console.log('  没记：跑的时候工作区不是这份代码（或中途变过），下次会再跑')
  return { ok: vet.ok && test.ok, recorded, passed, vet, test }
}

// 命令行：--full 整套；--by <名字> 写进验证记录（夜间整套用 nightly；不写照旧 go-verify）；第一个别的参数是基准分支。
// 单独成函数是因为 --by 的值也是参数，不能像以前那样把第一个非 -- 开头的当成基准
export type VerifyArgs = { full: boolean; base: string; by: string }
export function parseVerifyArgs(args: string[]): VerifyArgs {
  let full = false
  let by = 'go-verify'
  const positional: string[] = []
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!
    if (a === '--full') { full = true; continue }
    if (a === '--by') {
      const v = args[++i]
      if (!v || v.startsWith('--')) throw new Error('--by 后面要跟名字')
      by = v
      continue
    }
    if (a.startsWith('--by=')) {
      const v = a.slice('--by='.length)
      if (!v) throw new Error('--by 后面要跟名字')
      by = v
      continue
    }
    if (a.startsWith('--')) continue
    positional.push(a)
  }
  return { full, base: positional[0] ?? 'main', by }
}

if (import.meta.main) {
  const args = process.argv.slice(2)
  let opts: VerifyArgs
  try { opts = parseVerifyArgs(args) } catch (e) { console.log((e as Error).message); process.exit(2) }
  const { full, base, by } = opts
  const commit = git(['rev-parse', 'HEAD'])
  if (dirty()) {
    console.log(`工作区里有 Go 测试会读到、还没提交的改动，测出来的不是 HEAD 的代码。先提交再跑：
${dirty()}`)
    process.exit(2)
  }
  const tree = goTree('HEAD')
  const env = goEnv()
  let files: string[] = full ? ['native/go.mod'] : []
  if (!full) {
    const mb = git(['merge-base', base, 'HEAD'])
    if (!mb) { console.log(`找不到 ${base} 和 HEAD 的共同祖先`); process.exit(2) }
    files = git(['diff', '--name-only', '--no-renames', mb, 'HEAD']).split(/\r?\n/).filter(Boolean)
  }
  // 按改了哪些路径挑 Bun 后台和协议的检查（规格-Bun后台第1批 第八节）；--full（每日整套）全都跑
  const pl = full ? FULL_PLAN : plan(files)
  const extra: string[] = []   // 结论里单独写一行的（队列把"合并说明另写："的行接在结论后面）
  const wrapUp = (line: string, lead = '') => { console.log(`${lead}合并说明可以写：${line}`); for (const l of extra) console.log(`合并说明另写：${l}`) }
  if (needsRunPlanned(pl)) {
    // 公开导出的仓库没有 server/、desktop/：不存在的路径不列出来，免得提示里出现根本没有的目录
    const planned = [...PLANNED_PATHS].filter((p) => existsSync(join(process.cwd(), p)))
    const bunDirty = git(['status', '--porcelain', '--untracked-files=all', '--', ...planned])
    if (bunDirty) { console.log(`工作区里有 Bun 检查、生成检查会读到、还没提交的改动，测出来的不是 HEAD 的代码。先提交再跑：
${bunDirty}`); process.exit(2) }
    // 同一棵树上验过的步骤不再跑（整套 --full 照跑）；工作区就是 HEAD 这棵树时才认、才记
    const steps = runPlanned(pl, process.cwd(), env, undefined, full ? {} : { tree: cleanTree(process.cwd()), by })
    extra.push(...steps.map((s) => s.summary))
    if (steps.some((s) => !s.ok)) { console.log(`有检查红了。没过的：${steps.filter((s) => !s.ok).map((s) => s.name).join('、')}。Go 部分没跑，改好再跑一遍`); process.exit(1) }
  }
  if (!pl.go) { wrapUp('没有 Go 改动，跳过 Go 的 vet、test、archcheck'); process.exit(0) }
  const graph = readGraph('native', env)
  const all = graph.pkgs
  let pkgs = all
  let why = '整套的 '
  let chosen = '整套'   // 挑包的说明，进结论行（合并说明里看得出这次测了哪些层）
  if (!full) {
    const r = pick(files, graph)
    chosen = describePick(r)
    if (!r.full) { pkgs = r.pkgs; why = `${base} 以来 ${files.length} 个文件，${chosen}，合计 ` }
    else why = `整套（${base} 以来改了会被很多测试读到的文件）的 `
  }
  const mygo = verifyMygo(files, env, { commit, by })
  if (mygo && !mygo.ok) process.exit(1)
  const need = uncovered(loadVerified(), tree, goFingerprint(env), pkgs, full)
  console.log(`Go 验证：${commit.slice(0, 8)}，${why}${pkgs.length} 个包（共 ${all.length} 个）${need.length < pkgs.length ? `，其中 ${pkgs.length - need.length} 个已在这份代码上验过，跑剩下的 ${need.length} 个` : ''}`)
  if (!need.length) { wrapUp(`go vet、go test ${full ? "" : "-short "}-count=1 ${pkgs.length} 个包都过（${by} 的记录；挑包：${chosen}）`, '都验过了。'); process.exit(0) }
  const r = verify(need, tree, commit, by, env, !full, mygo)
  const failed = need.filter((p) => !r.passed.includes(p))
  if (r.ok) wrapUp(`go vet 整个模块退出码 0（${r.vet.seconds.toFixed(0)} 秒），go test ${full ? "" : "-short "}-count=1 ${need.length} 个包退出码 0（${r.test.seconds.toFixed(0)} 秒）${need.length < pkgs.length ? `；另 ${pkgs.length - need.length} 个包之前已验过` : ''}；挑包：${chosen}`, `${r.recorded ? '记下了' : '没记下'}。`)
  else console.log(`有检查红了。${r.passed.length ? `过了的 ${r.passed.length} 个${r.recorded ? '已记下' : '没记下'}，` : ''}没过的 ${failed.length} 个：${failed.map((p) => p.replace('sumika-native/', '')).join('、')}。改好或机器闲下来再跑一遍，只会跑没过的`)
  process.exit(r.ok ? 0 : 1)
}
