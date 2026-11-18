import { afterAll, expect, test } from 'bun:test'
import { appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { acquireNightly, addNightly, defaultDeps, failedPkgs, NIGHTLY_MAX_RUN_MS, parseNightlyArgs, prepWorktree, ranEvidence, registerScript, runNightly, stableBun, statusScript, unregisterScript, waitQueueIdle, type NightlyCtx, type NightlyDeps, type NightlyRecord } from './nightly'
import { loadNightly } from './nightly-record'

// 全部在临时目录里：不起 git（prepWorktree 那条除外，用临时仓库）、不碰系统任务计划，go-verify 用假的
const dirs: string[] = []
const tmp = () => { const d = mkdtempSync(join(tmpdir(), 'nightly-test-')); dirs.push(d); return d }
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }) })

const COMMIT = 'a'.repeat(40)
const TREE = 'b'.repeat(40)

// go-verify 实际打印的两句结论（scripts/go-verify.ts 的 wrapUp；测试用真句子，不按意思猜）
const FULL_EVIDENCE = '合并说明可以写：go vet 整个模块退出码 0（1 秒），go test -count=1 3 个包退出码 0（2 秒）；挑包：整套\n'
const REUSED_EVIDENCE = '都验过了。合并说明可以写：go vet、go test -count=1 128 个包都过（nightly 的记录；挑包：整套）\n'

function ctxFor(d: string): NightlyCtx {
  const common = join(d, 'common')
  mkdirSync(common, { recursive: true })
  return {
    main: 'main',
    common,
    mainDir: join(d, 'main'),
    wt: join(d, 'main', '.claude', 'worktrees', 'nightly'),
    outDir: join(d, 'out'),
    recordPath: join(common, 'sumika-nightly.json'),
    queuePath: join(common, 'sumika-merge-queue.json'),
  }
}

// 假时钟：runVerify 推进 120 秒，测 seconds；记录文件和真正的临时目录打交道，改名重试也被测到
function fakeDeps(over: Partial<NightlyDeps> = {}) {
  const clock = { now: Date.parse('2026-11-16T20:00:00Z') }
  const deps: NightlyDeps = {
    now: () => clock.now,
    sleep: async (ms) => { clock.now += ms },
    read: (p) => readFileSync(p, 'utf8'),
    write: (p, body) => writeFileSync(p, body),
    append: (p, body) => appendFileSync(p, body),
    rename: (from, to) => renameSync(from, to),
    remove: (p) => rmSync(p, { force: true }),
    exists: (p) => existsSync(p),
    mkdir: (p) => { mkdirSync(p, { recursive: true }) },
    copyDir: (from, to) => { cpSync(from, to, { recursive: true }) },
    git: (_cwd, args) => {
      if (args[0] === 'rev-parse' && args[1] === 'HEAD') return { code: 0, out: COMMIT, err: '' }
      if (args[0] === 'rev-parse' && args[1] === 'HEAD^{tree}') return { code: 0, out: TREE, err: '' }
      return { code: 0, out: '', err: '' }
    },
    runVerify: async ({ onData }) => {
      onData('ok  \tsumika-native/internal/a\t0.1s\n' + FULL_EVIDENCE)
      clock.now += 120_000
      return { code: 0 }
    },
    runPowerShell: () => ({ code: 0, out: '' }),
    pidAlive: () => false,
    ...over,
  }
  return { deps, clock }
}

const rec = (over: Partial<NightlyRecord> = {}): NightlyRecord => ({
  at: '2026-01-01T00:00:00Z', commit: 'c', tree: 't', ok: true, seconds: 1, exit: 0, output: 'o', failed: [],
  ...over,
})

