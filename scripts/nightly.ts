// 每天整套（规格-每日整套，P1-5）：在专用 worktree 里检出本地 main 最新提交，等合并队列空闲，跑 go-verify --full --by nightly。
// 输出边跑边写主工作区 data/dev/test-runs/nightly-<本地时间>.txt，结论另记 <git 公共目录>/sumika-nightly.json（最近 30 条）。
// 结论和验证记录分开：验证记录只留 200 条、红的时候也照写过了的包，回答不了"整套是绿是红"（盘点坑 1、2、3）。
//   bun scripts/nightly.ts                              跑一次
//   bun scripts/nightly.ts --register [--at HH:MM]      登记每天的任务（默认 04:30，仅登录时运行）
//   bun scripts/nightly.ts --unregister                 注销
//   bun scripts/nightly.ts --status                     只读：任务在不在、下次几点、上次结果
// 登记会改系统的持久配置：主会话运行前先问主人（规格第 4 条）。测试只调 registerScript 这类纯函数，绝不真登记。
import { appendFileSync, cpSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { git, lockFile, makeCtx } from './merge-queue'
import { KEEP, loadNightly, type NightlyRecord } from './nightly-record'

export type { NightlyRecord } from './nightly-record'
export type NightlyCtx = { main: string; common: string; mainDir: string; wt: string; outDir: string; recordPath: string; queuePath: string }

// 外部依赖全走这里：测试换掉起子进程、文件、时钟，不碰真实 worktree、系统任务计划
export type NightlyDeps = {
  now: () => number
  sleep: (ms: number) => Promise<void>
  read: (p: string) => string
  write: (p: string, body: string) => void
  append: (p: string, body: string) => void
  rename: (from: string, to: string) => void
  remove: (p: string) => void
  exists: (p: string) => boolean
  mkdir: (p: string) => void
  copyDir: (from: string, to: string) => void
  git: (cwd: string, args: string[]) => { code: number; out: string; err: string }
  runVerify: (a: { cwd: string; script: string; args: string[]; env: Record<string, string | undefined>; onData: (text: string) => void; timeoutMs: number }) => Promise<{ code: number | null; timedOut?: boolean }>
  runPowerShell: (script: string) => { code: number | null; out: string }
  pidAlive: (pid: number) => boolean
}

export const NIGHTLY_TASK = 'SumikaNightly'
export const DEFAULT_AT = '04:30'
export const NIGHTLY_MAX_RUN_MS = 90 * 60_000

// 只收自己起的那个子进程和它的子进程：按名字杀会把主人手动开的 bun 一起带走
export function killProcessTree(pid: number) {
  if (!pid) return
  if (process.platform === 'win32') {
    Bun.spawnSync(['taskkill', '/T', '/F', '/PID', String(pid)], { stdout: 'ignore', stderr: 'ignore' })
    return
  }
  try { process.kill(-pid, 'SIGKILL') } catch { try { process.kill(pid, 'SIGKILL') } catch { /* 已经退了 */ } }
}

export function defaultDeps(): NightlyDeps {
  return {
    now: () => Date.now(),
    sleep: (ms) => {
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, ms)
      return promise
    },
    read: (p) => readFileSync(p, 'utf8'),
    write: (p, body) => writeFileSync(p, body),
    append: (p, body) => appendFileSync(p, body),
    rename: (from, to) => renameSync(from, to),
    remove: (p) => rmSync(p, { force: true }),
    exists: (p) => existsSync(p),
    mkdir: (p) => { mkdirSync(p, { recursive: true }) },
    copyDir: (from, to) => { cpSync(from, to, { recursive: true }) },
    git,
    runVerify: async ({ cwd, script, args, env, onData, timeoutMs }) => {
      const p = Bun.spawn([process.execPath, script, ...args], { cwd, env, stdout: 'pipe', stderr: 'pipe' })
      let timedOut = false
      const timer = setTimeout(() => { timedOut = true; killProcessTree(p.pid) }, timeoutMs)
      const dec = new TextDecoder()
      const pump = async (s: ReadableStream<Uint8Array>) => {
        for await (const c of s) onData(dec.decode(c, { stream: true }))
      }
      try {
        await Promise.all([pump(p.stdout), pump(p.stderr)])
        return { code: await p.exited, timedOut }
      } finally {
        clearTimeout(timer)
      }
    },
    runPowerShell: (script) => {
      const file = join(tmpdir(), `sumika-nightly-${process.pid}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}.ps1`)
      // PowerShell 5.1 按 BOM 认 UTF-8，没有 BOM 会把中文当 ANSI；脚本里有“仅登录时运行”这类注释
      writeFileSync(file, '\ufeff' + script, 'utf8')
      try {
        const p = Bun.spawnSync(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', file], { stdout: 'pipe', stderr: 'pipe' })
        return { code: p.exitCode, out: p.stdout.toString() + p.stderr.toString() }
      } finally {
        rmSync(file, { force: true })
      }
    },
    pidAlive: (pid) => {
      try { process.kill(pid, 0); return true } catch (e) { return (e as NodeJS.ErrnoException).code === 'EPERM' }
    },
  }
}

// 专用 worktree 放在主工作区旁边（和合并队列的 _queue 同一层）；record 在 git 公共目录，所有 worktree 读得到
export function makeNightlyCtx(cwd = process.cwd(), over: Partial<NightlyCtx> = {}): NightlyCtx {
  const q = makeCtx(cwd)
  return {
    main: q.main,
    common: q.common,
    mainDir: q.mainDir,
    wt: process.env.SUMIKA_NIGHTLY_WORKTREE || join(q.mainDir, '.claude', 'worktrees', 'nightly'),
    outDir: q.outDir,
    recordPath: process.env.SUMIKA_NIGHTLY_RECORD || join(q.common, 'sumika-nightly.json'),
    queuePath: q.statePath,
    ...over,
  }
}

// 先写临时文件再改名，读的人不会读到半份；Windows 上目标文件正被别的进程打开时改名报 EPERM，隔一会儿再试（同 addVerified）。
// 最后还不行就直接写：结论是尽力而为，写坏了下次读的人当没有记录
export async function addNightly(r: NightlyRecord, path: string, deps: Pick<NightlyDeps, 'read' | 'write' | 'rename' | 'remove' | 'sleep'> = defaultDeps()) {
  const body = JSON.stringify([...loadNightly(path, deps.read), r].slice(-KEEP), null, 1)
  const tmp = `${path}.${process.pid}.${Math.random().toString(36).slice(2, 8)}.tmp`
  try {
    deps.write(tmp, body)
    for (let i = 1; ; i++) {
      try { deps.rename(tmp, path); return true } catch (e) { if (i >= 8) throw e; await deps.sleep(25 * i) }
    }
  } catch {
    try {
      deps.write(path, body)
      return true
    } catch (e) {
      console.log(`  没记下整套结论（${(e as Error).message}）`)
      return false
    } finally {
      deps.remove(tmp)
    }
  }
}

// go test 红的包：`FAIL\t包\t秒数`（build failed 也在这行）。只取包名，去重
export function failedPkgs(out: string) {
  const seen = new Set<string>()
  for (const line of out.split(/\r?\n/)) {
    const m = /^FAIL\s+(\S+)/.exec(line)
    if (m) seen.add(m[1]!)
  }
  return [...seen]
}

// go-verify 的两句真结论（scripts/go-verify.ts 的 wrapUp）：
//   这次跑了整套：合并说明可以写：go vet 整个模块退出码 0（N 秒），go test -count=1 N 个包退出码 0（N 秒）…
//   同一棵树已经验过：都验过了。合并说明可以写：go vet、go test -count=1 N 个包都过（nightly 的记录；挑包：整套）
// 只认 "go test -count=1"，带 -short 的短跑不算；退出码 0 本身不是证据
export function ranEvidence(out: string): 'full' | 'reused' | undefined {
  if (/都验过了。合并说明可以写：go vet、go test -count=1 \d+ 个包都过（nightly 的记录；挑包：整套）/.test(out)) return 'reused'
  if (/合并说明可以写：go vet 整个模块退出码 0（\d+ 秒），go test -count=1 \d+ 个包退出码 0（\d+ 秒）/.test(out)) return 'full'
  return undefined
}

export function localStamp(ms: number) {
  const d = new Date(ms), p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
}

// 专用 worktree 回到干净状态并 detach 到本地 main：没有就建，顺手拷主工作区的 vendor/bin（新 worktree 缺 rg、fd）
export function prepWorktree(ctx: NightlyCtx, deps: NightlyDeps = defaultDeps()) {
  const wt = ctx.wt
  if (!deps.exists(join(wt, '.git'))) {
    deps.git(ctx.mainDir, ['worktree', 'prune'])
    deps.mkdir(dirname(wt))
    const r = deps.git(ctx.mainDir, ['worktree', 'add', '--detach', '--force', wt, ctx.main])
    if (r.code !== 0) throw new Error(`建整套 worktree 失败（${wt}）：${r.err}`)
  } else {
    deps.git(wt, ['merge', '--abort'])
    deps.git(wt, ['reset', '--hard'])
    deps.git(wt, ['clean', '-fd'])   // 不加 -x：vendor/bin 之类被忽略的文件留着
    const r = deps.git(wt, ['checkout', '--detach', ctx.main])
    if (r.code !== 0) throw new Error(`整套 worktree 切不到 ${ctx.main}：${r.err}`)
  }
  const vb = join(ctx.mainDir, 'vendor', 'bin')
  if (deps.exists(vb) && !deps.exists(join(wt, 'vendor', 'bin'))) {
    deps.mkdir(join(wt, 'vendor'))
    deps.copyDir(vb, join(wt, 'vendor', 'bin'))
  }
  const commit = deps.git(wt, ['rev-parse', 'HEAD'])
  const tree = deps.git(wt, ['rev-parse', 'HEAD^{tree}'])
  if (commit.code !== 0 || tree.code !== 0) throw new Error(`整套 worktree 读不到 HEAD：${commit.err || tree.err}`)
  return { commit: commit.out, tree: tree.out }
}

// 队列有条目在跑就等它跑完；最多等 maxMs，等不到照跑（名额锁会排队，盘点坑 6）
export async function waitQueueIdle(ctx: NightlyCtx, deps: NightlyDeps = defaultDeps(), maxMs = 30 * 60_000) {
  const end = deps.now() + maxMs
  let told = false
  for (;;) {
    let s: { running?: { pid?: number; branch?: string } } | null
    try { s = JSON.parse(deps.read(ctx.queuePath)) } catch { return true }
    if (!s || typeof s !== 'object') return true   // 空文件、null、写坏的：当队列空闲
    const running = s.running
    if (!running || typeof running !== 'object' || typeof running.pid !== 'number' || running.pid <= 0 || !deps.pidAlive(running.pid)) return true
    if (deps.now() >= end) {
      console.log(`  合并队列还在跑（${running.branch ?? '不明'}），等了 ${Math.round(maxMs / 60_000)} 分钟，先开跑（名额锁会排队）`)
      return false
    }
    if (!told) {
      console.log(`  合并队列在跑 ${running.branch ?? ''}，等它跑完再开（最多 ${Math.round(maxMs / 60_000)} 分钟）`)
      told = true
    }
    await deps.sleep(5000)
  }
}

export type NightlyRun = { record: NightlyRecord; wrote: boolean; waited: boolean }

export const nightlyLockPath = (ctx: NightlyCtx) => join(ctx.common, 'sumika-nightly.lock')

// 全机只有一个整套：和合并队列的执行者锁同一套写法；已有活着的整套返回它的 pid
export function acquireNightly(ctx: NightlyCtx): { release: () => void } | { pid: number } {
  const path = nightlyLockPath(ctx)
  for (let i = 0; i < 5; i++) {
    const rel = lockFile(path, 0, Number.MAX_SAFE_INTEGER)
    if (rel) return { release: rel }
    try { return { pid: JSON.parse(readFileSync(path, 'utf8')).pid } } catch { /* 锁刚被释放或还没写完：再试 */ }
    Bun.sleepSync(50)
  }
  return { pid: 0 }
}

export async function runNightly(ctx: NightlyCtx = makeNightlyCtx(), deps: NightlyDeps = defaultDeps(), o: { maxQueueWaitMs?: number } = {}): Promise<NightlyRun> {
  const got = acquireNightly(ctx)
  if (!('release' in got)) throw new Error(`已经有一个整套在跑（pid ${got.pid || '不明'}），这个不起了`)
  try {
    return await runNightlyLocked(ctx, deps, o)
  } finally {
    got.release()
  }
}

// 任何一步抛错（等队列、准备 worktree、写输出）都落一条红记录再交回，不在没有结论的情况下退出
async function runNightlyLocked(ctx: NightlyCtx, deps: NightlyDeps, o: { maxQueueWaitMs?: number }): Promise<NightlyRun> {
  const started = deps.now()
  const output = join(ctx.outDir, `nightly-${localStamp(started)}.txt`)
  let waited = false
  try {
    waited = await waitQueueIdle(ctx, deps, o.maxQueueWaitMs ?? 30 * 60_000)
    const { commit, tree } = prepWorktree(ctx, deps)
    deps.mkdir(ctx.outDir)
    deps.write(output, `整套：${ctx.main} ${commit.slice(0, 8)}，${new Date(started).toLocaleString('zh-CN', { hour12: false })}\n命令：bun scripts/go-verify.ts --full --by nightly\n\n`)
    let text = ''
    const onData = (t: string) => { text += t; deps.append(output, t) }
    let code: number | null = null
    let timedOut = false
    try {
      const r = await deps.runVerify({ cwd: ctx.wt, script: join(ctx.wt, 'scripts', 'go-verify.ts'), args: ['--full', '--by', 'nightly'], env: { ...process.env, SUMIKA_TEST_LANE: 'verify' }, onData, timeoutMs: NIGHTLY_MAX_RUN_MS })
      code = r.code
      timedOut = !!r.timedOut
    } catch (e) {
      onData(`\n起不来：${(e as Error).message}\n`)
    }
    const seconds = Math.max(0, deps.now() - started) / 1000
    onData(`\n退出码 ${code ?? '起不来'}${timedOut ? '（超过 90 分钟，已停掉）' : ''}，${seconds.toFixed(1)} 秒\n`)
    // 退出码 0 不够：go-verify 在刚检出的 main 上可能因为“没有 Go 改动”直接退 0，那种没跑过整套
    const evidence = ranEvidence(text)
    const ok = code === 0 && !timedOut && !!evidence
    const reason = timedOut ? '超过 90 分钟' : ok ? undefined : code === 0 && !evidence ? '没认出整套跑过的证据' : undefined
    if (reason) onData(`原因：${reason}\n`)
    const record: NightlyRecord = {
      at: new Date(deps.now()).toISOString(),
      commit,
      tree,
      ok,
      seconds: Math.round(seconds),
      exit: code ?? -1,
      output,
      failed: failedPkgs(text).slice(0, 20),
      ...(evidence ? { ran: evidence } : {}),
      ...(reason ? { reason } : {}),
    }
    return { record, wrote: await addNightly(record, ctx.recordPath, deps), waited }
  } catch (e) {
    const message = (e as Error).message
    const nowMs = (() => { try { return deps.now() } catch { return Date.now() } })()
    const seconds = Math.max(0, nowMs - started) / 1000
    const text = `整套没跑成：${message}\n`
    try { deps.mkdir(ctx.outDir); deps.append(output, text) } catch { /* 输出写不进也照记结论 */ }
    const record: NightlyRecord = {
      at: new Date(nowMs).toISOString(),
      commit: '',
      tree: '',
      ok: false,
      seconds: Math.round(seconds),
      exit: -1,
      output,
      failed: [],
      reason: `整套没跑成：${message}`,
    }
    return { record, wrote: await addNightly(record, ctx.recordPath, deps), waited }
  }
}

const psQuote = (s: string) => `'${s.replaceAll("'", "''")}'`

// 登记里的 bun 不用 process.execPath：那是带版本号的目录（...\apps\bun\1.4.4\bun.exe），scoop cleanup 后任务会无声失败。
// 先认 scoop 的 current（跟着升级走），再认 PATH 上的 bun；都没有才退回当前进程（测试里可以换掉）
export function stableBun(o: { home?: string; exists?: (p: string) => boolean; which?: () => string | null; fallback?: string } = {}) {
  const home = o.home ?? process.env.USERPROFILE ?? process.env.HOME ?? ''
  const exists = o.exists ?? existsSync
  if (home) {
    const current = join(home, 'scoop', 'apps', 'bun', 'current', 'bun.exe')
    if (exists(current)) return current
  }
  const w = (o.which ?? (() => Bun.which('bun')))()
  return w || o.fallback || process.execPath
}

// 登记命令只生成文本，测试只查文本；绝不在这里执行（--register 的 CLI 才执行）
export function registerScript(o: { repo: string; bun: string; at: string; taskName?: string }) {
  const task = o.taskName ?? NIGHTLY_TASK
  const script = join(o.repo, 'scripts', 'nightly.ts')
  return [
    "$ErrorActionPreference = 'Stop'",
    '# 仅在用户登录时运行（LogonType Interactive）：服务账户会和交互式队列抢 Local\\ 名额',
    `$action = New-ScheduledTaskAction -Execute ${psQuote(o.bun)} -Argument ${psQuote(`"${script}"`)} -WorkingDirectory ${psQuote(o.repo)}`,
    `$trigger = New-ScheduledTaskTrigger -Daily -At ${psQuote(o.at)}`,
    '$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -Priority 10',
    '$principal = New-ScheduledTaskPrincipal -UserId ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive',
    `Register-ScheduledTask -TaskName ${psQuote(task)} -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null`,
    // 登记失败不能当成功：查不到就抛错，PowerShell 以非 0 退出，CLI 跟着退非 0
    `if (-not (Get-ScheduledTask -TaskName ${psQuote(task)} -ErrorAction SilentlyContinue)) { throw ${psQuote(`登记后查不到任务 ${task}`)} }`,
    `Write-Output ${psQuote(`已登记 ${task}，每天 ${o.at}`)}`,
  ].join('\r\n')
}

export function unregisterScript(taskName = NIGHTLY_TASK) {
  return [
    "$ErrorActionPreference = 'Stop'",
    `Unregister-ScheduledTask -TaskName ${psQuote(taskName)} -Confirm:$false`,
    `if (Get-ScheduledTask -TaskName ${psQuote(taskName)} -ErrorAction SilentlyContinue) { throw ${psQuote(`注销后仍查得到任务 ${taskName}`)} }`,
    `Write-Output ${psQuote(`已注销 ${taskName}`)}`,
  ].join('\r\n')
}

export function statusScript(taskName = NIGHTLY_TASK) {
  const t = psQuote(taskName)
  return [
    "$ErrorActionPreference = 'Stop'",
    `$t = Get-ScheduledTask -TaskName ${t} -ErrorAction SilentlyContinue`,
    `if (-not $t) { Write-Output ${psQuote(`没有登记 ${taskName}（bun scripts/nightly.ts --register）`)}; exit 0 }`,
    `$i = Get-ScheduledTaskInfo -TaskName ${t}`,
    `if (-not $i) { throw ${psQuote(`查不到任务 ${taskName} 的运行记录`)} }`,
    'Write-Output ("任务：" + $t.TaskName + "　状态：" + $t.State)',
    'Write-Output ("下次：" + $i.NextRunTime)',
    'Write-Output ("上次：" + $i.LastRunTime + "　结果：" + $i.LastTaskResult)',
  ].join('\r\n')
}

export type NightlyAction = 'run' | 'register' | 'unregister' | 'status' | 'help'
export type NightlyArgs = { action: NightlyAction; at: string }

export const USAGE = [
  '用法：',
  '  bun scripts/nightly.ts                         跑一次整套',
  '  bun scripts/nightly.ts --register [--at HH:MM]  登记每天的任务（默认 04:30，仅登录时运行）',
  '  bun scripts/nightly.ts --unregister            注销任务',
  '  bun scripts/nightly.ts --status                只读：任务在不在、下次几点、上次结果',
  '  bun scripts/nightly.ts --help                  只打印这段用法',
].join('\n')

export function parseNightlyArgs(args: string[]): NightlyArgs {
  if (args.includes('--help')) {
    if (args.length > 1) throw new Error(`--help 不能和别的参数一起用\n${USAGE}`)
    return { action: 'help', at: DEFAULT_AT }
  }
  // 不认识的参数不能当成“跑一次”：少一个拼错的动作，夜里就会真跑整套
  const known: Record<string, true> = { '--register': true, '--unregister': true, '--status': true, '--at': true }
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!
    if (a === '--at') { i++; continue }
    if (!known[a]) throw new Error(`不认识的参数：${a}\n${USAGE}`)
  }
  const actions = (['--register', '--unregister', '--status'] as const).filter((f) => args.includes(f))
  if (actions.length > 1) throw new Error('--register、--unregister、--status 一次只能用一个')
  const action: NightlyAction = actions[0] === '--register' ? 'register' : actions[0] === '--unregister' ? 'unregister' : actions[0] === '--status' ? 'status' : 'run'
  const i = args.indexOf('--at')
  if (i >= 0 && action !== 'register') throw new Error('--at 只和 --register 一起用')
  const at = i >= 0 ? args[i + 1] : undefined
  if (i >= 0 && !at) throw new Error('--at 后面要跟时间，例如 --at 04:30')
  const time = at ?? DEFAULT_AT
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(time)) throw new Error(`--at 要写成 24 小时制的 HH:MM：${time}`)
  return { action, at: time }
}

