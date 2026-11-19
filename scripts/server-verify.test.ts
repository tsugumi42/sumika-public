import { afterAll, expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { readFileSync } from 'node:fs'
import { FULL_PLAN, bunParallel, bunSummary, describePlan, needsRunPlanned, parseBunCounts, PLANNED_PATHS, plan, runGenCheck, runPlanned, runServerTest, serverTestFiles, stepScope } from './server-verify'
import { genGo, load } from './gen-rpc'

// 这几条真的起 bun install、bun test、git 子进程（低优先级），机器忙时一条要好几秒；默认 5 秒的上限会误红。这是干活的上限，不是等待
const SPAWN_TIMEOUT = 60_000

// 下面在临时目录里真起几次只有一两行的 bun test：不值得排全机验证档名额（队列跑 go test 时会等好几分钟、被 5 秒超时打红）。
// 当作已经拿着名额，run-low 就不再抢（scripts/run-low.ps1 的 SUMIKA_SLOT_HELD）
process.env.SUMIKA_SLOT_HELD = [process.env.SUMIKA_SLOT_HELD, 'verify'].filter(Boolean).join(',')

// 挑选：改了什么 → 跑什么（规格-Bun后台第1批 第八节）
const GO = { go: true, bun: false, gen: false }

test('只改 server/：只跑 Bun，不跑 Go', () => {
  expect(plan(['server/src/main.ts', 'server/test/a.test.ts'])).toEqual({ go: false, bun: true, gen: false })
  expect(plan(['server/test/a.test.ts'])).toEqual({ go: false, bun: true, gen: false })
  expect(plan(['server/package.json', 'server/bun.lock'])).toEqual({ go: false, bun: true, gen: false })
})

test('server/ 加文档、脚本：还是不跑 Go（这些不是 Go 会读到的）', () => {
  expect(plan(['server/src/a.ts', 'docs/待办.md', 'scripts/dsh-ask.ts'])).toEqual({ go: false, bun: true, gen: false })
})

test('只改 native/：只跑 Go，不跑 Bun，也不跑生成检查', () => {
  expect(plan(['native/internal/app/app.go', 'native/go.mod'])).toEqual(GO)
  expect(plan(['native/internal/rpcserve/server.go'])).toEqual(GO)
})

test('改 protocol/：生成检查 + Bun + Go', () => {
  expect(plan(['protocol/sumika-rpc.json'])).toEqual({ go: true, bun: true, gen: true })
})

test('只改某个生成文件：也要生成检查和 Bun（native 那个还有 Go）', () => {
  expect(plan(['native/internal/rpcwire/wire_gen.go'])).toEqual({ go: true, bun: true, gen: true })
  // 只有 TS 生成文件在 server/ 里：没有别的 Go 文件，Go 跳过，但生成检查必须跑
  expect(plan(['server/src/protocol/gen.ts'])).toEqual({ go: false, bun: true, gen: true })
})

test('改检查脚本自己（生成器、挑选、队列、钩子配置）：全都跑，外加脚本测试', () => {
  const all = { go: true, bun: true, gen: true, scripts: true }
  for (const f of ['scripts/gen-rpc.ts', 'scripts/server-verify.ts', 'scripts/merge-queue.ts', 'scripts/go-verify.ts', 'scripts/go-affected.ts', 'scripts/pre-push.ts', 'scripts/nightly.ts', 'scripts/nightly-record.ts', 'scripts/precommit-fast.ts', 'scripts/precommit-fast.test.ts', 'scripts/pre-push.test.ts', 'scripts/server-verify.test.ts', 'lefthook.yml']) {
    expect(plan([f])).toEqual(all)
    expect(plan([f, 'server/src/a.ts'])).toEqual(all)
  }
})

test('Bun 照抄的 Go 规则和迁移改了：Bun 也跑', () => {
  for (const f of ['native/internal/store/migrations/chat/0016_x.sql', 'native/internal/store/display.go', 'native/internal/store/display_conversations.go', 'native/internal/store/local_groups.go', 'native/internal/desktopread/service.go']) {
    expect(plan([f])).toEqual({ go: true, bun: true, gen: false })
  }
  expect(plan(['native/internal/store/store.go'])).toEqual({ go: true, bun: false, gen: false })
})

test('改 testdata/shared/rpc/：Bun 和 Go 都跑，不跑生成检查', () => {
  expect(plan(['testdata/shared/rpc/basic.json'])).toEqual({ go: true, bun: true, gen: false })
})

test('改 testdata/shared/ 下 rpc 以外的场景：只跑 Go，冻结的 Bun 不挡合并（ADR-378）', () => {
  for (const f of ['testdata/shared/memory-write/06-replace-protected.json', 'testdata/shared/turn/x.json', 'testdata/shared/qq-send/y.json', 'testdata/shared/README.md'])
    expect(plan([f])).toEqual({ go: true, bun: false, gen: false })
  expect(plan(['testdata/shared/memory-write/a.json', 'testdata/shared/rpc/b.json']).bun).toBe(true)
})

test('别的 testdata（Go 自己的）：只跑 Go', () => {
  expect(plan(['testdata/intended/x.json'])).toEqual(GO)
})

test('混合改动：server/ + native/ 两边都跑', () => {
  expect(plan(['server/src/a.ts', 'native/internal/app/app.go'])).toEqual({ go: true, bun: true, gen: false })
})

// 主窗口（ADR-353）
test('server/ 加会让 Go 整套跑的文件（提示词、go.mod、示例配置、挑包脚本）：Go 要跑', () => {
  for (const f of ['prompts/a.md', 'characters/x/card.json', 'sumika.example.yml', 'native/go.mod', 'scripts/go-verify.ts']) {
    expect(plan(['server/src/a.ts', f]).go).toBe(true)
  }
})

test('没有 server/ 的改动：Go 照旧跑，Bun 不跑（文档、脚本、空改动）', () => {
  expect(plan(['docs/待办.md'])).toEqual(GO)
  expect(plan(['scripts/dsh-ask.ts'])).toEqual(GO)
  expect(plan([])).toEqual(GO)
})

test('路径只是长得像：serverless/、native-x/、protocols/ 不算', () => {
  expect(plan(['serverless/a.ts'])).toEqual(GO)
  expect(plan(['protocols/a.json'])).toEqual(GO)
  expect(plan(['xserver/a.ts'])).toEqual(GO)
})

test('计划行：没有 server/ 时写跳过，不写跑', () => {
  const root = mkdtempSync(join(tmpdir(), 'plan-line-noserver-'))
  try {
    expect(describePlan({ go: true, bun: true, gen: true }, root))
      .toBe('Go：跑；server bun test：跳过（没有 server/）；gen-rpc --check：跑（没有 server/，只查 Go 侧）')
    expect(describePlan({ go: true, bun: false, gen: false }, process.cwd())).toBe('Go：跑；server bun test：不跑；gen-rpc --check：不跑')
  } finally { rmSync(root, { recursive: true, force: true }) }
})

// 结论行
test('从 bun test 输出认出通过和失败的数', () => {
  const out = ' 12 pass\n 0 fail\n 30 expect() calls\nRan 12 tests across 3 files. [88.00ms]\n'
  expect(parseBunCounts(out)).toEqual({ pass: 12, fail: 0 })
  expect(parseBunCounts('')).toEqual({ pass: undefined, fail: undefined })
})

test('结论行：绿写通过数，红写退出码，认不出数就写退出码', () => {
  expect(bunSummary(0, ' 12 pass\n 0 fail\n')).toBe('server bun test：12 通过')
  expect(bunSummary(1, ' 10 pass\n 2 fail\n')).toBe('server bun test：红（退出码 1，2 个失败，10 通过）')
  expect(bunSummary(0, '')).toBe('server bun test：通过（退出码 0）')
  expect(bunSummary(1, 'error: boom')).toBe('server bun test：红（退出码 1）')
})

// 真跑一次：临时目录里放一个假的 server/，绿的和红的各一个
const dirs: string[] = []
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })
function fakeRoot(body: string) {
  const root = mkdtempSync(join(tmpdir(), 'server-verify-'))
  dirs.push(root)
  mkdirSync(join(root, 'server', 'test'), { recursive: true })
  writeFileSync(join(root, 'server', 'package.json'), '{"name":"fake-server","private":true}')   // 每次都 bun install --frozen-lockfile：没有依赖也要能装过
  writeFileSync(join(root, 'server', 'bunfig.toml'), '[test]\nroot = "test"\n')
  writeFileSync(join(root, 'server', 'test', 'a.test.ts'), `import { expect, test } from 'bun:test'\ntest('a', () => { ${body} })\n`)
  return root
}