test('绿：记下提交、树、秒数，ran=full，参数是 --full --by nightly，验证档名额', async () => {
  const c = ctxFor(tmp())
  const { deps, clock } = fakeDeps({
    runVerify: async (a) => {
      expect(a.args).toEqual(['--full', '--by', 'nightly'])
      expect(a.env.SUMIKA_TEST_LANE).toBe('verify')
      expect(a.timeoutMs).toBe(NIGHTLY_MAX_RUN_MS)
      a.onData(FULL_EVIDENCE)
      clock.now += 120_000
      return { code: 0 }
    },
  })
  const r = await runNightly(c, deps)
  expect(r.record.ok).toBe(true)
  expect(r.record.ran).toBe('full')
  expect(r.record.reason).toBeUndefined()
  expect(r.record.commit).toBe(COMMIT)
  expect(r.record.tree).toBe(TREE)
  expect(r.record.seconds).toBe(120)
  expect(r.record.exit).toBe(0)
  expect(r.record.output).toMatch(/nightly-\d{8}-\d{6}\.txt$/)
  expect(readFileSync(r.record.output, 'utf8')).toContain('go test -count=1 3 个包')
  expect(loadNightly(c.recordPath, deps.read)).toEqual([r.record])
})

test('同一棵树第二晚：认出“都验过了”也算整套跑过，ran=reused', async () => {
  const c = ctxFor(tmp())
  const { deps } = fakeDeps({ runVerify: async ({ onData }) => { onData(REUSED_EVIDENCE); return { code: 0 } } })
  const r = await runNightly(c, deps)
  expect(r.record.ok).toBe(true)
  expect(r.record.ran).toBe('reused')
})

test('退出码 0 但没认出整套证据：记红，哪怕 go-verify 正常退出', async () => {
  const c = ctxFor(tmp())
  const { deps } = fakeDeps({
    runVerify: async ({ onData }) => {
      onData('合并说明可以写：没有 Go 改动，跳过 Go 的 vet、test、archcheck\n')
      return { code: 0 }
    },
  })
  const r = await runNightly(c, deps)
  expect(r.record.ok).toBe(false)
  expect(r.record.exit).toBe(0)
  expect(r.record.ran).toBeUndefined()
  expect(r.record.reason).toBe('没认出整套跑过的证据')
  expect(readFileSync(r.record.output, 'utf8')).toContain('没认出整套跑过的证据')
})

test('90 分钟总时限：记红，原因写超过 90 分钟', async () => {
  const c = ctxFor(tmp())
  const { deps } = fakeDeps({ runVerify: async () => ({ code: 1, timedOut: true }) })
  const r = await runNightly(c, deps)
  expect(r.record.ok).toBe(false)
  expect(r.record.reason).toBe('超过 90 分钟')
  expect(readFileSync(r.record.output, 'utf8')).toContain('超过 90 分钟')
})

test('红：退出码和没过的包写对，最多留 20 个', async () => {
  const c = ctxFor(tmp())
  const failed = Array.from({ length: 25 }, (_, i) => `FAIL\tsumika-native/internal/p${String(i).padStart(2, '0')}\t0.1s`)
  const { deps } = fakeDeps({
    runVerify: async ({ onData }) => {
      onData(`--- FAIL: TestX (0.1s)\n${failed.join('\n')}\n`)
      return { code: 1 }
    },
  })
  const r = await runNightly(c, deps)
  expect(r.record.ok).toBe(false)
  expect(r.record.exit).toBe(1)
  expect(r.record.failed).toHaveLength(20)
  expect(r.record.failed[0]).toBe('sumika-native/internal/p00')
  expect(r.record.failed[19]).toBe('sumika-native/internal/p19')
  expect(readFileSync(r.record.output, 'utf8')).toContain('--- FAIL: TestX')
})

test('go-verify 起不来：也写一条红的记录，输出里留原因', async () => {
  const c = ctxFor(tmp())
  const { deps } = fakeDeps({ runVerify: async () => { throw new Error('bun 找不到') } })
  const r = await runNightly(c, deps)
  expect(r.record.ok).toBe(false)
  expect(r.record.exit).toBe(-1)
  expect(r.record.failed).toEqual([])
  expect(readFileSync(r.record.output, 'utf8')).toContain('起不来：bun 找不到')
  expect(loadNightly(c.recordPath, deps.read)[0]!.ok).toBe(false)
})

