// 读 `go test -json` 的输出，在 CI 日志里打出：每个包的结论、红了的用例的原文、按原因汇总的跳过清单；
// 还有一道检查：缺依赖（字体、表情、rg/fd、ffmpeg、中文字体）而跳过的用例，在 CI 上判红——CI 已经装齐了这些，
// 再跳过就是装的那一步坏了，不能悄悄少测（2026-11-19：推特卡片真字体 13 条、表情 2 条在 CI 两格上一直被跳过，没人看见）。
//
//   go test -short -json ./... > go-test.json; bun scripts/ci-test-report.ts go-test.json [存一份的文件]
//   第二个参数可选：打出来的内容（含 ::error:: 行）再原样写进这个文件，UTF-8，给 nightly.yml 传成附件
//
// 退出码：只看缺依赖那道检查（有就 1，没有就 0）；go test 自己红不红由调用方照 go test 的退出码判，这里不重复。
import { readFileSync, writeFileSync } from 'node:fs'

/** CI 上必须具备的依赖：跳过原因里出现这些说法，说明 CI 装依赖那一步没生效。说法照各测试的 t.Skip 原文，改了那边同步改这里 */
export const MUST_HAVE: { need: string; match: RegExp }[] = [
  { need: '文渊圆体（scripts/ci-assets.ts）', match: /找不到文渊圆体|本机没有文渊圆体/ },
  { need: 'Twemoji（scripts/ci-assets.ts）', match: /找不到 Twemoji|本机没有 Twemoji/ },
  { need: 'rg、fd（scripts/fetch-tools.ts）', match: /vendor\/bin 和 PATH 里都没有|要 ripgrep（rg）在 PATH/ },
  { need: 'ffmpeg、ffprobe', match: /没有 ffmpeg/ },
  { need: '中文字体', match: /画不出中文字/ },
]

type Event = { Action?: string; Package?: string; Test?: string; Output?: string; Elapsed?: number }
type Entry = { action?: string; output: string[]; elapsed?: number }
export type Report = { tests: Map<string, Entry>; packages: Map<string, Entry> }

const key = (pkg: string, test: string) => `${pkg}\t${test}`

export function parse(text: string): Report {
  const tests = new Map<string, Entry>()
  const packages = new Map<string, Entry>()
  for (const line of text.split(/\r?\n/)) {
    if (!line.startsWith('{')) continue
    let e: Event
    try {
      e = JSON.parse(line)
    } catch {
      continue
    }
    if (!e.Package) continue
    const map = e.Test ? tests : packages
    const k = e.Test ? key(e.Package, e.Test) : e.Package
    let entry = map.get(k)
    if (!entry) map.set(k, (entry = { output: [] }))
    if (e.Action === 'output' || e.Action === 'build-output') entry.output.push(e.Output ?? '')
    else if (e.Action === 'pass' || e.Action === 'fail' || e.Action === 'skip') {
      entry.action = e.Action
      entry.elapsed = e.Elapsed
    }
  }
  return { tests, packages }
}

/** 跳过原因：用例自己打的最后一行话，去掉 `文件.go:行号: ` 前缀；没写原因的记“（没写原因）” */
export function skipReason(output: string[]): string {
  const lines = output.map((l) => l.trim()).filter((l) => l && !/^(=== (RUN|PAUSE|CONT|NAME)|--- SKIP)/.test(l))
  const last = lines.at(-1)
  return last ? last.replace(/^[\w.-]+\.go:\d+: /, '') : '（没写原因）'
}

export function summarize(r: Report) {
  const skips = new Map<string, string[]>()
  const failed: { name: string; output: string[] }[] = []
  for (const [k, v] of r.tests) {
    const [pkg, test] = k.split('\t') as [string, string]
    const name = `${pkg} ${test}`
    if (v.action === 'skip') {
      const why = skipReason(v.output)
      skips.set(why, [...(skips.get(why) ?? []), name])
    } else if (v.action === 'fail') failed.push({ name, output: v.output })
  }
  const missing = MUST_HAVE.map((m) => ({ need: m.need, tests: [...skips].filter(([why]) => m.match.test(why)).flatMap(([, ts]) => ts) })).filter((m) => m.tests.length > 0)
  return { skips, failed, missing }
}

if (import.meta.main) {
  const file = process.argv[2]
  if (!file) {
    console.error('用法：bun scripts/ci-test-report.ts <go test -json 的输出文件> [存一份的文件]')
    process.exit(2)
  }
  const r = parse(readFileSync(file, 'utf8'))
  const s = summarize(r)
  const saved: string[] = []
  const log = (t: string) => { console.log(t); saved.push(t) }
  const raw = (t: string) => { process.stdout.write(t); saved.push(t.replace(/\n$/, '')) }
  const err = (t: string) => { console.error(t); saved.push(t) }
  // 包的结论，和不带 -json 时 go test 打的那几行一样
  for (const [pkg, v] of [...r.packages].sort(([a], [b]) => a.localeCompare(b))) {
    const t = v.elapsed !== undefined ? `\t${v.elapsed.toFixed(3)}s` : ''
    if (v.action === 'fail') log(`FAIL\t${pkg}${t}`)
    else if (v.action === 'pass') log(`ok  \t${pkg}${t}`)
    else if (v.action === 'skip') log(`?   \t${pkg}\t[no test files]`)
  }
  // 红了的包：包自己的输出（编译错、panic、超时打出的协程栈都在这里），再是每条红了的用例的原文
  for (const [pkg, v] of r.packages) {
    if (v.action !== 'fail') continue
    log(`\n::group::${pkg} 包的输出`)
    raw(v.output.join(''))
    log('::endgroup::')
  }
  for (const f of s.failed) {
    log(`\n::group::红：${f.name}`)
    raw(f.output.join(''))
    log('::endgroup::')
  }
  const total = [...s.skips.values()].reduce((n, ts) => n + ts.length, 0)
  log(`\n跳过了 ${total} 条，按原因：`)
  for (const [why, ts] of [...s.skips].sort((a, b) => b[1].length - a[1].length)) {
    log(`  ${String(ts.length).padStart(4)}  ${why}`)
    for (const t of ts.slice(0, 3)) log(`          ${t}`)
    if (ts.length > 3) log(`          ……还有 ${ts.length - 3} 条`)
  }
  const done = (code: number) => {
    if (process.argv[3]) writeFileSync(process.argv[3], saved.join('\n') + '\n')
    process.exit(code)
  }
  if (s.missing.length) {
    for (const m of s.missing) err(`::error::缺 ${m.need}，这 ${m.tests.length} 条被跳过了：${m.tests.slice(0, 5).join('、')}${m.tests.length > 5 ? ' ……' : ''}`)
    done(1)
  }
  done(0)
}
