// 量 Go 测试的耗时：每个包多久、最慢的是哪几条，对照 docs/规范-测试耗时.md 的预算。
//   bun scripts/go-test-times.ts                          native/ 下全部包（带 -short，低优先级）
//   bun scripts/go-test-times.ts ./internal/app .         只量这几个包
//   bun scripts/go-test-times.ts --from 文件.jsonl        读已经存好的 go test -json 输出，不重跑
// 原始输出存到主工作区 data/dev/test-runs/，结果写在终端。超预算的包退出码仍是 0：这是量具，不是卡口。

export const PACKAGE_BUDGET_SECONDS = 60
export const TEST_BUDGET_SECONDS = 3

export type TestEvent = { Action?: string; Package?: string; Test?: string; Elapsed?: number }

export type Timing = {
  packages: { name: string; seconds: number; failed: boolean }[]
  tests: { name: string; pkg: string; seconds: number }[]
}

/** 读 go test -json 的一行行事件，只留结束事件。子测试不单算：顶层那条已经包含它们。 */
export function collect(lines: string[]): Timing {
  const packages: Timing['packages'] = []
  const tests: Timing['tests'] = []
  for (const line of lines) {
    if (!line.startsWith('{')) continue
    let e: TestEvent
    try {
      e = JSON.parse(line)
    } catch {
      continue
    }
    if (e.Action !== 'pass' && e.Action !== 'fail') continue
    if (!e.Package || e.Elapsed === undefined) continue
    if (!e.Test) packages.push({ name: e.Package, seconds: e.Elapsed, failed: e.Action === 'fail' })
    else if (!e.Test.includes('/')) tests.push({ name: e.Test, pkg: e.Package, seconds: e.Elapsed })
  }
  packages.sort((a, b) => b.seconds - a.seconds)
  tests.sort((a, b) => b.seconds - a.seconds)
  return { packages, tests }
}

/** 超预算的包、超预算的测试。 */
export function overBudget(t: Timing) {
  return {
    packages: t.packages.filter((p) => p.seconds > PACKAGE_BUDGET_SECONDS),
    tests: t.tests.filter((x) => x.seconds > TEST_BUDGET_SECONDS),
  }
}

function short(pkg: string) {
  return pkg.replace(/^sumika-native\/?/, '') || '（根包）'
}

export function report(t: Timing, top = 25) {
  const over = overBudget(t)
  const out: string[] = []
  const total = t.packages.reduce((n, p) => n + p.seconds, 0)
  out.push(`一共 ${t.packages.length} 个包，${t.tests.length} 条顶层测试，串行相当于 ${total.toFixed(0)} 秒`)
  out.push(`超过 ${PACKAGE_BUDGET_SECONDS} 秒的包 ${over.packages.length} 个，超过 ${TEST_BUDGET_SECONDS} 秒的测试 ${over.tests.length} 条`)
  out.push('', '最慢的包：')
  for (const p of t.packages.slice(0, 12)) out.push(`  ${p.seconds.toFixed(1).padStart(7)}  ${short(p.name)}${p.failed ? '（红）' : ''}`)
  out.push('', `最慢的 ${top} 条测试：`)
  for (const x of t.tests.slice(0, top)) out.push(`  ${x.seconds.toFixed(1).padStart(7)}  ${short(x.pkg)}  ${x.name}`)
  return out.join('\n')
}

async function main() {
  const args = process.argv.slice(2)
  const from = args.indexOf('--from')
  if (from >= 0) {
    const text = await Bun.file(args[from + 1]).text()
    console.log(report(collect(text.split('\n'))))
    return
  }
  const pkgs = args.length > 0 ? args : ['./...']
  const repo = new URL('..', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1')
  const stamp = new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
  const main = process.env.SUMIKA_MAIN_WORKTREE || repo
  const outPath = `${main}data/dev/test-runs/times-${stamp}.jsonl`
  const proc = Bun.spawn(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', `${repo}scripts/run-low.ps1`, 'go', 'test', '-short', '-count=1', '-json', ...pkgs], {
    cwd: `${repo}native`,
    stdout: 'pipe',
    stderr: 'inherit',
    stdin: 'ignore',
  })
  const text = await new Response(proc.stdout).text()
  await Bun.write(outPath, text)
  await proc.exited
  console.log(report(collect(text.split('\n'))))
  console.log(`\n原始输出：${outPath}`)
}

if (import.meta.main) await main()