test('准备 worktree 抛错：外层兜底写红记录，不碰 go-verify', async () => {
  const c = ctxFor(tmp())
  let verified = false
  const { deps } = fakeDeps({
    exists: () => false,
    git: (_cwd, args) => args[0] === 'worktree' && args[1] === 'add' ? { code: 1, out: '', err: '磁盘满了' } : { code: 0, out: '', err: '' },
    runVerify: async () => { verified = true; return { code: 0 } },
  })
  const r = await runNightly(c, deps)
  expect(verified).toBe(false)
  expect(r.record.ok).toBe(false)
  expect(r.record.exit).toBe(-1)
  expect(r.record.reason).toContain('磁盘满了')
  expect(readFileSync(r.record.output, 'utf8')).toContain('整套没跑成')
  expect(loadNightly(c.recordPath, deps.read)[0]!.reason).toContain('磁盘满了')
})

test('已经有一个整套在跑：报错退出，不碰 worktree', async () => {
  const c = ctxFor(tmp())
  const got = acquireNightly(c)
  expect('release' in got).toBe(true)
  let touched = false
  const { deps } = fakeDeps({ git: () => { touched = true; return { code: 0, out: COMMIT, err: '' } } })
  await expect(runNightly(c, deps)).rejects.toThrow('已经有一个整套在跑')
  expect(touched).toBe(false)
  if ('release' in got) got.release()
})

test('只留最近 30 条：旧记录从前面挤掉', async () => {
  const d = tmp()
  const path = join(d, 'sumika-nightly.json')
  const old = Array.from({ length: 35 }, (_, i) => rec({ at: new Date(Date.parse('2026-01-01T00:00:00Z') + i * 1000).toISOString(), commit: `old${i}` }))
  writeFileSync(path, JSON.stringify(old))
  const { deps } = fakeDeps()
  expect(await addNightly(rec({ commit: 'new', at: '2026-02-01T00:00:00Z' }), path, deps)).toBe(true)
  const all = loadNightly(path, deps.read)
  expect(all).toHaveLength(30)
  expect(all[0]!.commit).toBe('old6')
  expect(all[29]!.commit).toBe('new')
})

test('写结论时目标文件被占：隔一会儿重试改名', async () => {
  const d = tmp()
  const path = join(d, 'sumika-nightly.json')
  let tries = 0
  const { deps } = fakeDeps({
    rename: (from, to) => {
      tries++
      if (tries <= 2) throw Object.assign(new Error('EPERM'), { code: 'EPERM' })
      renameSync(from, to)
    },
  })
  expect(await addNightly(rec(), path, deps)).toBe(true)
  expect(tries).toBe(3)
  expect(loadNightly(path, deps.read)).toHaveLength(1)
})

test('直写也失败：addNightly 返回 false，不抛出', async () => {
  const d = tmp()
  const path = join(d, 'sumika-nightly.json')
  const { deps } = fakeDeps({
    write: (p, body) => { if (p === path) throw new Error('没空间'); writeFileSync(p, body) },
    rename: () => { throw Object.assign(new Error('EPERM'), { code: 'EPERM' }) },
  })
  expect(await addNightly(rec(), path, deps)).toBe(false)
})

test('结论文件：坏条目跳过，commit 不是字符串、at 解析不了的不算', () => {
  const at = '2026-11-16T20:00:00Z'
  const body = JSON.stringify([
    null, 5, 's',
    { at: '坏', ok: true, commit: 'x' },
    { at, ok: true, commit: 123 },
    { at, ok: 'yes', commit: 'x' },
    { at, ok: true, commit: 'ok' },
  ])
  expect(loadNightly('x', () => body).map((r) => r.commit)).toEqual(['ok'])
})

// 阈值边界、未来绿记录、旧的绿加新的红在 merge-queue.test.ts（提醒属于队列显示）

test('从 go test 输出里认出 FAIL 的包，失败行只取包名', () => {
  const out = 'ok  \tm/a\t0.1s\nFAIL\tm/b\t1.0s\nFAIL\tm/c [build failed]\n--- FAIL: TestX\n'
  expect(failedPkgs(out)).toEqual(['m/b', 'm/c'])
})