if (import.meta.main) {
  try {
    const { action, at } = parseNightlyArgs(process.argv.slice(2))
    if (action === 'help') {
      console.log(USAGE)
      process.exit(0)
    }
    if (action === 'run') {
      const r = await runNightly()
      console.log(`${r.record.ok ? '绿' : '红'}：${r.record.commit.slice(0, 8) || '（没有提交）'}，${r.record.seconds} 秒，输出 ${r.record.output}`)
      if (r.record.failed.length) console.log(`没过的 ${r.record.failed.length} 个：${r.record.failed.join('、')}`)
      if (r.record.reason) console.log(`原因：${r.record.reason}`)
      if (!r.wrote) console.log('整套结论没记下（sumika-nightly.json）')
      process.exit(r.record.ok && r.wrote ? 0 : 1)
    }
    const ctx = makeNightlyCtx()
    const script = action === 'register' ? registerScript({ repo: ctx.mainDir, bun: stableBun(), at }) : action === 'unregister' ? unregisterScript() : statusScript()
    if (action !== 'status') console.log(script)
    const r = defaultDeps().runPowerShell(script)
    if (r.out) process.stdout.write(r.out.endsWith('\n') ? r.out : r.out + '\n')
    process.exit(r.code === 0 ? 0 : 1)
  } catch (e) {
    console.log(`出错了：${(e as Error).message}`)
    process.exit(2)
  }
}
