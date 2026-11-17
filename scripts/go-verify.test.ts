import { afterAll, expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { addVerified, goTree, loadVerified, needsMygoVet, overBudget, parseVerifyArgs, passedPkgs, RUNNERS, splitSlow, uncovered, verify, verifyMygo, WATCH, type Verified } from './go-verify'
import { EXAMPLE, FULL, pick } from './go-affected'

// 示例配置不在整套名单里（只测读它的包），但改了它旧的绿记录不能再算：指纹必须照旧包含它（2026-11-13 复核建议）
test('示例配置移出整套名单后仍在指纹范围里', () => {
  expect(FULL.some((re) => re.test(EXAMPLE))).toBe(false)
  expect(WATCH).toContain(EXAMPLE)
})

test('--by：写进验证记录的名字，缺省 go-verify，基准分支不受影响', () => {
  expect(parseVerifyArgs([])).toEqual({ full: false, base: 'main', by: 'go-verify' })
  expect(parseVerifyArgs(['--full'])).toEqual({ full: true, base: 'main', by: 'go-verify' })
  expect(parseVerifyArgs(['--full', '--by', 'nightly'])).toEqual({ full: true, base: 'main', by: 'nightly' })
  expect(parseVerifyArgs(['--by=nightly', 'feature'])).toEqual({ full: false, base: 'feature', by: 'nightly' })
  expect(() => parseVerifyArgs(['--by'])).toThrow()
})

// 编的记录：树、go 指纹都是假的
const rec = (tree: string, pkgs: string[], go = 'go1|windows|amd64||0|'): Verified => ({ tree, commit: 'c', pkgs, go, at: '2026-11-01T00:00:00Z', seconds: 1, by: 'test' })
const P = ['m/a', 'm/b', 'm/c']

test('同一棵树、同一套 go 验过全部：一个都不用跑', () => expect(uncovered([rec('t1', P)], 't1', 'go1|windows|amd64||0|', P)).toEqual([]))
test('树不一样：全部要跑', () => expect(uncovered([rec('t1', P)], 't2', 'go1|windows|amd64||0|', P)).toEqual(P))
test('go 不一样：全部要跑', () => expect(uncovered([rec('t1', P)], 't1', 'go2|windows|amd64||0|', P)).toEqual(P))
test('只验过一部分：跑剩下的', () => expect(uncovered([rec('t1', ['m/a'])], 't1', 'go1|windows|amd64||0|', P)).toEqual(['m/b', 'm/c']))
test('几条记录合起来算', () => expect(uncovered([rec('t1', ['m/a']), rec('t1', ['m/b', 'm/c'])], 't1', 'go1|windows|amd64||0|', P)).toEqual([]))
test('别的树上验过的包不算', () => expect(uncovered([rec('t2', ['m/a']), rec('t1', ['m/b'])], 't1', 'go1|windows|amd64||0|', P)).toEqual(['m/a', 'm/c']))
test('没有记录：全部要跑', () => expect(uncovered([], 't1', 'go1|windows|amd64||0|', P)).toEqual(P))

// 每次新建一个唯一的临时目录（不写固定路径），跑完删掉
const dirs: string[] = []
const tmp = () => { const d = mkdtempSync(join(tmpdir(), 'go-verify-')); dirs.push(d); return join(d, 'verified.json') }
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })

test('写进去读得回来，只留最近 200 条', () => {
  const f = tmp()
  for (let i = 0; i < 205; i++) addVerified(rec(`t${i}`, ['m/a']), f)
  const all = loadVerified(f)
  expect(all.length).toBe(200)
  expect(all[0].tree).toBe('t5')
  expect(all[199].tree).toBe('t204')
})

test('文件没有或写坏了当成没验过，只会多跑', () => {
  const f = tmp()
  expect(loadVerified(f)).toEqual([])
  writeFileSync(f, '{半份')
  expect(loadVerified(f)).toEqual([])
  writeFileSync(f, JSON.stringify([{ tree: 't1' }, rec('t2', ['m/a'])]))
  expect(loadVerified(f).map((r) => r.tree)).toEqual(['t2'])   // 缺字段的那条不认
})

test('从 go test 的输出认出过了的包：ok 和没有测试文件的算，FAIL 和不在这次名单里的不算', () => {
  const out = [
    'ok  \tm/a\t1.2s',
    '--- FAIL: TestX (0.1s)',
    'FAIL\tm/b\t3.0s',
    '?   \tm/c\t[no test files]',
    'ok  \tm/zzz\t0.1s',
    'ok  \tm/d\t0.5s [no tests to run]',
    'panic: test timed out after 10m0s',
  ].join('\r\n')
  expect(passedPkgs(out, ['m/a', 'm/b', 'm/c', 'm/d', 'm/e'])).toEqual(['m/a', 'm/c', 'm/d'])
})