test('server 测试红：退出码非 0 就是红，结论行写红', () => {
  const r = runServerTest(fakeRoot('expect(1).toBe(2)'), process.env)
  expect(r.ok).toBe(false)
  expect(r.summary).toContain('server bun test：红')
}, SPAWN_TIMEOUT)

test('server 测试绿：结论行写通过数', () => {
  const r = runServerTest(fakeRoot('expect(1).toBe(1)'), process.env)
  expect(r.ok).toBe(true)
  expect(r.summary).toBe('server bun test：1 通过')
}, SPAWN_TIMEOUT)

test('对拍 Go 的 Bun 测试红了不挡关卡，别的照样挡（ADR-378）', () => {
  const root = fakeRoot('expect(1).toBe(1)')
  writeFileSync(join(root, 'server', 'test', 'turn-golden.test.ts'), "import { expect, test } from 'bun:test'\ntest('g', () => { expect(1).toBe(2) })\n")
  const r = runServerTest(root, process.env)
  expect(r.ok).toBe(true)
  expect(r.summary).toBe('server bun test：1 通过')
  writeFileSync(join(root, 'server', 'test', 'other.test.ts'), "import { expect, test } from 'bun:test'\ntest('o', () => { expect(1).toBe(2) })\n")
  expect(runServerTest(root, process.env).ok).toBe(false)
}, SPAWN_TIMEOUT)

