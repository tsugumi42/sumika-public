// 开发中跑测试的入口：排全机名额、最低优先级、叫它的进程没了整串收掉（都是 scripts/run-low.ps1 做的）。
//   bun ../scripts/slot.ts go test -short -count=1 ./internal/app   （在 native/ 里跑；默认 dev 档，go test 自动限 -p 到核数一半）
//   bun ../scripts/slot.ts bun test ./test/a.test.ts                （在 server/ 里跑）
// 为什么要它：SUMIKA_TEST_LANE 只有经过 run-low 才有人读；直接敲 go test / bun test 等于绕开名额，
// 几个会话、代理和合并队列会叠着跑满 CPU（2026-11-12 夜里三样重活同时跑，墙钟类测试大片误红）。
// 档位照环境变量 SUMIKA_TEST_LANE（verify | dev），默认 dev。参数不能带空格（run-low 经 Start-Process 传参）。
import { join } from 'node:path'

const args = process.argv.slice(2)
if (!args.length) { console.error('用法：bun scripts/slot.ts <命令> [参数…]，比如在 native/ 里 bun ../scripts/slot.ts go test ./internal/app'); process.exit(2) }
if (args.some((a) => /\s/.test(a))) { console.error('slot：参数里不能有空格（run-low 经 Start-Process 传参会拆开），先写成脚本再交给它'); process.exit(2) }
const p = Bun.spawn(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', join(import.meta.dir, 'run-low.ps1'), ...args], {
  stdio: ['inherit', 'inherit', 'inherit'],
  env: { ...process.env, SUMIKA_TEST_LANE: process.env.SUMIKA_TEST_LANE || 'dev' },
})
process.exit(await p.exited)
