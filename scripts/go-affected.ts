// 合并前、推送前测改了文件的 Go 包本身，加上它们的直接导入方（ADR-225 第 1 条，规格-提交前检查 第二节；ADR-362 加上直接导入方）。
// 挑包规则只在这个文件里实现，go-verify.ts（合并队列、手动）、pre-push.ts 都调 pick()，不各挑各的。
// 挑进来的有四类：
//   1. 改到的包：文件所在目录往上最近的一个包（含 testdata、嵌进去的 sql），共用场景、协议、界面协议输入连带读到的包也算；
//   2. 直接导入方（ADR-362）：源码改了（只改 _test.go 的不算；testdata 算，别的包的测试会按路径读它，例如 linkexpand 读 ../linkreader/testdata）的包，
//      同一个模块里直接 import 它的那些包，只多一层、不递归，根包（native/ 目录本身，桌面程序）不带；
//      直接导入方自己没有测试文件时没有测试可跑，它只靠导入它的包测，所以穿过去，改选最近的有测试的导入方（穿过一串没有测试文件的包为止，ADR-362 决定 3）；
//   3. 自己没有测试文件的包（audience、persona、prompts……只靠引用它的包测）改了，连（含测试代码）直接或间接 import 它的包一起测，
//      这条比第 2 条宽，包括根包，ADR-225 就有，保留；
//   4. 只要挑上了 Go 包，就带上架构测试的包 internal/archcheck（几秒，只跑 go list），它管的是整棵依赖树，哪个包改了都可能踩到。
// 间接导入方、根包、一次性的包交给每天巡检的整套 go test ./...（cmd/ 下有测试文件的工具作为直接导入方会被选上）。引用方编译不过由 go vet ./... 当场拦（go-verify.ts 的 verify 整个模块 vet）。
// 2026-10-31 到 11-01 曾连带测直接或间接 import 它们的包（ADR-218），一次合并测试 CPU 一千多秒，ADR-225 收窄到只测改了的包；
// 2026-11-13 只改 linkreader 合进 main，依赖它的 linkexpand 的对拍没跑到而红，ADR-362 再加回一层直接导入方。
//   bun scripts/go-affected.ts <文件>...     手动看会挑哪些包（路径相对仓库根）
const MODULE = 'sumika-native'
const ARCH = `${MODULE}/internal/archcheck`

