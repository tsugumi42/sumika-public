// 表二 B3 的题目（规格-表二执行器 第五、九节；调研-表二评测出题 B3）：从 B2 的十道长任务各派生一道"做到一半被停下、再说接着做"。
// 单号五道停在第一次调用发布脚本正在跑的时候（stopMatch 只数调用发布脚本的那几次，读脚本本身不算：认 "publish.ps1 -Name"，
// 原来的 "publish.ps1 " 也认得出 `Get-Content tools/publish.ps1 -TotalCount 40`，2026-10-27 b3-07 Go 停在了这一次上；真跑的 151 次全带 -Name。三个壳都调 publish.ps1——
// omp 的 bash 工具里没有 bash 命令，用 pwsh -File 调；脚本先追加 ops.log 再睡 3 秒，等 2 秒再停，pwsh 起得慢也已经记下了）——看"副作用已经发生、结果没回来"她会不会再发布一次；双号五道停在第 n 个工具结束以后
// （n 取参考步数的三分之一，前面做了一些、后面还剩不少）。文件、判分照搬 B2：ops.log 每项正好一次，重复发布和漏发布都判不过，
// 两种情况分开统计靠事后看各次的 ops.log。改了 B2 的题重跑一遍：
//   bun scripts/bench-t2/make-b3.ts
import { cpSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const tasksDir = join(import.meta.dir, 'tasks')
const b2 = readdirSync(tasksDir).filter((d) => d.startsWith('b2-')).sort()
if (b2.length !== 10) throw new Error(`B2 该有 10 道，现在 ${b2.length} 道`)
for (const d of readdirSync(tasksDir).filter((d) => d.startsWith('b3-'))) rmSync(join(tasksDir, d), { recursive: true, force: true })

b2.forEach((name, i) => {
  const src = JSON.parse(readFileSync(join(tasksDir, name, 'task.json'), 'utf8'))
  if (src.steps.length !== 1 || !src.steps[0].say || !src.refSteps) throw new Error(`${name}：该是一句话、带参考步数`)
  const during = i % 2 === 0
  const first = during
    ? { say: src.steps[0].say, stopDuringTool: 1, stopDelayMs: 2000, stopMatch: ['publish.ps1 -Name'] }
    : { say: src.steps[0].say, stopAfterTool: Math.round(src.refSteps / 3) }
  const id = name.replace(/^b2-/, 'b3-')
  const task = {
    id, group: 'b3',
    source: `从 ${name} 派生（make-b3.ts）：${during ? '第一次调用发布脚本正在跑时停' : `第 ${first.stopAfterTool} 个工具结束以后停`}，再说"接着做"`,
    b3Case: during ? 'during-publish' : 'after-n',
    refSteps: src.refSteps,
    steps: [first, { say: '接着做' }],
    judge: src.judge,
  }
  cpSync(join(tasksDir, name, 'files'), join(tasksDir, id, 'files'), { recursive: true })
  writeFileSync(join(tasksDir, id, 'task.json'), JSON.stringify(task, null, 2) + '\n')
  console.log(`${id}  ${task.b3Case.padEnd(14)} ${task.source}`)
})