// -short 跑的记录（ADR-225）：平常认，整套不认；没写 short 的旧记录当整套跑过
test('-short 的记录平常认、整套不认', () => {
  const short = { ...rec('t1', P), short: true }
  expect(uncovered([short], 't1', 'go1|windows|amd64||0|', P)).toEqual([])
  expect(uncovered([short], 't1', 'go1|windows|amd64||0|', P, true)).toEqual(P)
  expect(uncovered([rec('t1', P)], 't1', 'go1|windows|amd64||0|', P, true)).toEqual([])
})

test('原生界面实现与依赖触发 mygo vet，内部包和测试文件不触发', () => {
  expect(needsMygoVet(['native/ui.go'])).toBe(true)
  expect(needsMygoVet(['native/automation_e2e.go'])).toBe(true)
  expect(needsMygoVet(['native/go.mod'])).toBe(true)
  expect(needsMygoVet(['native/go.sum'])).toBe(true)
  expect(needsMygoVet(['native/ui_test.go', 'native/internal/app/app.go', 'scripts/go-verify.ts', 'native/README.md'])).toBe(false)
})

test('最慢的几个包单独跑，别的包不受影响', () => {
  const { slow, rest } = splitSlow(['sumika-native', 'sumika-native/internal/app', 'sumika-native/internal/turn', 'sumika-native/internal/llm', 'sumika-native/internal/turn/turntest', 'sumika-native/cmd/sumika-ctl'])
  expect(slow).toEqual(['sumika-native', 'sumika-native/internal/app'])
  expect(rest).toEqual(['sumika-native/internal/turn', 'sumika-native/internal/llm', 'sumika-native/internal/turn/turntest', 'sumika-native/cmd/sumika-ctl'])
})

test('超过一分钟的包会被挑出来提醒', () => {
  const out = 'ok  \tsumika-native/internal/a\t12.3s\nok  \tsumika-native/internal/b\t61.0s\nFAIL\tsumika-native/internal/c\t99.0s\n--- FAIL: x (70.0s)\nok  \tsumika-native/internal/d\t(cached)\n'
  expect(overBudget(out)).toEqual(['sumika-native/internal/b 61 秒'])
})

// 起 git 一律传清理过的 env（钩子里带着 GIT_DIR，会落到真仓库上）
const noGitEnv = () => Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')))
test('verifyMygo：同一份 native/ 验过就不再跑；native/ 改了、SUMIKA_PUSH_FULL=1 都照跑', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mygo-reuse-'))
  const back = process.cwd()
  try {
    const env = noGitEnv()
    const g = (...a: string[]) => Bun.spawnSync(['git', ...a], { cwd: dir, env, stdout: 'pipe', stderr: 'pipe' })
    g('init', '-q'); g('config', 'user.email', 't@example.invalid'); g('config', 'user.name', 't')
    mkdirSync(join(dir, 'native'), { recursive: true })
    writeFileSync(join(dir, 'native', 'ui.go'), 'package main\n')
    g('add', '-A'); g('commit', '-q', '-m', '一')
    const rec = join(dir, 'records.json')
    let runs = 0
    const run = () => { runs++; return { ok: true, seconds: 1, out: '' } }
    process.chdir(dir)
    const files = ['native/ui.go']
    verifyMygo(files, env, { commit: 'HEAD', records: rec, run }); expect(runs).toBe(1)
    verifyMygo(files, env, { commit: 'HEAD', records: rec, run }); expect(runs).toBe(1)          // 同一份代码：认记录
    verifyMygo(files, { ...env, SUMIKA_PUSH_FULL: '1' }, { commit: 'HEAD', records: rec, run }); expect(runs).toBe(2)
    verifyMygo(files, env, { records: rec, run }); expect(runs).toBe(3)                           // 没给提交：不认
    writeFileSync(join(dir, 'native', 'ui.go'), 'package main\n\nvar x = 1\n'); g('commit', '-qam', '改')
    verifyMygo(files, env, { commit: 'HEAD', records: rec, run }); expect(runs).toBe(4)          // native/ 变了
    const red = () => { runs++; return { ok: false, seconds: 1, out: '' } }
    writeFileSync(join(dir, 'native', 'ui.go'), 'package main\n\nvar x = 2\n'); g('commit', '-qam', '再改')
    expect(verifyMygo(files, env, { commit: 'HEAD', records: rec, run: red })!.ok).toBe(false)
    verifyMygo(files, env, { commit: 'HEAD', records: rec, run }); expect(runs).toBe(6)          // 红的不记，下次照跑
    expect(verifyMygo(['docs/a.md'], env, { commit: 'HEAD', records: rec, run })).toBeUndefined() // 根包没改不跑
  } finally { process.chdir(back); rmSync(dir, { recursive: true, force: true }) }
}, 60_000)