// 改了这些就整套跑：Go 的测试会读它们（对拍现读提示词和示例卡、bench 读任务），或者是挑包的逻辑本身。
// web/ 原来也在（2026-10-31），查下来 Go 的测试不读它；2026-11-01 分仓以后它在 sumika-bun 仓库，去掉了
// sumika.example.yml 原来也在：每加一个配置键都要同步改它，每次都整套 179 个包十来分钟（2026-11-13 主人嫌慢），改成下面 EXAMPLE_READERS 只测读它的包
export const FULL = [/^native\/go\.(mod|sum)$/, /^prompts\//, /^characters\//, /^scripts\/bench-t2\//, /^scripts\/(pre-push|go-affected|go-verify|server-verify|merge-queue|gen-rpc|go-shard|nightly)\.ts$/, /^scripts\/(go-shard\.cmd|run-low\.ps1)$/]

// 示例配置由哪些 Go 包的测试读（'' 是根包）：config、desktopread 读内容，app、根包、cmd/sumika-go 认它在不在来找项目根。
// 2026-11-13 grep 全部 _test.go 得出；go-affected.test.ts 的守卫测试每次重扫，有包的测试提到它却不在这里就红，免得新读者漏测
export const EXAMPLE_READERS = ['internal/config', 'internal/desktopread', 'internal/app', '', 'cmd/sumika-go']
export const EXAMPLE = 'sumika.example.yml'

// native/ 以外、由某几个包的测试按路径读的单个文件 → 读它的包（连带读到，不往外带导入方）：
// race.yml 由 internal/turn 的 race_workflow_test.go 守着白名单（issue #36）
export const FILE_READERS: Record<string, string[]> = { [EXAMPLE]: EXAMPLE_READERS, '.github/workflows/race.yml': ['internal/turn'] }

// 仓库根以外、native/ 以外的文件里，Go 的测试会读到的：整套名单加 FILE_READERS。server-verify 判断“这次有没有 Go 会读到的文件”用它，
// 不自己拼名单（2026-11-13 复核：示例配置移出 FULL 后，server-verify 只看 FULL，改 server/ 加示例配置就把 Go 整个跳过了）
export const readsGo = (f: string) => FULL.some((re) => re.test(f)) || Object.hasOwn(FILE_READERS, f)

// testdata/shared/<目录> 由哪些 Go 包的测试读（各目录 README 的"Go 的跑法"）。新加目录不在表里就整套跑，免得漏测（规格-Bun后台第1批 第八节）
export const SHARED_READERS: Record<string, string[]> = {
  backfill: ['internal/app'],
  turn: ['internal/turn'],
  'qq-frames': ['internal/channel/snowluma'],
  'qq-intake': ['internal/channel'],
  'qq-send': ['internal/channel'],
  rpc: ['internal/rpcserve'],
  // 记忆召回考题、写入场景（README 的 Go 读者：internal/memory 的 shared_recall_test.go、shared_write_test.go；issue #36，2026-11-13）
  'memory-recall': ['internal/memory'],
  'memory-write': ['internal/memory'],
}

// 这些包改了，界面协议的共用场景（rpcserve 的 TestSharedRPC）可能跟着变：带上 rpcserve
const RPC_INPUTS = ['internal/store', 'internal/desktopread', 'internal/rpcconv', 'internal/rpcwire', 'internal/rpcclient']

// pkgs：要测的包（含 archcheck）；changed：其中“改到的”（含连带读到的）；importers：其中因为导入关系带上的（直接导入方和没有测试文件的包的导入方），不含 archcheck
export type Pick = { full: true } | { full: false; pkgs: string[]; changed: string[]; importers: string[] }

// pkgs：模块里所有包；deps：包 → 它（含 _test.go 和外部测试包）依赖的全部包（传递）；
// imports：包 → 它（含 _test.go 和外部测试包）直接 import 的包，不含自己；bare：自己没有测试文件的包
export type Graph = { pkgs: string[]; deps: Map<string, Set<string>>; imports: Map<string, Set<string>>; bare: Set<string> }

// 源码有没有改：_test.go 只有这个包自己的测试会编进去，别的包不受影响；其余文件都算，含嵌进去的 sql、模板，也含 testdata
// （别的包的测试会按路径读它：linkexpand/mall_test.go 读 ../linkreader/testdata，localmodels 的 engine、pool 读 ../ort/testdata；2026-11-13 查过）
const isSource = (rel: string) => !rel.endsWith('_test.go')

export function pick(files: string[], g: Graph): Pick {
  const pkgs = g.pkgs
  if (files.some((f) => FULL.some((re) => re.test(f)))) return { full: true }
  const all = new Set(pkgs)
  const changed = new Set<string>()   // 要测的（改到的，和共用场景、协议连带读到的）
  const edited = new Set<string>()    // 其中源码真的改了的：只有这些往外带一层直接导入方
  for (const f of files) {
    if (f.startsWith('testdata/shared/')) {
      const dir = f.split('/')[2] ?? ''
      const readers = Object.hasOwn(SHARED_READERS, dir) ? SHARED_READERS[dir] : undefined   // constructor、__proto__ 这类目录名不能取到原型上的东西
      if (!readers) {
        if (f.split('/').length > 3) return { full: true }   // 不认识的共用目录
        continue                                             // testdata/shared/README.md 这类说明
      }
      for (const r of readers) if (all.has(`${MODULE}/${r}`)) changed.add(`${MODULE}/${r}`)
      continue
    }
    if (f.startsWith('protocol/')) {
      for (const r of ['internal/rpcwire', 'internal/rpcserve']) if (all.has(`${MODULE}/${r}`)) changed.add(`${MODULE}/${r}`)
      continue
    }
    if (Object.hasOwn(FILE_READERS, f)) {   // 连带读到的包，不往外带导入方（同共用场景）
      for (const r of FILE_READERS[f]!) { const p = r ? `${MODULE}/${r}` : MODULE; if (all.has(p)) changed.add(p) }
      continue
    }
    if (!f.startsWith('native/')) continue
    // 文件所在目录往上找最近的一个包：testdata、嵌进去的 sql 都算它所属的包；删掉的目录照样往上找
    const rel = f.slice('native/'.length)
    let dir = rel.split('/').slice(0, -1)
    for (;;) {
      const p = dir.length ? `${MODULE}/${dir.join('/')}` : MODULE
      if (all.has(p)) { changed.add(p); if (isSource(rel)) edited.add(p); break }
      if (!dir.length) return { full: true }   // 模块根不是包时才会走到这里（现在根包就是桌面程序，native/ 下的文件最后都落到它）
      dir = dir.slice(0, -1)
    }
  }
  if (RPC_INPUTS.some((r) => changed.has(`${MODULE}/${r}`)) && all.has(`${MODULE}/internal/rpcserve`)) changed.add(`${MODULE}/internal/rpcserve`)
  const bare = [...changed].filter((p) => g.bare.has(p))
  // 直接导入方：只对源码改了的包求，一层；根包不带。没有测试文件的导入方没有测试可跑，改成穿过它选导入它的包，直到碰到有测试的包为止
  const direct = new Set<string>()
  const seen = new Set<string>(edited)
  for (let frontier = [...edited]; frontier.length;) {
    const next: string[] = []
    for (const p of pkgs) {
      if (p === MODULE || seen.has(p) || !frontier.some((e) => g.imports.get(p)?.has(e))) continue
      seen.add(p)
      if (g.bare.has(p)) next.push(p)
      else direct.add(p)
    }
    frontier = next
  }
  const out = pkgs.filter((p) => changed.has(p) || direct.has(p) || bare.some((b) => g.deps.get(p)?.has(b)))
  const changedOut = out.filter((p) => changed.has(p))
  const importers = out.filter((p) => !changed.has(p) && p !== ARCH)
  if (out.length && pkgs.includes(ARCH) && !out.includes(ARCH)) out.push(ARCH)
  return { full: false, pkgs: out, changed: changedOut, importers }
}

// 给人和合并说明看的“挑了什么”：改到的 N 个包 + 导入方 M 个 + archcheck
export function describePick(r: Pick) {
  if (r.full) return '整套'
  if (!r.pkgs.length) return '没有要测的包'
  const arch = r.pkgs.includes(ARCH) && !r.changed.includes(ARCH) ? ' + archcheck' : ''
  return `改到的 ${r.changed.length} 个包 + 导入方 ${r.importers.length} 个${arch}`
}

// go list -test 会给每个包列出本身、"包 [包.test]"、"包_test [包.test]"、"包.test" 几条，合到同一个包名下
export function readGraph(cwd: string, env?: Record<string, string | undefined>): Graph {
  const run = (args: string[]) => {
    const p = Bun.spawnSync(['go', ...args], { cwd, env })
    if (p.exitCode !== 0) throw new Error(`go ${args.join(' ')}：${p.stderr.toString()}`)
    return p.stdout.toString().split(/\r?\n/).filter(Boolean)
  }
  const pkgs: string[] = []
  const bare = new Set<string>()
  for (const line of run(['list', '-f', '{{.ImportPath}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}', './...'])) {
    const [p, a, b] = line.split('|')
    pkgs.push(p)
    if (a === '0' && b === '0') bare.add(p)
  }
  const deps = new Map<string, Set<string>>()
  const imports = new Map<string, Set<string>>()
  const inModule = (d: string) => d === MODULE || d.startsWith(`${MODULE}/`)   // 带 "[x.test]" 的后缀拆成了单独的词，不以模块名开头，自然被滤掉
  for (const line of run(['list', '-test', '-f', '{{.ImportPath}}|{{join .Deps " "}}|{{join .Imports " "}}', './...'])) {
    const [id, ds = '', is = ''] = line.split('|')
    const name = id.split(' [')[0].replace(/\.test$/, '').replace(/_test$/, '')
    const set = deps.get(name) ?? new Set<string>()
    for (const d of ds.split(' ')) if (inModule(d)) set.add(d)
    deps.set(name, set)
    // p.test 导入 p 和 p_test、p_test 导入 p，归并到同一个名字后都是指向自己的，去掉（p_test 先还原成 p 再比）
    const imp = imports.get(name) ?? new Set<string>()
    for (const d of is.split(' ')) if (inModule(d) && d.replace(/_test$/, '') !== name) imp.add(d)
    imports.set(name, imp)
  }
  return { pkgs, deps, imports, bare }
}

// 手动输入的路径：Windows 的反斜杠、开头的 ./ 换成 git diff --name-only 的写法（队列、推送钩子给的本来就是这种）
export const normalizePath = (f: string) => f.replaceAll('\\', '/').replace(/^(\.\/)+/, '')

if (import.meta.main) {
  const r = pick(process.argv.slice(2).map(normalizePath), readGraph('native'))
  console.log(r.full ? '整套' : r.pkgs.length ? r.pkgs.join('\n') : '（没有要测的包）')
  if (!r.full && r.pkgs.length) console.log(`—— ${describePick(r)}，共 ${r.pkgs.length} 个包`)
}
