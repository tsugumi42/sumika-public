// 推送前的检查（规格-提交前检查 第二节）：git 的 pre-push 钩子从标准输入给"本地 ref、本地提交、远端 ref、远端提交"，按这次要推的范围挑检查。
// lefthook.yml 里 use_stdin 把输入转给这里。每个检查用它自己的退出码，有一个红就整体退出码 1、推不上去。
//   echo "refs/heads/main <本地提交> refs/heads/main <远端提交>" | bun scripts/pre-push.ts <远端名> <远端地址>     手动试（不给就问不到远端有什么，新分支扫全部可达历史）
// 按远端 ref 分两档（ADR-398）：远端是 refs/heads/main 的整套检查（隐私扫描、Bun、Go）；其余（功能分支、试验分支、tag）只扫隐私，
// Go 测试和脚本测试交给公开仓库 CI；删远端分支什么都不查。没有旁路开关：要少跑只能改远端 ref，不能靠环境变量或参数。
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { describePick, pick, readGraph } from './go-affected'
import { devEnv, devExe } from './lib/sumika-dev'
import { cleanTree, needsRunPlanned, PLANNED_PATHS, plan, runPlanned } from './server-verify'
import { git, goEnv, goFingerprint, goTree, loadVerified, uncovered, verify, verifyMygo, when } from './go-verify'

const ZERO = /^0+$/
export const MAIN_REF = 'refs/heads/main'
export type PushLine = { localRef?: string; local?: string; remoteRef?: string; remote?: string }
export type PushMode = 'skip' | 'privacy' | 'full' | 'bad'

export const parsePush = (input: string): PushLine[] =>
  input.split(/\r?\n/).filter(Boolean).map((line) => { const [localRef, local, remoteRef, remote] = line.split(/\s+/); return { localRef, local, remoteRef, remote } })

const HEX = /^[0-9a-f]{40,64}$/

// 这一行查什么：输入不是 git 给的格式（缺字段、提交号不是十六进制）一律红，不当成没事跳过；删远端分支（本地提交全零）不查；
// 远端 ref 恰好是 refs/heads/main（不分大小写：refs/heads/Main 在不分大小写的服务器上就是 main）才整套；其余只扫隐私。
export function pushMode(l: PushLine): PushMode {
  if (!l.localRef || !l.remoteRef || !l.local || !l.remote || !HEX.test(l.local) || !HEX.test(l.remote)) return 'bad'
  if (ZERO.test(l.local)) return 'skip'
  return l.remoteRef.toLowerCase() === MAIN_REF ? 'full' : 'privacy'
}

// refs/replace/ 能让 git 把一个提交换成另一个的内容：本脚本和它起的所有子进程（含 sumika-dev）都不认替换对象
process.env.GIT_NO_REPLACE_OBJECTS = '1'
const env = goEnv()
const root = process.cwd()
// show：打印用的命令（ref 名里可能有号码，不把 --line 的值打出来）
const run = (cmd: string[], cwd?: string, runEnv = env, show = cmd.join(' ')) => {
  const t = Date.now()
  const p = Bun.spawnSync(cmd, { cwd, env: runEnv, stdout: 'inherit', stderr: 'inherit' })
  console.log(`  ${p.exitCode === 0 ? '过' : '红'}（退出码 ${p.exitCode}，${((Date.now() - t) / 1000).toFixed(1)} 秒）：${show}`)
  return p.exitCode === 0
}

// 这次会新发到远端的全部提交 = 本地提交 --not 远端已有的提交。算不出"远端已有什么"时退成本地提交的全部可达历史（慢，但少见，宁可多扫）；
// 不用本地的 refs/remotes/<远端>/main 当起点：换了远端或久没 fetch，它指向的提交远端不一定有（旧历史里加过又删掉的号码、提交说明都会漏）。
export type Scope = { revs: string[]; label: string; base?: string }
type Q = { ok: boolean; out: string }
const gq = (args: string[]): Q => { const p = Bun.spawnSync(['git', '-c', 'core.quotepath=false', ...args], { env: process.env }); return { ok: p.exitCode === 0, out: p.stdout.toString().trim() } }
const MAX_TIPS = 150   // 排除列表交给命令行传（Windows 上命令行有长度上限）；远端在本地也有的提交超过这个数就当取不到，扫全部历史

