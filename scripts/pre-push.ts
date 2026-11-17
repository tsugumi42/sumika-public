// 推送前的检查（规格-提交前检查 第二节）：git 的 pre-push 钩子从标准输入给"本地 ref、本地提交、远端 ref、远端提交"，按这次要推的范围挑检查。
// lefthook.yml 里 use_stdin 把输入转给这里。每个检查用它自己的退出码，有一个红就整体退出码 1、推不上去。
//   echo "refs/heads/main <本地提交> refs/heads/main <远端提交>" | bun scripts/pre-push.ts     手动试
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { describePick, pick, readGraph } from './go-affected'
import { cleanTree, needsRunPlanned, PLANNED_PATHS, plan, runPlanned } from './server-verify'
import { git, goEnv, goFingerprint, goTree, loadVerified, uncovered, verify, verifyMygo, when } from './go-verify'

const ZERO = /^0+$/
const env = goEnv()
const root = process.cwd()
const run = (cmd: string[], cwd?: string) => {
  const t = Date.now()
  const p = Bun.spawnSync(cmd, { cwd, env, stdout: 'inherit', stderr: 'inherit' })
  console.log(`  ${p.exitCode === 0 ? '过' : '红'}（退出码 ${p.exitCode}，${((Date.now() - t) / 1000).toFixed(1)} 秒）：${cmd.join(' ')}`)
  return p.exitCode === 0
}

const input = await Bun.stdin.text()
let ok = true
for (const line of input.split(/\r?\n/).filter(Boolean)) {
  const [, local, , remote] = line.split(/\s+/)
  if (!local || ZERO.test(local)) continue   // 删远端分支
  // 远端还没有这个分支：只看远端哪个分支都没有的那些提交
  const base = !remote || ZERO.test(remote) ? git(['merge-base', local, 'origin/main']) || local : remote
  const range = `${base}..${local}`
  const files = git(['diff', '--name-only', '--no-renames', base, local]).split(/\r?\n/).filter(Boolean)
  console.log(`推送前检查：${range}，${git(['rev-list', '--count', range])} 个提交、${files.length} 个文件`)
  ok = run(['bun', 'scripts/scan-private.ts', '--range', range]) && ok
  // 按改了哪些路径挑 Bun 后台和协议的检查（规格-Bun后台第1批 第八节）；只改 server/ 时不跑 Go
  const pl = plan(files)
  if (needsRunPlanned(pl)) {
    // Bun 这几步测的是工作区：工作区和要推的提交在这几处不一样时，测出来的不是要推的代码，照 go-verify 的做法停下
    // 公开导出的仓库没有 server/、desktop/：不存在的路径不列出来，免得提示里出现根本没有的目录
    const planned = [...PLANNED_PATHS].filter((p) => existsSync(join(root, p)))
    const dirty = [git(['diff', '--name-only', local, '--', ...planned, 'scripts']), git(['ls-files', '--others', '--exclude-standard', '--', ...planned, 'scripts'])].filter(Boolean).join('\n')
    if (dirty) { console.log(`工作区里有 Bun 那几步会读到、还没提交的改动，测不到要推的 ${local.slice(0, 8)}。先提交或收起改动再推：\n${dirty}`); ok = false; continue }
    // 合并队列在同一棵树上跑过的步骤不再跑（SUMIKA_PUSH_FULL=1 照跑）：工作区就是要推的这棵树时才认、才记
    const steps = runPlanned(pl, root, env, undefined, process.env.SUMIKA_PUSH_FULL === '1' ? {} : { tree: cleanTree(root, local), by: 'pre-push' })
    if (steps.some((s) => !s.ok)) ok = false
  }
  if (!pl.go && process.env.SUMIKA_PUSH_FULL !== '1') { console.log('Go：没有 Go 改动，跳过 vet、test、archcheck'); continue }
  // 合并提交不走 pre-commit，合并后的结果只有这里查得到。go vet 整个模块，go test 测改了文件的包和它们的直接导入方（ADR-225 第 1 条、ADR-362，
  // 挑包规则只在 go-affected.ts）；间接导入方、根包和整套交给每天的巡检，手动要整套设 SUMIKA_PUSH_FULL=1。
  // 合并前 go-verify 在同一份代码（Go 会读到的那几处）、同一套 go 上验过的包不再跑（ADR-223）；要跑的一律 -count=1，原因见 go-verify.ts 开头
  const graph = readGraph('native', env)
  const all = graph.pkgs
  const pushFull = process.env.SUMIKA_PUSH_FULL === '1'
  const picked = pushFull ? { full: true as const } : pick(files, graph)
  const pkgs = picked.full ? all : picked.pkgs
  console.log(picked.full ? `Go：整套 ${pkgs.length} 个包` : `Go：${describePick(picked)}，合计 ${pkgs.length} 个包（共 ${all.length} 个）`)
  const mygo = verifyMygo(files, env, { commit: local, by: 'pre-push' })
  ok = (mygo?.ok ?? true) && ok
  if (pkgs.length && (mygo?.ok ?? true)) {
    const tree = goTree(local)
    const records = loadVerified()
    const go = goFingerprint(env)
    const need = uncovered(records, tree, go, pkgs, pushFull)
    const seen = records.filter((r) => r.tree === tree && r.go === go && !(pushFull && r.short)).map((r) => `${r.by} ${when(r.at)}${r.short ? '，-short' : ''}`)
    if (!need.length) console.log(`  这 ${pkgs.length} 个包已在同一份代码、同一套 go 上验过（${seen.join('；')}），不再跑`)
    else {
      if (need.length < pkgs.length) console.log(`  其中 ${pkgs.length - need.length} 个已在同一份代码上验过（${seen.join('；')}），跑剩下的 ${need.length} 个`)
      // 测的是工作区：工作区就是要推的这份代码时才记下来（verify 里查），不是的话照跑、不记；过了的包一个记一个
      ok = verify(need, tree, local, 'pre-push', env, !pushFull, mygo).ok && ok
    }
  }
  // 对拍录像由 Go 自己录，不再回 Bun 那边重录（ADR-225 第 3 条）
}
if (!ok) { console.log('有检查红了，没推。改好再推，不用 --no-verify 绕过（规格-提交前检查 第四节）。'); process.exit(1) }
export {}   // 顶层 await 要求这是个模块
