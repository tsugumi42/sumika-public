import { expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describePick, EXAMPLE, EXAMPLE_READERS, type Graph, normalizePath, pick, readGraph, readsGo } from './go-affected'

// 编的依赖图：types ← store ← app ← cmd/main，根包也直接 import app；aud 自己没有测试文件，store 和 app 引用它；archcheck 是架构测试包
// deps 是传递依赖，imports 是直接 import（ADR-362 的直接导入方看后者）
const M = 'sumika-native'
const P = (s: string) => (s === '根' ? M : `${M}/${s}`)
const pkgs = ['internal/types', 'internal/store', 'internal/app', 'internal/aud', 'cmd/main', 'internal/leaf', 'internal/archcheck', '根'].map(P)
const deps = new Map<string, Set<string>>([
  [P('internal/types'), new Set()],
  [P('internal/store'), new Set([P('internal/types'), P('internal/aud')])],
  [P('internal/app'), new Set([P('internal/store'), P('internal/types'), P('internal/aud')])],
  [P('internal/aud'), new Set()],
  [P('cmd/main'), new Set([P('internal/app'), P('internal/store'), P('internal/types'), P('internal/aud')])],
  [P('internal/leaf'), new Set()],
  [P('internal/archcheck'), new Set()],
  [M, new Set([P('internal/app')])],
])
const imports = new Map<string, Set<string>>([
  [P('internal/types'), new Set()],
  [P('internal/store'), new Set([P('internal/types'), P('internal/aud')])],
  [P('internal/app'), new Set([P('internal/store'), P('internal/aud')])],
  [P('internal/aud'), new Set()],
  [P('cmd/main'), new Set([P('internal/app')])],
  [P('internal/leaf'), new Set()],
  [P('internal/archcheck'), new Set()],
  [M, new Set([P('internal/app')])],
])
const g: Graph = { pkgs, deps, imports, bare: new Set([P('internal/aud')]) }
const names = (files: string[], graph = g) => {
  const r = pick(files, graph)
  return r.full ? '整套' : r.pkgs.map((p) => (p === M ? '根' : p.slice(M.length + 1))).join(' ')
}

