import { expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { goPackages, serverNeedsAll, serverTestFiles } from './precommit-fast'

// 起子进程一律传清理过的 env：钩子里跑时继承了 GIT_DIR 等，会落到真仓库上（2026-11-11 core.bare 事故）
const cleanEnv = () => Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')))
const script = join(import.meta.dir, 'precommit-fast.ts')

test('serverTestFiles：只挑 server/test 下的测试文件，路径改成相对 server/', () => {
  expect(serverTestFiles(['server/test/a.test.ts', 'server/src/x.ts', 'server/test/helpers/client.ts', 'server\\test\\b.test.tsx', 'desktop/test/c.test.ts', 'server/test/a.test.ts']))
    .toEqual(['./test/a.test.ts', './test/b.test.tsx'])
  expect(serverTestFiles([])).toEqual([])
})

test('goPackages：暂存的 .go 所在包；根包是 .；testdata 不算；go.mod / go.sum 变了就整个模块', () => {
  expect(goPackages(['native/main.go', 'native/internal/app/a.go', 'native/internal/app/a_test.go', 'native/internal/x/testdata/t.go', 'server/a.go']))
    .toEqual(['.', './internal/app'])
  expect(goPackages(['native/internal/app/a.go', 'native/go.sum'])).toEqual(['./...'])
  expect(goPackages(['native/go.mod'])).toEqual(['./...'])
  expect(goPackages(['docs/a.md'])).toEqual([])
})

// 改坏检查：钩子换成只跑改到的那块以后，改到的那块坏了照样要红
test('server-tests：暂存了一个会红的测试文件就红；没暂存测试文件就过', () => {
  const root = mkdtempSync(join(tmpdir(), 'precommit-fast-'))
  try {
    mkdirSync(join(root, 'server', 'test'), { recursive: true })
    writeFileSync(join(root, 'server', 'test', 'bad.test.ts'), "import { expect, test } from 'bun:test'\ntest('坏', () => { expect(1).toBe(2) })\n")
    writeFileSync(join(root, 'server', 'test', 'good.test.ts'), "import { expect, test } from 'bun:test'\ntest('好', () => { expect(1).toBe(1) })\n")
    const go = (files: string[]) => Bun.spawnSync([process.execPath, script, 'server-tests', ...files], { cwd: root, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' }).exitCode
    expect(go(['server/test/bad.test.ts'])).toBe(1)
    expect(go(['server/test/good.test.ts'])).toBe(0)
    expect(go(['server/src/whatever.ts'])).toBe(0)
  } finally { rmSync(root, { recursive: true, force: true }) }
}, 60_000)

test('没有 server/：server-tests 跳过、退出码 0，结论写明原因', () => {
  const root = mkdtempSync(join(tmpdir(), 'precommit-fast-noserver-'))
  try {
    const r = Bun.spawnSync([process.execPath, script, 'server-tests', 'server/test/a.test.ts'], { cwd: root, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' })
    expect(r.exitCode).toBe(0)
    expect(r.stdout.toString()).toContain('没有 server/，跳过')
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('go-vet：暂存的包 vet 报错就红；别的包有问题不归这次管', () => {
  const root = mkdtempSync(join(tmpdir(), 'precommit-fast-go-'))
  try {
    const n = join(root, 'native')
    mkdirSync(join(n, 'bad'), { recursive: true }); mkdirSync(join(n, 'good'), { recursive: true })
    writeFileSync(join(n, 'go.mod'), 'module example.test/m\n\ngo 1.22\n')
    // Printf 的参数个数对不上：vet 一定报
    writeFileSync(join(n, 'bad', 'b.go'), 'package bad\n\nimport "fmt"\n\nfunc F() { fmt.Printf("%d %d\\n", 1) }\n')
    writeFileSync(join(n, 'good', 'g.go'), 'package good\n\nfunc G() int { return 1 }\n')
    const go = (files: string[]) => Bun.spawnSync([process.execPath, script, 'go-vet', ...files], { cwd: root, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' }).exitCode
    expect(go(['native/bad/b.go'])).toBe(1)
    expect(go(['native/good/g.go'])).toBe(0)
    expect(go(['native/go.mod'])).toBe(1)   // 依赖变了 vet 整个模块，bad 也在里面
  } finally { rmSync(root, { recursive: true, force: true }) }
}, 120_000)

test('serverNeedsAll：改了 server/test 下的辅助文件才要整套；只改测试文件或 src 不要', () => {
  expect(serverNeedsAll(['server/test/helpers/client.ts'])).toBe(true)
  expect(serverNeedsAll(['server/test/preload.ts'])).toBe(true)
  expect(serverNeedsAll(['server/test/a.test.ts', 'server/src/x.ts'])).toBe(false)
  expect(serverNeedsAll([])).toBe(false)
})

// lefthook 的 glob 写坏会静默跳过这条命令：接线也要有测试（2026-11-12 复核问题 2）
test('lefthook.yml 里两条快检查接在 precommit-fast 上，glob 认得代表路径', async () => {
  const y = Bun.YAML.parse(await Bun.file(join(import.meta.dir, '..', 'lefthook.yml')).text()) as any
  const cmds = y['pre-commit'].commands
  const srv = cmds['Server测试'], gov = cmds['Go检查']
  expect(srv.run).toBe('bun scripts/precommit-fast.ts server-tests {staged_files}')
  expect(gov.run).toBe('bun scripts/precommit-fast.ts go-vet {staged_files}')
  expect(srv.root).toBeUndefined(); expect(gov.root).toBeUndefined()   // 路径相对仓库根
  const hit = (globs: string | string[], f: string) => [globs].flat().some((g) => new Bun.Glob(g).match(f))
  for (const f of ['server/test/a.test.ts', 'server/test/sub/b.test.tsx', 'server/test/helpers/client.ts']) expect(hit(srv.glob, f)).toBe(true)
  expect(hit(srv.glob, 'server/src/a.ts')).toBe(false)
  for (const f of ['native/main.go', 'native/internal/app/a_test.go', 'native/go.mod', 'native/go.sum']) expect(hit(gov.glob, f)).toBe(true)
  expect(hit(gov.glob, 'server/a.go')).toBe(false)
})

test('go-vet：目录里的 .go 全被构建约束排除时跳过，不误红', () => {
  const root = mkdtempSync(join(tmpdir(), 'precommit-fast-tag-'))
  try {
    const n = join(root, 'native')
    mkdirSync(join(n, 'lin'), { recursive: true })
    writeFileSync(join(n, 'go.mod'), 'module example.test/m\n\ngo 1.22\n')
    writeFileSync(join(n, 'lin', 'a_plan9.go'), 'package lin\n\nfunc F() {}\n')   // 本机（Windows / Linux）都不编
    const r = Bun.spawnSync([process.execPath, script, 'go-vet', 'native/lin/a_plan9.go'], { cwd: root, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' })
    expect(r.exitCode).toBe(0)
  } finally { rmSync(root, { recursive: true, force: true }) }
}, 60_000)
