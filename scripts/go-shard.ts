// go test -exec 的启动器：最慢的几个包的测试程序按测试名分成几份，各开一个进程并行跑；其余的包原样跑。
//   go test -exec "bun D:/.../scripts/go-shard.ts" -short -count=1 ./...
// 为什么分进程而不是 t.Parallel：这些测试共用不少包级变量（退避、轮询间隔、缓存），分进程不用逐条排查，互相也碰不到。
// -test.run、-test.list 这些已经指定了范围的不分，照原样跑。
// 关掉：SUMIKA_ROOT_SHARDS=1。
// 怎么分（2026-11-12 改）：每份带 -test.v 跑，从“--- PASS: TestX (1.23s)”记下每条顶层测试的耗时，存在共用 git 目录的
// sumika-go-test-times.json；下次按耗时分——慢的先分、每次给当前最空的一份（最长处理时间优先，LPT）。没记过的测试按已知的中位数算。
// 以前按名字轮流发牌，几条 20 多秒的大测试凑到同一份，整包要等这一份（根包 240 秒、app 115 秒）。

import { existsSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { isAbsolute, join, win32 } from 'node:path'

// 只分慢包（2026-11-09 不挤时量的串行耗时：根包 140 秒、memory 90 秒、turn 65 秒）；别的包不值得。
// app 2026-11-12 改成包内 t.Parallel 后单进程 42～46 秒，再分进程没有额外收益（实测 52 秒），从名单里拿掉
const SHARDED_BINARY = /(^|[\\/])sumika-native\.test(\.exe)?$/   // 根包：界面测试没法包内并行（mygo#1722）；turn、memory 2026-11-12 包内并行后不分了

/** 这次要不要分：是最慢的那几个包、没有自己指定范围、份数大于 1。 */
export function shouldShard(binary: string, args: string[], shards: number) {
  if (shards <= 1 || !SHARDED_BINARY.test(binary)) return false
  return !args.some((a) => /^-test\.(run|list|bench|fuzz|skip)(=|$)/.test(a))
}

/** 分份。没给耗时：按名字排好序轮流发牌（名字相近的常常一样慢，被摊开）。给了耗时：慢的先分，每条给当前总耗时最少的一份；
 *  没记过的按已知耗时的中位数算；耗时相同按名字排，同一份记录每次分法一样。 */
export function partition(names: string[], shards: number, times?: Record<string, number>) {
  const out: string[][] = Array.from({ length: shards }, () => [])
  const known = times ? names.map((n) => times[n]).filter((t): t is number => typeof t === 'number' && t >= 0).sort((a, b) => a - b) : []
  if (!known.length) {
    ;[...names].sort().forEach((n, i) => out[i % shards].push(n))
    return out.filter((b) => b.length > 0)
  }
  const median = known[Math.floor(known.length / 2)]
  const cost = (n: string) => (typeof times![n] === 'number' && times![n] >= 0 ? times![n] : median)
  const load = new Array(shards).fill(0)
  ;[...names].sort((a, b) => cost(b) - cost(a) || (a < b ? -1 : a > b ? 1 : 0)).forEach((n) => {
    let i = 0
    for (let k = 1; k < shards; k++) if (load[k] < load[i]) i = k
    out[i].push(n); load[i] += cost(n)
  })
  return out.filter((b) => b.length > 0).map((b) => b.sort())
}

/** 份数：环境变量优先；默认核数减 2（留两核给系统和别的进程），至少 1。go test 是 -p 1 一个包一个包跑这几个慢包的 */
export function shardCount(env: Record<string, string | undefined>, cores: number) {
  const set = Number(env.SUMIKA_ROOT_SHARDS)
  if (Number.isInteger(set) && set > 0) return set
  return Math.max(1, cores - 2)
}

/** 从 -test.v 的输出里取顶层测试的耗时（子测试行有缩进，不取） */
export function parseTimes(out: string): Record<string, number> {
  const t: Record<string, number> = {}
  for (const m of out.matchAll(/^\x16?--- (?:PASS|FAIL|SKIP): (Test\S*) \(([\d.]+)s\)/gm)) t[m[1]] = Number(m[2])
  return t
}

/** 测试程序叫 sumika-native.test.exe、app.test.exe……记录按去掉 .test(.exe) 的名字分开存 */
// win32.basename 认 \ 和 / 两种分隔符：Windows 的 x.test.exe 和 Linux 的 /…/x.test 都取得到文件名
export const binaryKey = (binary: string) => win32.basename(binary).replace(/\.test(\.exe)?$/, '')

type TimesFile = Record<string, Record<string, number>>
/** 记录放在共用的 git 目录（所有 worktree 和队列共用一份）；拿不到 git 目录就不记、不读 */
export function timesPath(cwd: string): string | undefined {
  const r = Bun.spawnSync(['git', 'rev-parse', '--git-common-dir'], { cwd, stdout: 'pipe', stderr: 'pipe' })
  if (r.exitCode !== 0) return undefined
  const d = r.stdout.toString().trim()
  return d ? join(isAbsolute(d) ? d : join(cwd, d), 'sumika-go-test-times.json') : undefined
}
export function loadTimes(path: string | undefined): TimesFile {
  if (!path || !existsSync(path)) return {}
  try { const v = JSON.parse(readFileSync(path, 'utf8')); return v && typeof v === 'object' ? v : {} } catch { return {} }
}
/** 合进去再写：这次跑到的覆盖旧值，没跑到的留着；先写临时文件再改名，几个包同时写也不会写出半个文件 */
export function saveTimes(path: string | undefined, key: string, fresh: Record<string, number>) {
  if (!path || !Object.keys(fresh).length) return
  try {
    const all = loadTimes(path)
    all[key] = { ...(all[key] ?? {}), ...fresh }
    const tmp = `${path}.${process.pid}.tmp`
    writeFileSync(tmp, JSON.stringify(all)); renameSync(tmp, path)
  } catch { /* 记不下只会让下次分得没那么匀 */ }
}

async function main() {
  const [binary, ...args] = process.argv.slice(2)
  if (!binary) {
    console.error('用法：go-shard.ts <测试程序> [参数…]（由 go test -exec 调用）')
    process.exit(2)
  }
  const shards = shardCount(process.env, navigator.hardwareConcurrency)
  if (!shouldShard(binary, args, shards)) {
    const p = Bun.spawn([binary, ...args], { stdio: ['inherit', 'inherit', 'inherit'] })
    process.exit(await p.exited)
  }
  const listed = Bun.spawnSync([binary, '-test.list', '.*'])
  if (listed.exitCode !== 0) {
    process.stdout.write(listed.stdout)
    process.stderr.write(listed.stderr)
    process.exit(listed.exitCode ?? 1)
  }
  // 只分顶层的 Test：Example、Benchmark 根包没有，有了也不该被悄悄漏掉，所以数量对不上就退回不分
  const lines = listed.stdout.toString().split(/\r?\n/).filter(Boolean)
  const names = lines.filter((n) => n.startsWith('Test'))
  if (names.length !== lines.length || names.length === 0) {
    const p = Bun.spawn([binary, ...args], { stdio: ['inherit', 'inherit', 'inherit'] })
    process.exit(await p.exited)
  }
  const store = timesPath(process.cwd())
  const key = binaryKey(binary)
  const known = loadTimes(store)[key]
  // 要记耗时就得 -test.v；go test -json 已经给了 -test.v=test2json，不再加
  const hasV = args.some((a) => /^-test\.v(=|$)/.test(a))
  const extra = hasV ? [] : ['-test.v']
  const runs = partition(names, shards, known).map(async (part) => {
    // 输出攒到结束再写，几份交错在一起没法看
    const p = Bun.spawn([binary, ...args, ...extra, `-test.run=^(${part.join('|')})$`], { stdout: 'pipe', stderr: 'pipe', stdin: 'ignore' })
    const [out, err, code] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text(), p.exited])
    return { out, err, code }
  })
  const verbose = args.some((a) => a === '-test.v' || a === '-test.v=true')
  let exit = 0
  const results = await Promise.all(runs)
  saveTimes(store, key, Object.assign({}, ...results.map((r) => parseTimes(r.out))))
  for (const r of results) {
    // 全过的份只有一行 PASS，不用写；红的份和 -v 要看明细的完整写出来
    if (r.code !== 0 || verbose) {
      process.stdout.write(r.out)
      process.stderr.write(r.err)
    }
    if (r.code !== 0) exit = r.code || 1
  }
  if (exit === 0) console.log('PASS')
  process.exit(exit)
}

if (import.meta.main) await main()