test('只摘名单里的：local-tools-golden 红了照样挡，.spec.ts 也会跑，一个文件都没有算红', () => {
  const kept = fakeRoot('expect(1).toBe(1)')
  writeFileSync(join(kept, 'server', 'test', 'local-tools-golden.test.ts'), "import { expect, test } from 'bun:test'\ntest('l', () => { expect(1).toBe(2) })\n")
  expect(runServerTest(kept, process.env).ok).toBe(false)
  const spec = fakeRoot('expect(1).toBe(1)')
  writeFileSync(join(spec, 'server', 'test', 'x.spec.ts'), "import { expect, test } from 'bun:test'\ntest('s', () => { expect(1).toBe(2) })\n")
  expect(runServerTest(spec, process.env).ok).toBe(false)
  const empty = fakeRoot('expect(1).toBe(1)')
  rmSync(join(empty, 'server', 'test', 'a.test.ts'))
  writeFileSync(join(empty, 'server', 'test', 'turn-golden.test.ts'), "import { expect, test } from 'bun:test'\ntest('g', () => { expect(1).toBe(1) })\n")
  const r = runServerTest(empty, process.env)
  expect(r.ok).toBe(false)
  expect(r.summary).toContain('一个测试文件都没有')
}, SPAWN_TIMEOUT)

test('计划任务里超时的冻结测试摘掉：serverTestFiles 不含 run-file-secure.test.ts', () => {
  const dir = mkdtempSync(join(tmpdir(), 'server-files-'))
  dirs.push(dir)
  for (const f of ['run-file-secure.test.ts', 'turn-golden.test.ts', 'local-tools-golden.test.ts', 'x.spec.ts']) writeFileSync(join(dir, f), '')
  expect(serverTestFiles(dir).sort()).toEqual(['local-tools-golden.test.ts', 'x.spec.ts'])
})

test('有 .only、跳过、待办：退出码 0 也算红', () => {
  expect(runServerTest(fakeRoot("expect(1).toBe(1) }); test.only('b', () => {"), process.env).ok).toBe(false)
  const skip = runServerTest(fakeRoot("expect(1).toBe(1) }); test.skip('b', () => {"), process.env)
  expect(skip.ok).toBe(false)
  expect(skip.summary).toContain('跳过')
  expect(runServerTest(fakeRoot("expect(1).toBe(1) }); test.todo('b', () => {"), process.env).ok).toBe(false)
}, SPAWN_TIMEOUT)