test('只改文档不测包', () => expect(names(['docs/x.md', 'LESSONS.md'])).toBe(''))
test('改底层包测它自己和直接导入方（加架构测试），不连带间接导入方', () => expect(names(['native/internal/types/a.go'])).toBe('internal/types internal/store internal/archcheck'))
test('直接导入方只多一层，不递归：改 store 带 app，不带 cmd/main', () => expect(names(['native/internal/store/a.go'])).toBe('internal/store internal/app internal/archcheck'))
test('直接导入方不带根包：app 被根包直接 import，改 app 只带 cmd/main', () => expect(names(['native/internal/app/a.go'])).toBe('internal/app cmd/main internal/archcheck'))
test('根包自己改了照旧测它', () => expect(names(['native/internal/app/a.go', 'native/main.go'])).toBe('internal/app cmd/main 根 internal/archcheck'))
test('只改 _test.go：只测这个包，不带导入方', () => expect(names(['native/internal/types/a_test.go'])).toBe('internal/types internal/archcheck'))
// 别的包的测试会按路径读 testdata（linkexpand 读 ../linkreader/testdata），所以 testdata 改了也带导入方
test('testdata 改了也带导入方', () => {
  expect(names(['native/internal/types/testdata/x.json'])).toBe('internal/types internal/store internal/archcheck')
  expect(names(['native/internal/types/sub/testdata/x.json'])).toBe('internal/types internal/store internal/archcheck')
})
test('测试文件和源码一起改：源码那份带导入方', () => expect(names(['native/internal/types/a_test.go', 'native/internal/types/a.go'])).toBe('internal/types internal/store internal/archcheck'))
test('没有测试文件的导入方不选', () => {
  const bareImporter = new Set([P('internal/aud'), P('cmd/main')])
  expect(names(['native/internal/app/a.go'], { ...g, bare: bareImporter })).toBe('internal/app internal/archcheck')
})
// 没有测试文件的包没有测试可跑，只靠导入它的包测：改底层包时穿过它，选最近的有测试的导入方（ADR-362 决定 3），不然它背后的测试漏掉
test('没有测试文件的导入方穿过去：改 types 带上导入 store 的 app，不带更远的 cmd/main', () => {
  expect(names(['native/internal/types/a.go'], { ...g, bare: new Set([P('internal/store')]) })).toBe('internal/types internal/app internal/archcheck')
})
test('连着几层都没有测试文件，穿到有测试的为止；根包穿过去也不带', () => {
  expect(names(['native/internal/types/a.go'], { ...g, bare: new Set([P('internal/store'), P('internal/app')]) })).toBe('internal/types cmd/main internal/archcheck')
  expect(names(['native/internal/store/a.go'], { ...g, bare: new Set([P('internal/app'), P('cmd/main')]) })).toBe('internal/store internal/archcheck')
})
test('手动输入的路径：反斜杠和开头的 ./ 换成 git 的写法', () => {
  expect(normalizePath('native\\internal\\linkreader\\reader.go')).toBe('native/internal/linkreader/reader.go')
  expect(normalizePath('./native/internal/linkreader/reader.go')).toBe('native/internal/linkreader/reader.go')
  expect(normalizePath('.\\native\\main.go')).toBe('native/main.go')
  expect(normalizePath('native/main.go')).toBe('native/main.go')
})
test('挑包说明：改到几个、导入方几个', () => {
  expect(describePick(pick(['native/internal/types/a.go'], g))).toBe('改到的 1 个包 + 导入方 1 个 + archcheck')
  expect(describePick(pick(['native/internal/types/a_test.go', 'native/internal/leaf/a_test.go'], g))).toBe('改到的 2 个包 + 导入方 0 个 + archcheck')
  expect(describePick(pick(['native/internal/archcheck/x_test.go'], g))).toBe('改到的 1 个包 + 导入方 0 个')
  expect(describePick(pick(['docs/x.md'], g))).toBe('没有要测的包')
  expect(describePick(pick(['native/go.mod'], g))).toBe('整套')
})
test('改了几个包就测那几个', () => expect(names(['native/internal/leaf/a_test.go', 'native/cmd/main/main.go'])).toBe('cmd/main internal/leaf internal/archcheck'))
test('自己没有测试文件的包，改了连 import 它的包一起测', () => expect(names(['native/internal/aud/aud.go'])).toBe('internal/store internal/app internal/aud cmd/main internal/archcheck'))
test('testdata 和嵌进去的文件算所属的包，也带它的导入方（leaf 没有导入方）', () => expect(names(['native/internal/store/migrations/0005.sql', 'native/internal/leaf/testdata/a.json'])).toBe('internal/store internal/app internal/leaf internal/archcheck'))
test('删掉的子目录往上找包', () => expect(names(['native/internal/leaf/gone/x.go'])).toBe('internal/leaf internal/archcheck'))
test('模块根的文件算根包', () => expect(names(['native/main.go'])).toBe('根 internal/archcheck'))
test('只改架构测试包不重复带', () => expect(names(['native/internal/archcheck/x_test.go'])).toBe('internal/archcheck'))
test('模块根不是包时，不属于任何包的文件整套跑', () => expect(pick(['native/README.md'], { ...g, pkgs: pkgs.filter((p) => p !== M) }).full).toBe(true))
for (const f of ['native/go.mod', 'native/go.sum', 'prompts/core.md', 'characters/x/card.yml', 'scripts/bench-t2/tasks/a.json','scripts/pre-push.ts', 'scripts/go-affected.ts', 'scripts/go-verify.ts', 'scripts/nightly.ts']) {
  test(`改 ${f} 整套跑`, () => expect(names(['docs/x.md', f])).toBe('整套'))
}
// 示例配置只测读它的包（2026-11-13）：编的图里有 app 和根包，读它的包不往外带导入方（改 app 源码会带 cmd/main，这里不该带）
test('改 sumika.example.yml 只测读它的包，不带导入方', () => {
  expect(names(['sumika.example.yml'])).toBe('internal/app 根 internal/archcheck')
  expect(names(['docs/x.md', 'sumika.example.yml', 'native/internal/leaf/a_test.go'])).toBe('internal/app internal/leaf 根 internal/archcheck')
})
// 守卫：真实仓库里哪个包的测试提到示例配置，就必须在 EXAMPLE_READERS 里；不然改它时那个包的测试漏跑
test('提到 sumika.example.yml 的 Go 测试所在的包都在 EXAMPLE_READERS 里', async () => {
  const native = join(import.meta.dir, '..', 'native')
  const found = new Set<string>()
  for (const f of new Bun.Glob('**/*_test.go').scanSync({ cwd: native })) {
    const rel = f.replace(/\\/g, '/')
    if (rel.startsWith('vendor/')) continue
    if ((await Bun.file(join(native, rel)).text()).includes(EXAMPLE)) found.add(rel.split('/').slice(0, -1).join('/'))
  }
  expect(found.size).toBeGreaterThan(0)
  expect([...found].filter((p) => !EXAMPLE_READERS.includes(p)).sort()).toEqual([])
}, 60_000)   // 要读全部 _test.go，机器忙时超过默认 5 秒（2026-11-13 队列里红过一次）
test('别的脚本不碰 Go', () => expect(names(['scripts/dsh-ask.ts'])).toBe(''))

