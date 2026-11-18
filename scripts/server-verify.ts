// 按改了哪些路径挑 Bun 后台（server/）和协议的检查（规格-Bun后台第1批 第八节）。go-verify、pre-push、合并队列共用这一份挑选。
//   bun scripts/server-verify.ts <文件>...     手动看会挑什么（路径相对仓库根）
import { existsSync, readdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { readsGo } from './go-affected'
import { hasServer } from './gen-rpc'

// 协议的两个生成文件（gen-rpc.ts 写的）
export const GEN_FILES = ['native/internal/rpcwire/wire_gen.go', 'server/src/protocol/gen.ts']

export type Plan = {
  go: boolean    // 跑 Go 的 vet / test / archcheck
  bun: boolean   // 在 server/ 里跑 bun test
  gen: boolean   // 跑 gen-rpc.ts --check
  scripts?: boolean // 挑选和队列脚本自己改了：点名跑它们的测试（根目录 bun test 只跑 test/，scripts/*.test.ts 要点名）
}

// 挑选、队列、生成器这几份脚本：它们改了，所有检查都要跑（检查本身也是高风险改动，AGENTS 第 7 条）
const CHECK_SCRIPTS = /^scripts\/(server-verify|go-verify|go-affected|pre-push|merge-queue(-\d+)?|merge-queue\.testlib|nightly(-record)?|gen-rpc|precommit-fast|go-shard|friction-report|slot|run-low|check-commit-msg)(\.test)?\.ts$|^scripts\/(run-low\.ps1|go-shard\.cmd)$|^lefthook\.yml$/
// Bun 测试会读到、却不在 server/ 里的 Go 文件：迁移 SQL（server/test 跑同一套迁移）和它照抄的显示规则
const BUN_READS_GO = /^native\/internal\/store\/(migrations\/|display[^/]*\.go$|local_groups\.go$|platform\.go$)|^native\/internal\/desktopread\//
export const SCRIPT_TESTS = ['./scripts/server-verify.test.ts', './scripts/merge-queue.test.ts', './scripts/merge-queue-2.test.ts', './scripts/merge-queue-3.test.ts', './scripts/go-affected.test.ts', './scripts/go-verify.test.ts', './scripts/nightly.test.ts', './scripts/precommit-fast.test.ts', './scripts/go-shard.test.ts', './scripts/run-low.test.ts', './scripts/check-commit-msg.test.ts', './scripts/export-public.test.ts', './scripts/sumika-dev.test.ts']

const isServer = (f: string) => f.startsWith('server/')
// 主窗口（desktop/）的文件：冻结的网页，不再有它自己的检查（ADR-359、378）；只改它时不用跑 Go
const isDesktop = (f: string) => f.startsWith('desktop/')
const isProtocol = (f: string) => f.startsWith('protocol/') || f === 'scripts/gen-rpc.ts' || GEN_FILES.includes(f)
// Go 的测试会读到的地方：native/、整个 testdata/（含 shared）、协议定义，加上 go-affected 的 readsGo（"改了就整套跑"的那批和示例配置）
const isGo = (f: string) => f.startsWith('native/') || f.startsWith('testdata/') || f.startsWith('protocol/') || readsGo(f)

// 空改动：和以前一样只跑 Go。只有"改到了 server/、又没有任何 Go 会读到的文件"才跳过 Go；只改文档、脚本的照旧跑 Go
export function plan(files: string[]): Plan {
  if (files.some((f) => CHECK_SCRIPTS.test(f))) return { go: true, bun: true, gen: true, scripts: true }
  // 共用场景只有 rpc/（Go 和 Bun 的通信协议）还挡合并；其余（记忆、QQ、回合、补录）是冻结的 Bun 实现对拍，
  // Go 照 ADR-384 等有意改行为时 Bun 不跟着改，红了不该挡（ADR-378：冻结代码碰到关卡就摘掉并登记；后续路线决 12）
  const bun = files.some((f) => isServer(f) || f.startsWith('protocol/') || f.startsWith('testdata/shared/rpc/') || GEN_FILES.includes(f) || BUN_READS_GO.test(f))
  const gen = files.some(isProtocol)
  // 只改 server/ 或 desktop/、没有 Go 会读到的文件：跳过 Go
  const go = !(files.some((f) => isServer(f) || isDesktop(f)) && !files.some(isGo))
  return { go, bun, gen }
}

// 要不要进 runPlanned：go-verify、pre-push 共用这一个判断，新加一类检查只改这里
export function needsRunPlanned(p: Plan): boolean {
  return !!(p.bun || p.gen || p.scripts)
}

// runPlanned 测的是工作区：这几处和要测的提交不一样时测出来的不是那份代码，调用方先查这几处干不干净
export const PLANNED_PATHS = ['server', 'protocol', 'testdata/shared', 'desktop', 'native/internal/design']

// --full（每日整套）：全都跑；公开导出的仓库没有 server/、desktop/ 时由各步骤自己跳过并在结论里写明
export const FULL_PLAN: Plan = { go: true, bun: true, gen: true, scripts: true }

export type StepResult = { name: string; ok: boolean; seconds: number; summary: string; skipped?: boolean }

// 用真实退出码：不吞、不用 || true。输出原样打出来，同时留下来认"N 通过"
function exec(cmd: string[], cwd: string, env: Record<string, string | undefined>) {
  const t = Date.now()
  const p = Bun.spawnSync(cmd, { cwd, env, stdout: 'pipe', stderr: 'pipe' })
  const out = p.stdout.toString() + p.stderr.toString()
  process.stdout.write(out)
  return { code: p.exitCode, out, seconds: (Date.now() - t) / 1000 }
}

// 重活经 run-low 跑：最低优先级、全机验证档名额、叫它的进程没了整串收掉（scripts/run-low.ps1）。
// runPlanned 只被合并队列（go-verify）和推送钩子调，ADR-320 里这两处都是验证档，所以这里写死 verify（2026-11-12：以前 bun test 不排名额，
// 推送钩子的 server 整套会和队列的 go test 叠着跑）。参数经 Start-Process 传，不能带空格——这里的 bun 路径和测试路径都没有空格。
function lowExec(cmd: string[], cwd: string, env: Record<string, string | undefined>) {
  return exec(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', join(import.meta.dir, 'run-low.ps1'), ...cmd], cwd, { ...env, SUMIKA_TEST_LANE: 'verify' })
}

// server 整套按文件分进程并行跑（2026-11-12 实测 2835 条：串行 133 秒、--parallel=4 44 秒、=8 35 秒，通过数都一样）。
// 不加 --no-isolate：实测没更快（45 秒），还会让文件之间看到彼此留下的全局状态。默认核数减 4、最多 8，SUMIKA_BUN_PARALLEL 可改
export function bunParallel(env: Record<string, string | undefined>, cores = navigator.hardwareConcurrency) {
  const set = Number(env.SUMIKA_BUN_PARALLEL)
  if (Number.isInteger(set) && set > 0) return set
  return Math.max(1, Math.min(8, cores - 4))
}

export function parseBunCounts(out: string) {
  // 取最后一个汇总：测试里自己再起 bun test 时，内层的汇总先打出来
  const last = (re: RegExp) => [...out.matchAll(re)].pop()?.[1]
  const pass = last(/^\s*(\d+) pass\b/gm)
  const fail = last(/^\s*(\d+) fail\b/gm)
  return { pass: pass === undefined ? undefined : Number(pass), fail: fail === undefined ? undefined : Number(fail) }
}

// 跳过、待办、只跑某几个（test.skip / test.todo / test.only）时 bun test 仍以 0 退出：这里当红
export function parseBunSkips(out: string) {
  const skip = Number(/^\s*(\d+) skip\b/m.exec(out)?.[1] ?? 0)
  const todo = Number(/^\s*(\d+) todo\b/m.exec(out)?.[1] ?? 0)
  return { skip, todo }
}

export function bunSummary(code: number | null, out: string) {
  const { pass, fail } = parseBunCounts(out)
  const s = parseBunSkips(out)
  if (code === 0 && (s.skip || s.todo)) return `server bun test：红（${s.skip} 个跳过、${s.todo} 个待办：检查里不许跳过）`
  if (code === 0) return pass === undefined ? 'server bun test：通过（退出码 0）' : `server bun test：${pass} 通过`
  return `server bun test：红（退出码 ${code}${fail ? `，${fail} 个失败` : ''}${pass !== undefined ? `，${pass} 通过` : ''}）`
}

// server/test 下拿 Go 的测试数据对拍、永远对不上的文件（路径相对 server/test）：不进关卡，见 runServerTest。
// 只摘真对不上的（复核 2026-11-16）：turn-golden 比 Go 的回合录像；policy-golden 有 30 条是措辞（所有者 / 主人）对不上，
// 冻结代码不能为过关改、按用例摘就得改 Bun 的测试文件，所以整个文件摘掉——代价是 Bun 审批策略引擎的 risk、shell、decide
// 判定不再由关卡看（待办登记）。memory-write 是共用场景执行器，ADR-384 把 testdata/shared/memory-write 里的“主人”改成“所有者”，
// 冻结的 Bun 同样永远对不上（决 12：共用场景执行器不挡合并）。local-tools-golden、textclean、memory-recall、shared-turn、shared-rpc
// 在 main 上是绿的，留在关卡里
// memory-recall、memory-write-core 等经 server/test/memory-write-fixture.ts、server/src/memory/fixture.ts 读 Go 源码里的 ownerPoolSchema 拼库，P1-3（382dc887）把建表收进基线后这个名字没了，冻结的 Bun 永远对不上（2026-11-17 摘，待办登记）
export const GO_PARITY_TESTS = ['turn-golden.test.ts', 'policy-golden.test.ts', 'memory-write.test.ts', 'memory-recall.test.ts', 'memory-write-core.test.ts',
  'memory-write-precheck.test.ts', 'memory-wiring.test.ts', 'memory-wiring-steer.test.ts', 'selftest-isolation.test.ts', 'selftest-memory.test.ts',
  // 下面这些回放 native/internal/linkreader、web/wiki 的夹具：公开前把夹具换成编造的（2026-11-17，chore/pub-fixtures），冻结的 Bun 断言的还是旧的真实内容
  'link-bilibili-opus.test.ts', 'link-bilibili.test.ts', 'link-booth.test.ts', 'link-douyin-netease.test.ts', 'link-pixiv.test.ts',
  'link-plugin-wiring.test.ts', 'link-steam.test.ts', 'link-twitter.test.ts', 'link-youtube.test.ts', 'web-wiki.test.ts']
// 冻结的 Bun 在计划任务环境里收 run/ 的权限超时（2026-11-17 每日整套），不是对不上而是跑不完。
// ADR-378：冻结代码碰到关卡就摘掉并登记（docs/待办.md）
// web-wiring：冻结的 Bun 联网接线测试 2026-11-18 在 main 上也红（合并队列 chore/sumika-dev-1 带出整套时发现），
// 搜索落到了真的 DuckDuckGo、没有结果，断言的“编造的结果”对不上；不为过关改冻结代码，摘掉并登记（docs/待办.md）
export const FROZEN_FLAKY_TESTS = ['run-file-secure.test.ts', 'web-wiring.test.ts']
// bun test 自己认的测试文件名（.test / _test / .spec / _spec，ts、tsx、js、mjs 等）：显式列文件时照它挑，免得以后漏跑
const BUN_TEST_FILE = /[._](test|spec)\.[cm]?[jt]sx?$/

// server/test 下哪些文件进关卡：两条摘名单都排除（路径相对 server/test）
export function serverTestFiles(dir: string): string[] {
  const skip = [...GO_PARITY_TESTS, ...FROZEN_FLAKY_TESTS]
  return readdirSync(dir, { recursive: true }).map(String).filter((f) => BUN_TEST_FILE.test(f) && !skip.includes(f.replace(/\\/g, '/')))
}

// 在 server/ 里跑 bun test。专用 worktree 是新开的、没有 node_modules（被忽略）：先按锁文件装，装不上算红
export function runServerTest(root: string, env: Record<string, string | undefined>, bun = process.execPath): StepResult {
  const cwd = `${root}/server`
  const t = Date.now()
  // 公开导出的仓库没有 server/：跳过、不红，结论里写明原因
  if (!existsSync(cwd)) return { name: 'server bun test', ok: true, seconds: 0, summary: 'server bun test：没有 server/，跳过', skipped: true }
  // test.only 会让别的测试不跑、照样 0 退出：源码里有就直接红
  const only = readdirSync(`${cwd}/test`, { recursive: true }).map(String).filter((f) => f.endsWith('.ts') && /\b(test|it|describe)\.only\(/.test(readFileSync(`${cwd}/test/${f}`, 'utf8')))
  if (only.length) return { name: 'server bun test', ok: false, seconds: 0, summary: `server bun test：红（${only.join('、')} 里有 .only）` }
  // 每次都按锁文件装：专用 worktree 会留着旧的 node_modules，锁文件变了也得跟上（有缓存时很快）
  {
    const i = exec([bun, 'install', '--frozen-lockfile'], cwd, env)
    if (i.code !== 0) return { name: 'server bun test', ok: false, seconds: (Date.now() - t) / 1000, summary: `server bun test：红（server/ 装依赖失败，退出码 ${i.code}）` }
  }
  // 拿 Go 的测试数据来对拍的 Bun 测试不进关卡：Go 是标准，Go 有意改了（例：ADR-384 把回合录像里的“主人”换成称呼），
  // 冻结的 Bun 不跟着改，它们永远对不上（ADR-378：冻结代码碰到关卡就摘掉并登记；后续路线决 12）。其余照跑
  const files = serverTestFiles(`${cwd}/test`)
  if (!files.length) return { name: 'server bun test', ok: false, seconds: (Date.now() - t) / 1000, summary: 'server bun test：红（server/test 里除摘掉的以外一个测试文件都没有）' }
  const r = lowExec([bun, 'test', `--parallel=${bunParallel(env)}`, ...files.map((f) => `./test/${f.replace(/\\/g, '/')}`)], cwd, env)
  const sk = parseBunSkips(r.out)
  return { name: 'server bun test', ok: r.code === 0 && !sk.skip && !sk.todo, seconds: (Date.now() - t) / 1000, summary: bunSummary(r.code, r.out) }
}

export function runScriptTests(root: string, env: Record<string, string | undefined>, bun = process.execPath): StepResult {
  // 按文件分进程并行（2026-11-12：串行 124 秒；merge-queue 拆成三个文件后并行见下一次实测）
  const r = lowExec([bun, 'test', `--parallel=${bunParallel(env)}`, ...SCRIPT_TESTS], root, env)
  const { pass } = parseBunCounts(r.out)
  return { name: '检查脚本的测试', ok: r.code === 0, seconds: r.seconds, summary: r.code === 0 ? `检查脚本的测试：${pass ?? '?'} 通过` : `检查脚本的测试：红（退出码 ${r.code}）` }
}

export function runGenCheck(root: string, env: Record<string, string | undefined>, bun = process.execPath): StepResult {
  const r = exec([bun, 'scripts/gen-rpc.ts', '--check'], root, env)
  const what = hasServer(root) ? '' : '，没有 server/，只查了 Go 侧'
  return { name: 'gen-rpc --check', ok: r.code === 0, seconds: r.seconds, summary: r.code === 0 ? `gen-rpc --check：生成文件是新的${what}` : `gen-rpc --check：红（退出码 ${r.code}，生成文件和协议定义对不上${what}）` }
}

// ───── 验过的记录（2026-11-11：合并队列在同一棵树上跑完，推送钩子又整套重跑一遍，每次推送多花一分钟左右） ─────
// 和 go-verify 的记录同一个思路：按“整个提交的树 + 这一套 bun”认，树一个字不差、bun 没换才跳过；记录写坏或读不到就当没验过，只会多跑。
// 只在工作区就是这棵树（没有任何没提交的改动）时才记：测的是工作区，不一样就不记。
// tree 存的是 stepScope 算出的“这一步读到的那几处”的哈希（旧记录是整棵树的号，比不上就当没验过）
export type StepRecord = { tree: string; bun: string; step: string; at: string; by: string }
export const stepRecordPath = () => process.env.SUMIKA_STEPS_VERIFIED || join(Bun.spawnSync(['git', 'rev-parse', '--path-format=absolute', '--git-common-dir']).stdout.toString().trim(), 'sumika-steps-verified.json')
export function loadStepRecords(path = stepRecordPath()): StepRecord[] {
  try { const v = JSON.parse(readFileSync(path, 'utf8')); return Array.isArray(v) ? v.filter((r) => r && typeof r.tree === 'string' && typeof r.step === 'string' && typeof r.bun === 'string') : [] } catch { return [] }
}
export function addStepRecord(r: StepRecord, path = stepRecordPath()) {
  try {
    const all = [r, ...loadStepRecords(path).filter((x) => !(x.tree === r.tree && x.step === r.step && x.bun === r.bun))].slice(0, 200)
    const tmp = `${path}.${process.pid}.tmp`
    writeFileSync(tmp, JSON.stringify(all, null, 1)); renameSync(tmp, path)
  } catch { /* 记不下只会让下次多跑 */ }
}
// 这个提交的树，且工作区一点没改时才返回（可以记、可以认）；否则 undefined
export function cleanTree(root: string, commit = 'HEAD', env?: Record<string, string | undefined>): string | undefined {
  const g = (a: string[]) => Bun.spawnSync(['git', ...a], { cwd: root, env })
  const tree = g(['rev-parse', `${commit}^{tree}`]).stdout.toString().trim()
  const head = g(['rev-parse', 'HEAD^{tree}']).stdout.toString().trim()
  // 已跟踪的文件一处改动都不许有；没进 git 的文件只看测试会读到的几处（主工作区常年躺着与测试无关的草稿文件）
  const dirty = g(['status', '--porcelain', '--untracked-files=no']).stdout.toString().trim()
    + g(['ls-files', '--others', '--exclude-standard', '--', ...PLANNED_PATHS, 'scripts']).stdout.toString().trim()
  return tree && tree === head && !dirty ? tree : undefined
}

// 每一步实际会读到的路径：记录和比对只看这几处在这棵树里的内容（以前比整棵树，只改 desktop/ 的提交也让 server 的测试重跑，
// 2026-11-12 推送一次跑了 20 分钟）。漏写一处会让该重跑的不跑，所以宁多勿少
export const STEP_PATHS: Record<string, string[]> = {
  'gen-rpc --check': ['protocol', 'server', 'desktop', 'scripts'],
  // 跑这一步的脚本也算输入：脚本改了跑法（比如换参数），旧记录不能再认（2026-11-12 研究铃音复核指出）
  'server bun test': ['server', 'protocol', 'testdata/shared', 'scripts/server-verify.ts'],
  '检查脚本的测试': ['scripts', 'lefthook.yml', 'package.json', 'bun.lock', 'native/go.mod'],
  // mygo vet 先跑整个模块的 go vet、结果顶替 go vet ./...（go-verify 的 verify），所以范围是整个 native/；记录里的工具版本记 go 的指纹
  'mygo vet': ['native', 'scripts/go-verify.ts'],
}
// 这一步读到的那几处在 tree 里的对象号拼起来取哈希；路径不在这棵树里记成“-”。没写路径的步骤退回整棵树
export function stepScope(root: string, tree: string, step: string, env?: Record<string, string | undefined>): string {
  const paths = STEP_PATHS[step]
  if (!paths) return tree
  if (Bun.spawnSync(['git', 'rev-parse', '--verify', '-q', `${tree}^{tree}`], { cwd: root, env }).exitCode !== 0) return tree   // 不是这个仓库里的树（测试给的假号）：照整棵比
  const ids = paths.map((p) => { const r = Bun.spawnSync(['git', 'rev-parse', '--verify', '-q', `${tree}:${p}`], { cwd: root, env }); return `${p}=${r.exitCode === 0 ? r.stdout.toString().trim() : '-'}` })
  return 'scope:' + new Bun.CryptoHasher('sha1').update(`${step}|${ids.join('|')}`).digest('hex')
}

// runners 只给测试换掉真正的检查
// 记录里存的是 UTC（ISO），显示成本机时间“11-11 20:57”（以前直接截 ISO，显示成 12:57）
const localTime = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, '0'); return Number.isNaN(d.getTime()) ? iso : `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}` }

export type PlannedOpts ={ tree?: string; by?: string; records?: string; runners?: Partial<Record<string, () => StepResult>> }
// 按 plan 跑，返回每一步。gen 先跑（快），红了不再往下。给了 tree（调用方确认工作区就是这棵树）时，同一棵树、同一套 bun 验过的步骤不再跑，过了的记下
export function runPlanned(p: Plan, root: string, env: Record<string, string | undefined>, bun = process.execPath, opts: PlannedOpts = {}): StepResult[] {
  const steps: StepResult[] = []
  const bunVer = Bun.version
  const recs = opts.tree ? loadStepRecords(opts.records) : []
  const add = (s: StepResult) => { console.log(`  ${s.ok ? '过' : '红'}（${s.seconds.toFixed(1)} 秒）：${s.summary}`); steps.push(s); return s.ok }
  const step = (name: string, run: () => StepResult) => {
    const key = opts.tree ? stepScope(root, opts.tree, name, env) : undefined
    const seen = key ? recs.find((r) => r.tree === key && r.step === name && r.bun === bunVer) : undefined
    if (seen) return add({ name, ok: true, seconds: 0, summary: `${name}：这份代码已验过（${seen.by}，${localTime(seen.at)}），不再跑` })
    const s = (opts.runners?.[name] ?? run)()
    if (s.ok && key && !s.skipped) addStepRecord({ tree: key, bun: bunVer, step: name, at: new Date().toISOString(), by: opts.by ?? '?' }, opts.records)
    return add(s)
  }
  if (p.gen && !step('gen-rpc --check', () => runGenCheck(root, env, bun))) return steps
  if (p.bun) step('server bun test', () => runServerTest(root, env, bun))
  if (p.scripts) step('检查脚本的测试', () => runScriptTests(root, env, bun))
  return steps
}

// 手动看的计划行：公开导出的仓库没有 server/ 时，直接写这两步会跳过，不写“跑”
export function describePlan(p: Plan, root = process.cwd()): string {
  const server = existsSync(join(root, 'server'))
  const bun = p.bun ? (server ? '跑' : '跳过（没有 server/）') : '不跑'
  const gen = p.gen ? '跑' + (server ? '' : '（没有 server/，只查 Go 侧）') : '不跑'
  return `Go：${p.go ? '跑' : '没有 Go 改动，跳过'}；server bun test：${bun}；gen-rpc --check：${gen}`
}

if (import.meta.main) {
  const files = process.argv.slice(2)
  console.log(describePlan(plan(files), process.cwd()))
}