test('runPlanned 按 plan 跑：Bun 红就红，没挑上的不跑', () => {
  const root = fakeRoot('expect(1).toBe(2)')
  expect(runPlanned({ go: false, bun: true, gen: false }, root, process.env).map((s) => s.ok)).toEqual([false])
  expect(runPlanned({ go: true, bun: false, gen: false }, root, process.env)).toEqual([])
}, SPAWN_TIMEOUT)

test('生成检查：仓库里的生成文件是新的，退出码 0', () => {
  expect(runGenCheck(process.cwd(), process.env).ok).toBe(true)
}, SPAWN_TIMEOUT)

test('runGenCheck：没有 server/ 时只查 Go 侧，结论写明原因', () => {
  const root = mkdtempSync(join(tmpdir(), 'gen-check-noserver-'))
  dirs.push(root)
  mkdirSync(join(root, 'scripts')); mkdirSync(join(root, 'protocol')); mkdirSync(join(root, 'native', 'internal', 'rpcwire'), { recursive: true })
  const source = join(import.meta.dir, '..', 'protocol', 'sumika-rpc.json')
  writeFileSync(join(root, 'scripts', 'gen-rpc.ts'), readFileSync(join(import.meta.dir, 'gen-rpc.ts')))
  writeFileSync(join(root, 'protocol', 'sumika-rpc.json'), readFileSync(source))
  writeFileSync(join(root, 'native', 'internal', 'rpcwire', 'wire_gen.go'), genGo(load(source)))
  const r = runGenCheck(root, process.env)
  expect(r.ok).toBe(true)
  expect(r.summary).toContain('没有 server/，只查了 Go 侧')
}, SPAWN_TIMEOUT)

test('没有 server/：步骤跳过、结论写明原因，不起子进程', () => {
  const root = mkdtempSync(join(tmpdir(), 'steps-noserver-'))
  dirs.push(root)
  expect(runServerTest(root, process.env)).toEqual({ name: 'server bun test', ok: true, seconds: 0, summary: 'server bun test：没有 server/，跳过', skipped: true })
  const steps = runPlanned({ go: false, bun: true, gen: false }, root, process.env)
  expect(steps.map((s) => [s.ok, s.skipped, s.summary])).toEqual([
    [true, true, 'server bun test：没有 server/，跳过'],
  ])
})

// ADR-359、378：desktop/ 是冻结的网页，没有自己的检查了；只改它什么都不挑
test('只改 desktop/ 或令牌：什么检查都不跑', () => {
  expect(plan(['desktop/src/web/App.tsx', 'desktop/bun.lock'])).toEqual({ go: false, bun: false, gen: false })
  expect(plan(['native/internal/design/tokens/semantic.tokens.json'])).toEqual(GO)
  for (const f of ['desktop/src/web/App.tsx', 'desktop/bun.lock', 'native/internal/design/tokens/semantic.tokens.json']) {
    expect(needsRunPlanned(plan([f]))).toBe(false)
  }
  expect(needsRunPlanned(plan(['native/internal/app/app.go']))).toBe(false)
  expect(FULL_PLAN).toEqual({ go: true, bun: true, gen: true, scripts: true })
})

test('go-verify、pre-push 都用共用的判断和路径，不自己写条件', () => {
  for (const f of ['scripts/go-verify.ts', 'scripts/pre-push.ts']) {
    const src = readFileSync(f, 'utf8')
    expect(src).toContain('needsRunPlanned(pl)')
    expect(src).not.toMatch(/pl\.bun \|\| pl\.gen/)
    expect(src).toContain('...PLANNED_PATHS')
  }
  expect(readFileSync('scripts/go-verify.ts', 'utf8')).toContain('full ? FULL_PLAN')
  expect(PLANNED_PATHS).toContain('desktop')
  expect(PLANNED_PATHS).toContain('native/internal/design')
})