// go list 整个真实模块要二三十秒，几条用真实依赖图的测试共用一次
let cached: Graph | undefined
const realGraph = () => (cached ??= readGraph(join(import.meta.dir, '..', 'native')))

// 真实的依赖图：有内核和模块根；persona 自己没有测试文件，改它会带上引用它的包（ADR-225 的例外）。
// 原来用 audience，它后来有了测试文件，这条在 main 上红过（2026-11-10 改）
// 2026-11-16 又红一次（persona 加了测试文件）：不再写死包名，从真实依赖图里挑一个没有测试文件、又被别的有测试的包直接引用的包
test('真实的依赖图', () => {
  const real = realGraph()
  expect(real.pkgs).toContain(`${M}/internal/turn`)
  const importersOf = (p: string) => [...real.imports].filter(([q, s]) => s.has(p) && q !== M && !real.bare.has(q)).map(([q]) => q)
  const bare = [...real.bare].filter((p) => p.startsWith(`${M}/internal/`) && importersOf(p).length > 0).sort()
  expect(bare.length).toBeGreaterThan(0)
  const p = bare[0]!
  const r = pick([`native/${p.slice(M.length + 1)}/x.go`], real)
  expect(r.full).toBe(false)
  if (r.full) return
  for (const q of importersOf(p)) expect(r.pkgs).toContain(q)
  expect(r.pkgs).toContain(`${M}/internal/archcheck`)
}, 60_000)

// 起因（ADR-362）：只改 linkreader，依赖它的 linkexpand 的对拍没跑到而红。真实依赖图里它必须被带上；根包不带
test('真实的依赖图：改 linkreader 带上直接导入方 linkexpand，不带根包、不带更远的包', () => {
  const real = realGraph()
  const r = pick(['native/internal/linkreader/reader.go'], real)
  expect(r.full).toBe(false)
  if (r.full) return
  expect(r.pkgs).toContain(`${M}/internal/linkreader`)
  expect(r.pkgs).toContain(`${M}/internal/linkexpand`)
  expect(r.pkgs).not.toContain(M)
  // 其余的要么直接 import linkreader，要么 import 了直接导入它、又没有测试文件的包（segview），不更远
  const via = (p: string) => real.imports.get(p)?.has(`${M}/internal/linkreader`) || [...(real.imports.get(p) ?? [])].some((q) => real.bare.has(q) && real.imports.get(q)?.has(`${M}/internal/linkreader`))
  for (const p of r.pkgs) if (p !== `${M}/internal/linkreader` && p !== `${M}/internal/archcheck`) expect(via(p)).toBe(true)
  const t = pick(['native/internal/linkreader/reader_test.go'], real)
  expect(!t.full && t.pkgs).toEqual([`${M}/internal/linkreader`, `${M}/internal/archcheck`])
}, 60_000)