// 远端真实的分支和标签指向的提交（git ls-remote），只留本地也有的（本地没有的对象帮不上，当它不存在）。取不到（网络、权限、地址不对、超时）返回 null，
// 调用方失败关闭、扫全部历史；空仓库返回空数组。target 是钩子的第二个参数（推送用的地址），没有就用第一个参数（远端名）。
export function remoteTips(target: string | undefined): string[] | null {
  if (!target || target.startsWith('-')) return null
  const p = Bun.spawnSync(['git', 'ls-remote', '--heads', '--tags', target], { env: { ...process.env, GIT_TERMINAL_PROMPT: '0' }, stdout: 'pipe', stderr: 'pipe', timeout: 60_000 })
  if (p.exitCode !== 0) return null
  const shas = [...new Set(p.stdout.toString().split(/\r?\n/).map((l) => l.split(/\s+/)[0] ?? '').filter((s) => /^[0-9a-f]{40,64}$/.test(s)))]
  if (!shas.length) return []
  // 一次问完：标签对象剥成提交，本地没有的、不是提交的显示 missing
  const b = Bun.spawnSync(['git', 'cat-file', '--batch-check=%(objectname) %(objecttype)'], { stdin: Buffer.from(shas.map((s) => `${s}^{commit}\n`).join('')), stdout: 'pipe', stderr: 'pipe' })
  if (b.exitCode !== 0) return null
  const tips = [...new Set(b.stdout.toString().split(/\r?\n/).filter((l) => / commit$/.test(l)).map((l) => l.split(' ')[0]!))]
  return tips.length > MAX_TIPS ? null : tips
}

// 推送用的地址：先按钩子第一个参数（远端名）问 git remote get-url --push（带空格的地址被 lefthook 的 {2} 劈成几段也没关系）；
// 它不是已配置的远端（按地址推时两个参数都是地址）就用第二个参数；参数多于两个说明地址被劈开了，取不到。
export function resolveTarget(args: string[]): string | undefined {
  const [name, url] = args
  if (!name) return undefined
  if (!name.startsWith('-')) {
    const u = gq(['remote', 'get-url', '--push', name])
    if (u.ok && u.out) return u.out
  }
  if (args.length === 1) return name
  if (args.length === 2) return url
  return undefined
}

// 远端已有这个分支、且这个提交在本地：看远端提交到本地提交；其余（新分支、远端提交不在本地）：看比远端所有分支标签都多出来的提交。
export function scopeOf(local: string, remote: string | undefined, target: string | undefined, g: (a: string[]) => Q = gq): Scope {
  const short = local.slice(0, 8)
  if (remote && !ZERO.test(remote) && g(['cat-file', '-e', `${remote}^{commit}`]).ok) return { revs: [local, '--not', remote], label: `${remote.slice(0, 8)}..${short}`, base: remote }
  const tips = remoteTips(target)
  if (tips === null) return { revs: [local], label: `查不到远端上有什么，扫 ${short} 的全部可达历史` }
  return { revs: tips.length ? [local, '--not', ...tips] : [local], label: `${short} 比远端多出来的提交` }
}