// ───── 验过的记录（推送前不重跑队列已在同一棵树上跑过的步骤） ─────
import { cleanTree, loadStepRecords, runPlanned as runPlannedR, type StepResult as SR } from './server-verify'
import { mkdtempSync as mkd, rmSync as rmd, writeFileSync as wf } from 'node:fs'
import { tmpdir as td } from 'node:os'
import { join as jn } from 'node:path'
// 钩子里跑时 git 设了 GIT_DIR 这些，起 git 前清掉（2026-11-11 出过：临时仓库的 git init 落到了真仓库）
const noGit = () => Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_'))) as Record<string, string>
const gitIn = (cwd: string, ...a: string[]) => Bun.spawnSync(['git', '-c', 'user.name=x', '-c', 'user.email=x@example.com', ...a], { cwd, env: noGit() })

test('同一棵树、同一套 bun 验过的步骤不再跑；红的不记；换了树就重跑', () => {
  const dir = mkd(jn(td(), 'steps-'))
  try {
    const rec = jn(dir, 'rec.json')
    let calls = 0
    const ok = (name: string) => (): SR => { calls++; return { name, ok: true, seconds: 1, summary: `${name}：过` } }
    const bad = (name: string) => (): SR => { calls++; return { name, ok: false, seconds: 1, summary: `${name}：红` } }
    const p = { go: false, bun: true, gen: false, scripts: true }
    runPlannedR(p, dir, {}, undefined, { tree: 'T1', by: '队列', records: rec, runners: { 'server bun test': ok('server bun test'), '检查脚本的测试': bad('检查脚本的测试') } })
    expect(calls).toBe(2)
    expect(loadStepRecords(rec).map((r) => r.step)).toEqual(['server bun test'])
    calls = 0
    const again = runPlannedR(p, dir, {}, undefined, { tree: 'T1', by: '推送', records: rec, runners: { 'server bun test': bad('server bun test'), '检查脚本的测试': ok('检查脚本的测试') } })
    expect(calls).toBe(1)   // server 认了记录没跑（换成会红的也不跑）；脚本测试上次红了，照跑
    expect(again.find((s) => s.name === 'server bun test')!.summary).toContain('已验过')
    calls = 0
    runPlannedR(p, dir, {}, undefined, { tree: 'T2', by: '推送', records: rec, runners: { 'server bun test': ok('server bun test'), '检查脚本的测试': ok('检查脚本的测试') } })
    expect(calls).toBe(2)   // 换了树
    calls = 0
    runPlannedR(p, dir, {}, undefined, { records: rec, runners: { 'server bun test': ok('server bun test'), '检查脚本的测试': ok('检查脚本的测试') } })
    expect(calls).toBe(2)   // 没给树（整套、工作区不干净）：一律跑
  } finally { rmd(dir, { recursive: true, force: true }) }
}, SPAWN_TIMEOUT)

test('cleanTree：工作区就是这棵树才给；改了已跟踪文件、测试目录里多了文件就不给；无关的草稿文件不算', () => {
  const dir = mkd(jn(td(), 'steps-git-'))
  try {
    gitIn(dir, 'init', '-q')
    wf(jn(dir, 'a.md'), '一\n'); gitIn(dir, 'add', 'a.md'); gitIn(dir, 'commit', '-q', '-m', '起点')
    const env = noGit()
    const t = cleanTree(dir, 'HEAD', env)
    expect(t).toMatch(/^[0-9a-f]{40}$/)
    wf(jn(dir, 'draft.bak'), '草稿')                       // 与测试无关的没进 git 的文件
    expect(cleanTree(dir, 'HEAD', env)).toBe(t)
    mkdirSync(jn(dir, 'server/test'), { recursive: true }); wf(jn(dir, 'server/test/x.test.ts'), '')
    expect(cleanTree(dir, 'HEAD', env)).toBeUndefined()   // 测试会读到的目录里多了文件
    rmd(jn(dir, 'server'), { recursive: true, force: true })
    wf(jn(dir, 'a.md'), '二\n')
    expect(cleanTree(dir, 'HEAD', env)).toBeUndefined()   // 已跟踪文件改了
  } finally { rmd(dir, { recursive: true, force: true }) }
}, 30_000) // 连着起七八次 git，机器忙时超过默认 5 秒（2026-11-12 合并队列里红过）