// 实测（2026-11-13 复核）：segview 没有测试文件、import 了 linkreader，导入它的 chatui 的测试走得到 linkreader，改 linkreader 要选上 chatui
test('真实的依赖图：改 linkreader 穿过没有测试文件的 segview 带上 chatui', () => {
  const real = realGraph()
  if (!real.bare.has(`${M}/internal/segview`)) return   // segview 后来有了测试文件就不再是这个形状，小模块里的 v → w 仍守着行为（audience 那次同理）
  const r = pick(['native/internal/linkreader/reader.go'], real)
  expect(!r.full && r.pkgs).toContain(`${M}/internal/chatui`)
  expect(!r.full && r.pkgs).not.toContain(`${M}/internal/segview`)
}, 60_000)

// 真的 go list：造一个小模块，a ← b ← c 三层，根包也直接 import a，u 没有测试文件、被 a 引用，t 只有测试文件。
// x 只在自己的 _test.go（同包测试）里 import a，y 只在外部测试包 y_test 里 import a：导入方含 _test.go 里的 import（ADR-362 决定 1），只读非测试那一行的 Imports 会漏掉这两个。
// v 没有测试文件、import 了 a，w 只经 v 才碰到 a：改 a 要穿过 v 选上 w。
// 模块名必须叫 sumika-native（MODULE 写死），文件路径按 native/ 前缀传。a、b、c、根包都带测试文件，
// 不然“改成递归”“去掉排除根包”会被“没有测试文件的包带全部导入方”那条盖住而测不出
function tempModule() {
  const dir = mkdtempSync(join(tmpdir(), 'go-affected-mod-'))
  const w = (f: string, body: string) => { mkdirSync(join(dir, ...f.split('/').slice(0, -1)), { recursive: true }); writeFileSync(join(dir, f), body) }
  const testFile = (pkg: string, name: string, extra = '') => `package ${pkg}\n\nimport (\n	"testing"\n${extra ? `\n	${extra}\n` : ''})\n\nfunc Test${name}(t *testing.T) {}\n`
  w('go.mod', `module ${M}\n\ngo 1.21\n`)
  w('main.go', `package main\n\nimport "${M}/a"\n\nfunc main() { _ = a.A }\n`)
  w('main_test.go', testFile('main', 'Main'))
  w('u/u.go', 'package u\n\nvar U = 1\n')
  w('a/a.go', `package a\n\nimport "${M}/u"\n\nvar A = u.U\n`)
  w('a/a_test.go', testFile('a', 'A'))
  w('b/b.go', `package b\n\nimport "${M}/a"\n\nvar B = a.A\n`)
  w('b/b_test.go', testFile('b', 'B'))
  w('c/c.go', `package c\n\nimport "${M}/b"\n\nvar C = b.B\n`)
  w('c/c_test.go', testFile('c', 'C'))
  w('t/t_test.go', testFile('t', 'T'))
  w('x/x.go', 'package x\n')
  w('x/x_test.go', testFile('x', 'X', `_ "${M}/a"`))
  w('y/y.go', 'package y\n')
  w('y/y_test.go', testFile('y_test', 'Y', `_ "${M}/a"`))
  w('v/v.go', `package v\n\nimport "${M}/a"\n\nvar V = a.A\n`)
  w('w/w.go', `package w\n\nimport "${M}/v"\n\nvar W = v.V\n`)
  w('w/w_test.go', testFile('w', 'W'))
  return dir
}
// 起 go 一律传清掉 GIT_* 的 env；不让外面的 go.work、GOFLAGS 和自动下载工具链影响这个小模块
const goEnv = () => ({ ...Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_'))), GOWORK: 'off', GOFLAGS: '-mod=mod', GOTOOLCHAIN: 'local', GOPROXY: 'off' }) as Record<string, string | undefined>
const short = (r: ReturnType<typeof pick>) => (r.full ? '整套' : r.pkgs.map((p) => (p === M ? '根' : p.slice(M.length + 1))).sort().join(' '))

test('真的 go list 一个小模块：改 a 选中 a、b（含只在测试文件里 import 的 x、y，穿过 v 的 w），不选 c、不选根包', () => {
  const dir = tempModule()
  try {
    const gr = readGraph(dir, goEnv())
    expect(gr.bare.has(`${M}/u`)).toBe(true)
    expect(gr.bare.has(`${M}/v`)).toBe(true)
    expect(gr.imports.get(`${M}/x`)).toEqual(new Set([`${M}/a`]))   // 只在同包 _test.go 里 import
    expect(gr.imports.get(`${M}/y`)).toEqual(new Set([`${M}/a`]))   // 只在外部测试包 y_test 里 import
    expect(gr.imports.get(`${M}/b`)).toEqual(new Set([`${M}/a`]))
    expect(gr.imports.get(M)).toEqual(new Set([`${M}/a`]))
    expect(gr.imports.get(`${M}/a`)).toEqual(new Set([`${M}/u`]))
    expect([...gr.deps.get(`${M}/c`)!].filter((d) => d !== `${M}/c`).sort()).toEqual([`${M}/a`, `${M}/b`, `${M}/u`])   // 传递依赖（含自己的测试程序指回自己，不算）
    const at = (...fs: string[]) => short(pick(fs, gr))
    expect(at('native/a/a.go')).toBe('a b w x y')                 // 直接导入方 b、x、y（后两个只经测试文件）；v 没有测试文件，穿过去选 w；c 是间接，根包不带
    expect(at('native/b/b.go')).toBe('b c')
    expect(at('native/c/c.go')).toBe('c')                         // 没人 import
    expect(at('native/main.go')).toBe('根')                       // 根包自己改了照旧测
    expect(at('native/a/a_test.go')).toBe('a')                    // 只改测试文件：不带导入方
    expect(at('native/a/testdata/x.json')).toBe('a b w x y')      // testdata 也带：别的包的测试会按路径读它
    expect(at('native/t/t_test.go')).toBe('t')                    // 只有测试文件的包
    expect(at('native/u/u.go')).toBe('a b c u v w x y 根')                // 没有测试文件的包：照旧带全部（含间接、含根包）导入方（ADR-225 的例外）
    expect(at('native/go.mod')).toBe('整套')
    expect(at('docs/x.md')).toBe('')
    expect(at('native/gone/dir/f.go')).toBe('根')                   // 删掉的目录往上找，落到根包
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 120_000)

test('go list 失败（导入环）时 readGraph 抛错，不当成“没有要测的包”', () => {
  const dir = tempModule()
  try {
    writeFileSync(join(dir, 'u', 'u.go'), `package u\n\nimport "${M}/c"\n\nvar U = c.C\n`)   // u → c → b → a → u 成环
    expect(() => readGraph(dir, goEnv())).toThrow(/import cycle/)
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 120_000)

// 共用场景（规格-Bun后台第1批 第八节，复核 2026-11-10 第 1 条）：改 testdata/shared/<目录> 要测读它的 Go 包
test('testdata/shared 映射到读它的包；不认识的目录整套跑；显示规则改了带上 rpcserve', () => {
  const ps = ['internal/rpcserve', 'internal/rpcwire', 'internal/store', 'internal/turn', 'internal/archcheck'].map(P)
  const g2: Graph = { pkgs: ps, deps: new Map(ps.map((p) => [p, new Set<string>()])), imports: new Map(ps.map((p) => [p, new Set<string>()])), bare: new Set() }
  const list = (files: string[], graph = g2) => { const r = pick(files, graph); return r.full ? r : r.pkgs }
  expect(list(['testdata/shared/rpc/paging.json'])).toEqual([P('internal/rpcserve'), P('internal/archcheck')])
  expect(list(['testdata/shared/turn/x.json'])).toEqual([P('internal/turn'), P('internal/archcheck')])
  expect(list(['testdata/shared/新目录/x.json'])).toEqual({ full: true })
  expect(list(['testdata/shared/README.md'])).toEqual([])
  expect(list(['protocol/sumika-rpc.json'])).toEqual([P('internal/rpcserve'), P('internal/rpcwire'), P('internal/archcheck')])
  expect(list(['native/internal/store/display.go'])).toEqual([P('internal/rpcserve'), P('internal/store'), P('internal/archcheck')])
  expect(list(['scripts/server-verify.ts'])).toEqual({ full: true })
  // 连带读到的包（共用场景、协议）不往外带导入方：app 直接 import rpcserve、rpcwire，这些改动不该平白带上 app
  const withApp = [...ps, P('internal/app')]
  const imp = new Map(withApp.map((p) => [p, new Set<string>()]))
  imp.set(P('internal/app'), new Set([P('internal/rpcserve'), P('internal/rpcwire'), P('internal/store')]))
  const g3: Graph = { pkgs: withApp, deps: new Map(withApp.map((p) => [p, new Set<string>()])), imports: imp, bare: new Set() }
  expect(list(['testdata/shared/rpc/paging.json'], g3)).toEqual([P('internal/rpcserve'), P('internal/archcheck')])
  expect(list(['protocol/sumika-rpc.json'], g3)).toEqual([P('internal/rpcserve'), P('internal/rpcwire'), P('internal/archcheck')])
  // store 自己的源码改了，app 是它的直接导入方；显示规则连带的 rpcserve 本身不再往外扩
  expect(list(['native/internal/store/display.go'], g3)).toEqual([P('internal/rpcserve'), P('internal/store'), P('internal/app'), P('internal/archcheck')])
})

// issue #36（2026-11-13）：记忆的两套共用场景只测 memory；race.yml 由 turn 的守卫测试读，改了测 turn；两者都算“Go 会读到的文件”
test('记忆共用场景映射到 memory，race.yml 映射到 turn', () => {
  const ps = ['internal/memory', 'internal/turn', 'internal/app', 'internal/archcheck'].map(P)
  const imp = new Map(ps.map((p) => [p, new Set<string>()]))
  imp.set(P('internal/app'), new Set([P('internal/memory'), P('internal/turn')]))   // 连带读到的不往外带导入方
  const g4: Graph = { pkgs: ps, deps: new Map(ps.map((p) => [p, new Set<string>()])), imports: imp, bare: new Set() }
  const list = (files: string[]) => { const r = pick(files, g4); return r.full ? r : r.pkgs }
  expect(list(['testdata/shared/memory-recall/q.json'])).toEqual([P('internal/memory'), P('internal/archcheck')])
  expect(list(['testdata/shared/memory-write/w.json'])).toEqual([P('internal/memory'), P('internal/archcheck')])
  expect(list(['.github/workflows/race.yml'])).toEqual([P('internal/turn'), P('internal/archcheck')])
  expect(list(['.github/workflows/other.yml'])).toEqual([])
  expect(readsGo('.github/workflows/race.yml')).toBe(true)
  expect(readsGo('.github/workflows/other.yml')).toBe(false)
  expect(readsGo('constructor')).toBe(false)
  expect(list(['testdata/shared/constructor/x.json'])).toEqual({ full: true })   // 原型上的名字不当成已登记的目录
})