test('认证据只认整套的两句真结论，带 -short 的、没 Go 改动的都不算', () => {
  expect(ranEvidence(`Go 验证：aaaaaaaa，整套的 3 个包（共 3 个）\n${FULL_EVIDENCE}`)).toBe('full')
  expect(ranEvidence(REUSED_EVIDENCE)).toBe('reused')
  expect(ranEvidence('合并说明可以写：没有 Go 改动，跳过 Go 的 vet、test、archcheck\n')).toBeUndefined()
  expect(ranEvidence('合并说明可以写：go vet 整个模块退出码 0（1 秒），go test -short -count=1 3 个包退出码 0（2 秒）；挑包：整套')).toBeUndefined()
  expect(ranEvidence('都验过了。合并说明可以写：go vet、go test -short -count=1 3 个包都过（nightly 的记录；挑包：整套）')).toBeUndefined()
})

test('等队列：没有状态文件、null、坏 pid 都当空闲', async () => {
  const c = ctxFor(tmp())
  const { deps } = fakeDeps()
  expect(await waitQueueIdle(c, deps)).toBe(true)
  writeFileSync(c.queuePath, 'null')
  expect(await waitQueueIdle(c, deps)).toBe(true)
  writeFileSync(c.queuePath, '{"running":{"pid":0}}')
  expect(await waitQueueIdle(c, deps)).toBe(true)
})

test('等队列：有执行者在跑就等，跑完再开', async () => {
  const c = ctxFor(tmp())
  writeFileSync(c.queuePath, JSON.stringify({ running: { pid: 123, branch: 'x' } }))
  let alive = true
  const f = fakeDeps({ pidAlive: () => alive, sleep: async (ms) => { f.clock.now += ms; alive = false } })
  const before = f.clock.now
  expect(await waitQueueIdle(c, f.deps, 60_000)).toBe(true)
  expect(f.clock.now - before).toBe(5000)
})

test('等队列：超过最长等待就先开跑', async () => {
  const c = ctxFor(tmp())
  writeFileSync(c.queuePath, JSON.stringify({ running: { pid: 123, branch: 'x' } }))
  const { deps, clock } = fakeDeps({ pidAlive: () => true })
  const before = clock.now
  expect(await waitQueueIdle(c, deps, 10_000)).toBe(false)
  expect(clock.now - before).toBe(10_000)
})

test('登记命令：Stop、Principal/Settings、登记后查得到才打印已登记，路径是主工作区不是 cwd', () => {
  const s = registerScript({ repo: 'D:\\仓库\\sumika', bun: 'C:\\bun\\bun.exe', at: '03:15' })
  expect(s.split(/\r?\n/)[0]).toBe("$ErrorActionPreference = 'Stop'")
  expect(s).toContain('仅在用户登录时运行')
  expect(s).toContain('-LogonType Interactive')
  expect(s).toContain('-StartWhenAvailable')
  expect(s).toContain("-Daily -At '03:15'")
  expect(s).toContain('-Principal $principal')
  expect(s).toContain('-Settings $settings')
  expect(s).not.toContain('SYSTEM')
  expect(s).not.toContain('RunLevel')
  expect(s).toContain('-Priority 10')
  expect(s).toContain("-WorkingDirectory 'D:\\仓库\\sumika'")
  expect(s).toContain('D:\\仓库\\sumika\\scripts\\nightly.ts')
  expect(s).not.toContain(join(process.cwd(), 'scripts', 'nightly.ts'))
  expect(s.indexOf('Register-ScheduledTask')).toBeLessThan(s.indexOf('Get-ScheduledTask'))
  expect(s.indexOf('Get-ScheduledTask')).toBeLessThan(s.indexOf('已登记'))
})

test('注销和只读状态：Stop、注销后确认查不到、状态查运行记录', () => {
  expect(unregisterScript().split(/\r?\n/)[0]).toBe("$ErrorActionPreference = 'Stop'")
  expect(unregisterScript()).toContain("Unregister-ScheduledTask -TaskName 'SumikaNightly' -Confirm:$false")
  expect(unregisterScript()).toContain('Get-ScheduledTask -TaskName')
  expect(unregisterScript().indexOf('Unregister-ScheduledTask')).toBeLessThan(unregisterScript().indexOf('Get-ScheduledTask'))
  expect(statusScript().split(/\r?\n/)[0]).toBe("$ErrorActionPreference = 'Stop'")
  expect(statusScript()).toContain('Get-ScheduledTaskInfo')
  expect(statusScript()).toContain('没有登记')
})