test('换了一套 bun：以前的记录不认', () => {
  const dir = mkd(jn(td(), 'steps-'))
  try {
    const rec = jn(dir, 'rec.json')
    wf(rec, JSON.stringify([{ tree: 'T3', bun: '0.0.1', step: 'server bun test', at: '2026-11-11T00:00:00Z', by: '旧' }]))
    let calls = 0
    runPlannedR({ go: false, bun: true, gen: false }, dir, {}, undefined, { tree: 'T3', records: rec, runners: { 'server bun test': () => { calls++; return { name: 'server bun test', ok: true, seconds: 1, summary: '过' } } } })
    expect(calls).toBe(1)
  } finally { rmd(dir, { recursive: true, force: true }) }
}, SPAWN_TIMEOUT)

test('stepScope：只改了这一步不读的目录，算出来一样；改了它读的目录就不一样', () => {
  const dir = mkd(jn(td(), 'steps-scope-'))
  try {
    const env = noGit()
    gitIn(dir, 'init', '-q')
    mkdirSync(jn(dir, 'server'), { recursive: true }); mkdirSync(jn(dir, 'desktop'), { recursive: true })
    wf(jn(dir, 'server/a.ts'), '1'); wf(jn(dir, 'desktop/b.ts'), '1')
    gitIn(dir, 'add', '-A'); gitIn(dir, 'commit', '-q', '-m', '一')
    const t1 = cleanTree(dir, 'HEAD', env)!
    wf(jn(dir, 'desktop/b.ts'), '2'); gitIn(dir, 'commit', '-qam', '只改 desktop')
    const t2 = cleanTree(dir, 'HEAD', env)!
    expect(t2).not.toBe(t1)
    expect(stepScope(dir, t2, 'server bun test', env)).toBe(stepScope(dir, t1, 'server bun test', env))   // server 的测试不读 desktop/
    wf(jn(dir, 'server/a.ts'), '2'); gitIn(dir, 'commit', '-qam', '改 server')
    const t3 = cleanTree(dir, 'HEAD', env)!
    expect(stepScope(dir, t3, 'server bun test', env)).not.toBe(stepScope(dir, t2, 'server bun test', env))
    expect(stepScope(dir, 'T1', 'server bun test', env)).toBe('T1')   // 假树号照原样
  } finally { rmd(dir, { recursive: true, force: true }) }
}, 30_000)

test('stepScope：跑这一步的脚本改了，旧记录不再认', () => {
  const dir = mkd(jn(td(), 'steps-scope-runner-'))
  try {
    const env = noGit()
    gitIn(dir, 'init', '-q')
    mkdirSync(jn(dir, 'server'), { recursive: true }); mkdirSync(jn(dir, 'scripts'), { recursive: true })
    wf(jn(dir, 'server/a.ts'), '1'); wf(jn(dir, 'scripts/server-verify.ts'), '1')
    gitIn(dir, 'add', '-A'); gitIn(dir, 'commit', '-q', '-m', '一')
    const t1 = cleanTree(dir, 'HEAD', env)!
    wf(jn(dir, 'scripts/server-verify.ts'), '2'); gitIn(dir, 'commit', '-qam', '只改跑法')
    const t2 = cleanTree(dir, 'HEAD', env)!
    expect(stepScope(dir, t2, 'server bun test', env)).not.toBe(stepScope(dir, t1, 'server bun test', env))
  } finally { rmd(dir, { recursive: true, force: true }) }
}, 30_000)

test('bunParallel：默认核数减 4、最多 8、至少 1；SUMIKA_BUN_PARALLEL 能改', () => {
  expect(bunParallel({}, 12)).toBe(8)
  expect(bunParallel({}, 16)).toBe(8)
  expect(bunParallel({}, 8)).toBe(4)
  expect(bunParallel({}, 4)).toBe(1)
  expect(bunParallel({ SUMIKA_BUN_PARALLEL: '3' }, 12)).toBe(3)
  expect(bunParallel({ SUMIKA_BUN_PARALLEL: '不是数' }, 12)).toBe(8)
})