/** 查一行；true = 过。target 是钩子给的远端地址（第二个参数）或远端名（第一个参数）。 */
export function checkLine(l: PushLine, target?: string): boolean {
  const mode = pushMode(l)
  if (mode === 'skip') return true
  if (mode === 'bad') { console.log(`推送前检查：输入行格式不对（要四段：本地 ref、本地提交、远端 ref、远端提交），不放行`); return false }
  const local = l.local!
  // 本地提交剥不出提交（指向 tree、blob 的 ref）rev-list 会数出 0 个提交，什么都不扫：不放行
  if (!gq(['rev-parse', '--verify', '-q', `${local}^{commit}`]).ok) { console.log(`推送前检查：${local.slice(0, 8)} 是 ${gq(['cat-file', '-t', local]).out || '本地没有的对象'}，不是提交也剥不出提交，没法查，不放行`); return false }
  const scope = scopeOf(local, l.remote, target)
  const n = gq(['rev-list', '--count', ...scope.revs, '--'])
  if (!n.ok) { console.log(`推送前检查：${scope.label}，git 数不出提交，没法查，不放行`); return false }
  let files: string[] = []
  if (scope.base) files = git(['diff', '--name-only', '--no-renames', scope.base, local]).split(/\r?\n/).filter(Boolean)
  console.log(`推送前检查：${scope.label}，${n.out} 个提交${scope.base ? `、${files.length} 个文件` : ''}`)
  // ref 名（分支名、tag 名）和带标注 tag 的标签对象文本也交给扫描
  const extra = [l.localRef!, l.remoteRef!].flatMap((t) => ['--line', t])
  if (gq(['cat-file', '-t', local]).out === 'tag') extra.push('--tag', local)
  let ok = run([devExe(root), 'check', 'private', ...extra, '--range', ...scope.revs], undefined, devEnv(env), `${devExe(root)} check private --range ${scope.revs.join(' ')}`)
  if (mode === 'privacy') { console.log(`远端 ref 不是 main：只扫隐私，Go 和脚本测试交给 GitHub 检查（ADR-398）`); return ok }
  if (!scope.base) {
    // 没有可比的远端提交：挑检查用的文件清单按整棵树（宁可多查）
    const ls = gq(['ls-tree', '-r', '--name-only', local])
    if (!ls.ok) { console.log(`${local.slice(0, 8)} 的文件清单读不出来，没法挑检查，不放行`); return false }
    files = ls.out.split(/\r?\n/).filter(Boolean)
    console.log(`没有可比的远端提交，挑检查的文件清单按整棵树（${files.length} 个文件）`)
  }
  // 按改了哪些路径挑 Bun 后台和协议的检查（规格-Bun后台第1批 第八节）；只改 server/ 时不跑 Go
  const pl = plan(files)
  if (needsRunPlanned(pl)) {
    // Bun 这几步测的是工作区：工作区和要推的提交在这几处不一样时，测出来的不是要推的代码，照 go-verify 的做法停下
    // 公开导出的仓库没有 server/、desktop/：不存在的路径不列出来，免得提示里出现根本没有的目录
    const planned = [...PLANNED_PATHS].filter((p) => existsSync(join(root, p)))
    const dirty = [git(['diff', '--name-only', local, '--', ...planned, 'scripts']), git(['ls-files', '--others', '--exclude-standard', '--', ...planned, 'scripts'])].filter(Boolean).join('\n')
    if (dirty) { console.log(`工作区里有 Bun 那几步会读到、还没提交的改动，测不到要推的 ${local.slice(0, 8)}。先提交或收起改动再推：\n${dirty}`); return false }
    // 合并队列在同一棵树上跑过的步骤不再跑（SUMIKA_PUSH_FULL=1 照跑）：工作区就是要推的这棵树时才认、才记
    const steps = runPlanned(pl, root, env, undefined, process.env.SUMIKA_PUSH_FULL === '1' ? {} : { tree: cleanTree(root, local), by: 'pre-push' })
    if (steps.some((s) => !s.ok)) ok = false
  }
  if (!pl.go && process.env.SUMIKA_PUSH_FULL !== '1') { console.log('Go：没有 Go 改动，跳过 vet、test、archcheck'); return ok }
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
  return ok
}

if (import.meta.main) {
  let ok = true
  // pre-push 钩子的第一个参数是远端名、第二个是推送用的地址（lefthook.yml 里的 {1} {2}）
  const target = resolveTarget(process.argv.slice(2))
  for (const l of parsePush(await Bun.stdin.text())) {
    let r = false
    try { r = checkLine(l, target) } catch (e) { console.log(`推送前检查出错，不放行：${e instanceof Error ? e.message : String(e)}`) }
    ok = r && ok
  }
  if (!ok) { console.log('有检查红了，没推。改好再推，不用 --no-verify 绕过（规格-提交前检查 第四节）。'); process.exit(1) }
}