test('登记用的 bun 走 scoop current 或 PATH，不带版本号目录', () => {
  const home = tmp()
  const current = join(home, 'scoop', 'apps', 'bun', 'current', 'bun.exe')
  expect(stableBun({ home, exists: (p) => p === current })).toBe(current)
  expect(stableBun({ home: join(home, '无'), exists: () => false, which: () => 'C:\\shims\\bun.exe' })).toBe('C:\\shims\\bun.exe')
  const s = registerScript({ repo: 'D:\\仓库\\sumika', bun: stableBun({ home, exists: () => true }), at: '04:30' })
  expect(s).not.toMatch(/\\bun\\\d+\.\d+\.\d+/)
})

test('参数：默认 04:30，--at 校验，动作互斥，未知参数和 --help 不当成跑一次', () => {
  expect(parseNightlyArgs([])).toEqual({ action: 'run', at: '04:30' })
  expect(parseNightlyArgs(['--register'])).toEqual({ action: 'register', at: '04:30' })
  expect(parseNightlyArgs(['--register', '--at', '05:10'])).toEqual({ action: 'register', at: '05:10' })
  expect(parseNightlyArgs(['--unregister']).action).toBe('unregister')
  expect(parseNightlyArgs(['--status']).action).toBe('status')
  expect(parseNightlyArgs(['--help'])).toEqual({ action: 'help', at: '04:30' })
  expect(() => parseNightlyArgs(['--register', '--at', '25:00'])).toThrow()
  expect(() => parseNightlyArgs(['--register', '--at'])).toThrow()
  expect(() => parseNightlyArgs(['--register', '--unregister'])).toThrow()
  expect(() => parseNightlyArgs(['--status', '--at', '04:30'])).toThrow()
  expect(() => parseNightlyArgs(['--run'])).toThrow(/用法/)
  expect(() => parseNightlyArgs(['--help', '--status'])).toThrow(/用法/)
})

// 真的建一个临时 git 仓库：main 上两次提交，worktree 里留脏文件，准备后必须在 main 最新且干净
test('prepWorktree：真的临时仓库，脏 worktree 回到 main 最新、干净', () => {
  const d = tmp()
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/^GIT_/.test(k)))
  const g = (...a: string[]) => {
    const p = Bun.spawnSync(['git', '-c', 'user.name=t', '-c', 'user.email=t@example.invalid', ...a], { cwd: d, env, stdout: 'pipe', stderr: 'pipe' })
    if (p.exitCode !== 0) throw new Error(`git ${a.join(' ')}：${p.stderr.toString()}`)
    return p.stdout.toString().trim()
  }
  g('init', '-q', '-b', 'main')
  writeFileSync(join(d, 'a.txt'), 'one\n')
  g('add', '-A'); g('commit', '-q', '-m', '一')
  writeFileSync(join(d, 'b.txt'), 'two\n')
  g('add', '-A'); g('commit', '-q', '-m', '二')
  const head = g('rev-parse', 'HEAD')
  const c: NightlyCtx = {
    main: 'main', common: join(d, '.git'), mainDir: d,
    wt: join(d, '.claude', 'worktrees', 'nightly'),
    outDir: join(d, 'out'), recordPath: join(d, 'record.json'), queuePath: join(d, 'queue.json'),
  }
  const first = prepWorktree(c, defaultDeps())
  expect(first.commit).toBe(head)
  writeFileSync(join(c.wt, 'dirty.txt'), '脏\n')
  writeFileSync(join(c.wt, 'a.txt'), '改坏\n')
  const second = prepWorktree(c, defaultDeps())
  expect(second.commit).toBe(head)
  const status = Bun.spawnSync(['git', 'status', '--porcelain'], { cwd: c.wt, env, stdout: 'pipe', stderr: 'pipe' })
  expect(status.stdout.toString().trim()).toBe('')
  expect(readFileSync(join(c.wt, 'a.txt'), 'utf8').trim()).toBe('one')
}, 60_000)