test('verify 把调用方给的名字（--by nightly）写进验证记录', () => {
  const dir = mkdtempSync(join(tmpdir(), 'verify-by-'))
  dirs.push(dir)
  const back = process.cwd()
  const savedGit = Object.fromEntries(Object.entries(process.env).filter(([k]) => k.startsWith('GIT_')))
  try {
    const env = noGitEnv()
    const g = (...a: string[]) => {
      const p = Bun.spawnSync(['git', '-c', 'user.name=t', '-c', 'user.email=t@example.invalid', ...a], { cwd: dir, env, stdout: 'pipe', stderr: 'pipe' })
      if (p.exitCode !== 0) throw new Error(`git ${a.join(' ')}：${p.stderr.toString()}`)
      return p.stdout.toString().trim()
    }
    g('init', '-q', '-b', 'main')
    mkdirSync(join(dir, 'native'), { recursive: true })
    writeFileSync(join(dir, 'native', 'a.go'), 'package a\n')
    g('add', '-A'); g('commit', '-q', '-m', '一')
    // worktreeIs 用继承的 env：钩子里的 GIT_DIR 会指回真仓库，先摘掉
    for (const k of Object.keys(process.env)) if (k.startsWith('GIT_')) delete process.env[k]
    process.chdir(dir)
    const tree = goTree('HEAD', env)
    let recorded: Verified | undefined
    const r = verify(['m/a'], tree, 'HEAD', 'nightly', env, false, { ok: true, seconds: 0, out: '' }, {
      test: { ok: true, seconds: 1, out: 'ok  \tm/a\t0.1s\n' },
      add: (x) => { recorded = x; return true },
    })
    expect(r.ok).toBe(true)
    expect(recorded?.by).toBe('nightly')
    expect(recorded?.pkgs).toEqual(['m/a'])
    expect(recorded?.short).toBeUndefined()
  } finally {
    process.chdir(back)
    for (const k of Object.keys(process.env)) if (k.startsWith('GIT_')) delete process.env[k]
    Object.assign(process.env, savedGit)
  }
})

// 执行测试的脚本也算进指纹：改了它们，同一份 Go 代码上的旧记录不再认；而且 go-affected 会把它们判成整套（两边口径一致）
// 名单写死一份：有人从 RUNNERS 删掉一项，下面的循环跟着变短，测试照样绿；这里直接对比
test('RUNNERS 就是这几个；FULL 里的每个 scripts/ 文件都在 RUNNERS 里', () => {
  expect([...RUNNERS].sort()).toEqual(['scripts/gen-rpc.ts', 'scripts/go-affected.ts', 'scripts/go-shard.cmd', 'scripts/go-shard.ts', 'scripts/go-verify.ts', 'scripts/merge-queue.ts', 'scripts/nightly.ts', 'scripts/pre-push.ts', 'scripts/run-low.ps1', 'scripts/server-verify.ts'])
  const scripts = readdirSync(join(import.meta.dir)).map((f) => `scripts/${f}`)
  const inFull = scripts.filter((f) => FULL.some((re) => re.test(f)))
  expect(inFull.length).toBeGreaterThan(0)
  for (const f of inFull) expect(RUNNERS).toContain(f)
})

test('执行测试的脚本改了：goTree 变，go-affected 判整套；改文档不变', () => {
  const dir = mkdtempSync(join(tmpdir(), 'go-tree-runner-'))
  dirs.push(dir)
  const back = process.cwd()
  try {
    const env = noGitEnv()
    const g = (...a: string[]) => Bun.spawnSync(['git', ...a], { cwd: dir, env, stdout: 'pipe', stderr: 'pipe' })
    g('init', '-q'); g('config', 'user.email', 't@example.invalid'); g('config', 'user.name', 't')
    mkdirSync(join(dir, 'native'), { recursive: true }); mkdirSync(join(dir, 'scripts'), { recursive: true }); mkdirSync(join(dir, 'docs'), { recursive: true })
    writeFileSync(join(dir, 'native', 'a.go'), 'package a\n')
    for (const f of RUNNERS) writeFileSync(join(dir, f), '// 一\n')
    writeFileSync(join(dir, 'docs', 'x.md'), '一\n')
    g('add', '-A'); g('commit', '-q', '-m', '一')
    process.chdir(dir)
    let last = goTree('HEAD', env)
    writeFileSync(join(dir, 'docs', 'x.md'), '二\n'); g('commit', '-qam', '文档')
    expect(goTree('HEAD', env)).toBe(last)
    for (const f of RUNNERS) {
      writeFileSync(join(dir, f), `// ${f}\n`); g('commit', '-qam', f)
      const now = goTree('HEAD', env)
      expect(now).not.toBe(last)
      last = now
      expect(pick([f], { pkgs: [], deps: new Map(), imports: new Map(), bare: new Set() })).toEqual({ full: true })
    }
  } finally { process.chdir(back) }
}, 60_000)   // 起十几次 git，并行跑时 5 秒不够（2026-11-12 队列里超时过一次）
