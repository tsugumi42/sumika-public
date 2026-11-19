// 推送钩子按远端 ref 分档（ADR-398）：远端是 refs/heads/main 才整套，其余（功能分支、试验分支、tag）只扫隐私，删远端分支不查。
// 表在 docs/规格-提交前检查.md 第二节。这里一部分直接测分档函数，一部分起真的 pre-push.ts：
// 编造的临时仓库，带一份和真程序一模一样的源码和程序（摘要对得上，不会重编），名单里只有一个编造号码（做法同 sumika-dev.test.ts）；
// “远端”是本地的裸仓库，问远端有什么走真的 git ls-remote。
// 每次推送的提交都只改 desktop/ 下的文件：整套那一档走到“没有 Go 改动，跳过”就收尾，不会真起 Go 或 Bun 测试；
// 这句话出现与否就是“整套 / 只扫隐私”的记号。
import { Database } from 'bun:sqlite'
import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { devEnv, devExe } from './lib/sumika-dev.ts'
import { pushMode } from './pre-push.ts'

// 每个用例要起两三个子进程（bun、sumika-dev、git），这台机器上起进程偏慢，默认的 5 秒不够；不是放宽检查，检查的退出码和输出照常断言
setDefaultTimeout(60_000)

const repoRoot = join(import.meta.dir, '..')
const script = join(import.meta.dir, 'pre-push.ts')
const FAKE_NUMBER = '8642097531' // 编的
const ZEROS = '0'.repeat(40)
const FULL_MARK = 'Go：没有 Go 改动' // 整套那一档独有的输出
const PRIVACY_MARK = '只扫隐私' // 只扫隐私那一档独有的输出
const SCANNED = '推送前检查：' // 这一行被查过
const NO_REMOTE_MARK = '查不到远端' // 问不到远端有什么、扫全部可达历史的输出

const dirs: string[] = []
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })
// 测试里起的 git 都用一份固定的干净配置：不读这台机器的全局、系统配置（敌对配置在专门的用例里写进测试仓库）
let emptyGitConfig = ''
const cleanEnv = (): Record<string, string> => {
  const env: Record<string, string> = {}
  // 钩子里继承的 GIT_DIR 等会落到真仓库上（2026-11-11 事故）；SUMIKA_PUSH_FULL 会改变整套那一档的走法
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !k.startsWith('GIT_') && k !== 'SUMIKA_PUSH_FULL') env[k] = v
  if (!emptyGitConfig) {
    const d = mkdtempSync(join(tmpdir(), 'pre-push-gitcfg-'))
    dirs.push(d)
    emptyGitConfig = join(d, 'gitconfig')
    writeFileSync(emptyGitConfig, '')
  }
  return { ...env, GIT_CONFIG_GLOBAL: emptyGitConfig, GIT_CONFIG_NOSYSTEM: '1' }
}

let fake = ''
let okRemote = '' // 裸仓库，main 在起点
let emptyRemote = '' // 裸仓库，什么都没有（新远端）
let goneRemote = '' // 不存在的地址（ls-remote 失败）
const sha: Record<string, string> = {}
const gitIn = (cwd: string, ...a: string[]) => {
  const p = Bun.spawnSync(['git', '-c', 'user.name=t', '-c', 'user.email=t@users.noreply.github.com', ...a], { cwd, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' })
  if (p.exitCode !== 0) throw new Error(`git ${a.join(' ')} 失败：${p.stderr.toString()}`)
  return p.stdout.toString().trim()
}
const git = (...a: string[]) => gitIn(fake, ...a)
const commitOn = (branch: string, from: string, file: string, text: string | Uint8Array) => {
  git('checkout', '-q', '-B', branch, from)
  mkdirSync(join(fake, 'desktop'), { recursive: true })
  writeFileSync(join(fake, 'desktop', file), text)
  git('add', `desktop/${file}`)
  git('commit', '-q', '-m', `改 ${file}`)
  return git('rev-parse', 'HEAD')
}
const bare = (name: string) => {
  const d = mkdtempSync(join(tmpdir(), `pre-push-${name}-`))
  dirs.push(d)
  gitIn(d, 'init', '-q', '--bare', '-b', 'main')
  return d
}

beforeAll(() => {
  const real = devExe(repoRoot)
  const v = Bun.spawnSync([real, 'version'], { env: devEnv(), stdout: 'pipe', stderr: 'pipe' })
  if (v.exitCode !== 0) throw new Error(`sumika-dev 起不来：${v.stderr.toString()}`)
  fake = mkdtempSync(join(tmpdir(), 'pre-push-test-'))
  dirs.push(fake)
  mkdirSync(join(fake, 'native/bin'), { recursive: true })
  for (const f of ['go.mod', 'go.sum']) copyFileSync(join(repoRoot, 'native', f), join(fake, 'native', f))
  for (const d of ['cmd/sumika-dev', 'internal/devtools', 'internal/jsjson', 'internal/netsafe'])
    cpSync(join(repoRoot, 'native', d), join(fake, 'native', d), { recursive: true })
  copyFileSync(real, join(fake, 'native/bin/sumika-dev.exe'))
  mkdirSync(join(fake, 'data/dev/human-replies'), { recursive: true })
  writeFileSync(join(fake, 'data/dev/human-replies/groups.json'), JSON.stringify({ g: FAKE_NUMBER }))
  writeFileSync(join(fake, 'data/dev/private-strings.json'), JSON.stringify(['编造的私人字符串，仅测试'])) // 推送前（--range）要求私人字符串名单和群名表都不为空
  mkdirSync(join(fake, 'data/go/chat'), { recursive: true })
  const db = new Database(join(fake, 'data/go/chat/qq.db'))
  db.run('CREATE TABLE events(actor_kind TEXT, actor_id TEXT, conversation TEXT)')
  db.run('CREATE TABLE qq_people(uid TEXT)')
  db.run('CREATE TABLE qq_groups(gid TEXT, name TEXT)')
  db.close()
  writeFileSync(join(fake, '.gitignore'), 'data/\nnative/\n')
  git('init', '-q', '-b', 'main')
  git('add', '.gitignore')
  git('commit', '-q', '-m', '起点')
  sha.base = git('rev-parse', 'HEAD')
  git('update-ref', 'refs/remotes/origin/main', sha.base)
  sha.main = commitOn('main', sha.base, 'm.txt', '干净\n')
  sha.feat = commitOn('feat', sha.base, 'f.txt', '干净\n')
  sha.bad = commitOn('bad', sha.base, 'b.txt', `号 ${FAKE_NUMBER}\n`)
  okRemote = bare('ok')
  git('push', '-q', okRemote, `${sha.base}:refs/heads/main`)
  emptyRemote = bare('empty')
  goneRemote = join(tmpdir(), 'pre-push-no-such-remote-' + process.pid)
  git('remote', 'add', 'dirremote', okRemote) // 钩子第一个参数是它：推送地址从 git remote get-url --push 取
}, 300_000)

// name、url 是钩子的第一、第二个参数（远端名、推送地址）；undefined 就是不给
const pushTo = (name: string | undefined, url: string | string[] | undefined, ...lines: string[]) => {
  const args = [...(name ? [name] : []), ...(name && url ? [url].flat() : [])]
  const p = Bun.spawnSync([process.execPath, script, ...args], { cwd: fake, env: cleanEnv(), stdin: Buffer.from(lines.join('\n') + (lines.length ? '\n' : '')), stdout: 'pipe', stderr: 'pipe' })
  const out = p.stdout.toString() + p.stderr.toString()
  expect(out).not.toContain(FAKE_NUMBER) // 扫描只打码，原文不出现
  for (const u of [url].flat()) if (u && u.length > 8) expect(out).not.toContain(u) // 推送地址可能带账号或令牌，不打印
  return { code: p.exitCode ?? -1, out }
}
const push = (...lines: string[]) => pushTo('origin', okRemote, ...lines)
const L = (localRef: string, local: string, remoteRef: string, remote: string) => `${localRef} ${local} ${remoteRef} ${remote}`
const count = (s: string, sub: string) => s.split(sub).length - 1

// ---- 分档函数 ----
test('pushMode：只有远端 ref 是 refs/heads/main（不分大小写）才整套；删除不查；格式不对的红', () => {
  const m = (remoteRef: string | undefined, local: string | undefined = 'a'.repeat(40)) => pushMode({ localRef: 'x', local, remoteRef, remote: 'b'.repeat(40) })
  expect(m('refs/heads/main')).toBe('full')
  expect(m('refs/heads/Main')).toBe('full')
  expect(m('refs/heads/MAIN')).toBe('full')
  expect(m('refs/heads/feat/x')).toBe('privacy')
  expect(m('refs/heads/try/x')).toBe('privacy')
  expect(m('refs/heads/mainline')).toBe('privacy')
  expect(m('refs/heads/main2')).toBe('privacy')
  expect(m('refs/heads/main/x')).toBe('privacy')
  expect(m('main')).toBe('privacy')
  expect(m('refs/tags/v1')).toBe('privacy')
  expect(m(undefined)).toBe('bad') // 缺远端 ref
  expect(m('refs/heads/main', ZEROS)).toBe('skip')
  expect(m('refs/heads/feat', ZEROS)).toBe('skip')
  expect(m('refs/heads/main', '')).toBe('bad') // 本地提交缺
  expect(m('refs/heads/main', 'zz'.repeat(20))).toBe('bad') // 本地提交不是十六进制
  expect(pushMode({ localRef: 'x', local: 'a'.repeat(40), remoteRef: 'refs/heads/main' })).toBe('bad') // 缺远端提交
})

// ---- 真起 pre-push.ts ----
test('只推 main：隐私扫描加整套那一档', () => {
  const r = push(L('refs/heads/main', sha.main!, 'refs/heads/main', sha.base!))
  expect(r.code).toBe(0)
  expect(r.out).toContain(SCANNED)
  expect(r.out).toContain(FULL_MARK)
  expect(r.out).not.toContain(PRIVACY_MARK)
})

test('只推功能分支：只扫隐私，不走整套', () => {
  const r = push(L('refs/heads/feat', sha.feat!, 'refs/heads/feat', sha.base!))
  expect(r.code).toBe(0)
  expect(r.out).toContain(SCANNED)
  expect(r.out).toContain(PRIVACY_MARK)
  expect(r.out).not.toContain(FULL_MARK)
})

test('功能分支里有编造号码：隐私扫描拦下，退出码非零', () => {
  const r = push(L('refs/heads/bad', sha.bad!, 'refs/heads/bad', sha.base!))
  expect(r.code).toBe(1)
  expect(r.out).toContain('拦 号码')
  expect(r.out).not.toContain(FULL_MARK)
  expect(r.out).toContain('没推')
})

test('试验分支 try/x：只扫隐私；有号码照拦', () => {
  const ok = push(L('refs/heads/try/x', sha.feat!, 'refs/heads/try/x', sha.base!))
  expect(ok.code).toBe(0)
  expect(ok.out).toContain(PRIVACY_MARK)
  expect(ok.out).not.toContain(FULL_MARK)
  const bad = push(L('refs/heads/try/x', sha.bad!, 'refs/heads/try/x', sha.base!))
  expect(bad.code).toBe(1)
  expect(bad.out).toContain('拦 号码')
})

test('同时推 main 和功能分支：main 照旧整套，功能分支只扫隐私，两行都扫', () => {
  const r = push(L('refs/heads/main', sha.main!, 'refs/heads/main', sha.base!), L('refs/heads/feat', sha.feat!, 'refs/heads/feat', ZEROS))
  expect(r.code).toBe(0)
  expect(count(r.out, SCANNED)).toBe(2)
  expect(count(r.out, FULL_MARK)).toBe(1)
  expect(count(r.out, PRIVACY_MARK)).toBe(1)
})

test('同时推 main 和带号码的功能分支：不管先后，号码都拦下，main 那行照样整套', () => {
  const a = push(L('refs/heads/main', sha.main!, 'refs/heads/main', sha.base!), L('refs/heads/bad', sha.bad!, 'refs/heads/bad', sha.base!))
  expect(a.code).toBe(1)
  expect(a.out).toContain('拦 号码')
  expect(a.out).toContain(FULL_MARK)
  const b = push(L('refs/heads/bad', sha.bad!, 'refs/heads/bad', sha.base!), L('refs/heads/main', sha.main!, 'refs/heads/main', sha.base!))
  expect(b.code).toBe(1)
  expect(b.out).toContain('拦 号码')
  expect(b.out).toContain(FULL_MARK)
})

test('新建远端分支（远端提交全零）：范围是比远端已有的提交多出来的，只扫隐私；号码照拦', () => {
  const ok = push(L('refs/heads/feat', sha.feat!, 'refs/heads/feat', ZEROS))
  expect(ok.code).toBe(0)
  expect(ok.out).toContain('1 个提交')
  expect(ok.out).toContain(PRIVACY_MARK)
  expect(ok.out).not.toContain(FULL_MARK)
  const bad = push(L('refs/heads/bad', sha.bad!, 'refs/heads/bad', ZEROS))
  expect(bad.code).toBe(1)
  expect(bad.out).toContain('拦 号码')
})

test('删除远端分支（本地提交全零）：什么都不查，不变', () => {
  for (const ref of ['refs/heads/feat', 'refs/heads/main']) {
    const r = push(L('(delete)', ZEROS, ref, sha.base!))
    expect(r.code).toBe(0)
    expect(r.out).toBe('')
  }
})

test('feat:main（本地是别的分支、远端 ref 是 main）：仍然整套', () => {
  const r = push(L('refs/heads/feat', sha.feat!, 'refs/heads/main', sha.base!))
  expect(r.code).toBe(0)
  expect(r.out).toContain(FULL_MARK)
  expect(r.out).not.toContain(PRIVACY_MARK)
  // 带号码的提交推到 main：同样先被隐私扫描拦下
  expect(push(L('refs/heads/bad', sha.bad!, 'refs/heads/main', sha.base!)).code).toBe(1)
})

test('远端 ref 前缀像 main（mainline、main2）：不当成 main，只扫隐私；号码照拦', () => {
  for (const ref of ['refs/heads/mainline', 'refs/heads/main2']) {
    const r = push(L('refs/heads/main', sha.main!, ref, sha.base!))
    expect(r.code).toBe(0)
    expect(r.out).toContain(PRIVACY_MARK)
    expect(r.out).not.toContain(FULL_MARK)
    expect(push(L('refs/heads/bad', sha.bad!, ref, sha.base!)).code).toBe(1)
  }
})

test('远端 ref 是 refs/heads/Main：当成 main，整套', () => {
  const r = push(L('refs/heads/main', sha.main!, 'refs/heads/Main', sha.base!))
  expect(r.code).toBe(0)
  expect(r.out).toContain(FULL_MARK)
})

test('推 tag：只扫隐私；指向带号码的提交照拦', () => {
  const ok = push(L('refs/tags/v1', sha.feat!, 'refs/tags/v1', ZEROS))
  expect(ok.code).toBe(0)
  expect(ok.out).toContain(PRIVACY_MARK)
  expect(ok.out).not.toContain(FULL_MARK)
  expect(push(L('refs/tags/v2', sha.bad!, 'refs/tags/v2', ZEROS)).code).toBe(1)
})

test('标准输入为空：退出码 0，什么都不做', () => {
  const r = push()
  expect(r.code).toBe(0)
  expect(r.out).toBe('')
})

// ---- “远端已有什么”以真实的远端为准（git ls-remote），不看本地可能过期的 refs/remotes/<远端>/main ----
// 旧历史：old1 干净、old2 加了编造号码、old3 又把它删掉；topic 在 old3 上再加一个干净提交
const oldHistory = () => {
  const o1 = commitOn('old', sha.base!, 'old1.txt', '干净\n')
  commitOn('old', o1, 'old2.txt', `号 ${FAKE_NUMBER}\n`)
  git('rm', '-q', 'desktop/old2.txt')
  git('commit', '-q', '-m', '删掉')
  const o3 = git('rev-parse', 'HEAD')
  return { o3, topic: commitOn('topic', o3, 't1.txt', '干净\n') }
}

test('换了远端没 fetch：本地 origin/main 指向旧仓库最新提交，推带完整旧历史的新分支到空的新远端，旧历史里加过又删掉的号码必须拦下', () => {
  const { o3, topic } = oldHistory()
  git('update-ref', 'refs/remotes/origin/main', o3) // 过期的跟踪分支：新远端根本没有 o3
  try {
    const r = pushTo('origin', emptyRemote, L('refs/heads/topic', topic, 'refs/heads/topic', ZEROS))
    expect(r.code).toBe(1)
    expect(r.out).toContain('拦 号码')
    expect(r.out).toContain('多出来的提交')
    expect(r.out).not.toContain(FULL_MARK)
  } finally { git('update-ref', 'refs/remotes/origin/main', sha.base!) }
})

test('提交说明里的号码（文件是干净的）也拦：新分支比远端多出来的提交都扫提交说明', () => {
  git('checkout', '-q', '-B', 'msg', sha.base!)
  git('commit', '-q', '--allow-empty', '-m', `号 ${FAKE_NUMBER}`)
  const r = push(L('refs/heads/msg', git('rev-parse', 'HEAD'), 'refs/heads/msg', ZEROS))
  expect(r.code).toBe(1)
  expect(r.out).toContain('提交说明')
})

test('问不到远端有什么（地址不存在、没给地址和名）：失败关闭，扫全部可达历史；旧历史里的号码拦下', () => {
  const { topic } = oldHistory()
  for (const [name, url] of [['origin', goneRemote], [undefined, undefined]] as const) {
    const old = pushTo(name, url, L('refs/heads/topic', topic, 'refs/heads/topic', ZEROS))
    expect(old.code).toBe(1)
    expect(old.out).toContain(NO_REMOTE_MARK)
    const bad = pushTo(name, url, L('refs/heads/bad', sha.bad!, 'refs/heads/bad', ZEROS))
    expect(bad.code).toBe(1)
    const ok = pushTo(name, url, L('refs/heads/feat', sha.feat!, 'refs/heads/feat', ZEROS))
    expect(ok.code).toBe(0)
    expect(ok.out).toContain(NO_REMOTE_MARK)
  }
})

test('推送用的地址优先于远端名：远端名乱写、地址是对的，照常按远端已有的提交算范围', () => {
  const r = pushTo('whatever', okRemote, L('refs/heads/feat', sha.feat!, 'refs/heads/feat', ZEROS))
  expect(r.code).toBe(0)
  expect(r.out).toContain('1 个提交')
  expect(r.out).not.toContain(NO_REMOTE_MARK)
})

test('远端提交在本地找不到（强推、别人推过）：问远端有什么再算，带号码的拦下', () => {
  const ghost = '1'.repeat(40)
  const ok = push(L('refs/heads/feat', sha.feat!, 'refs/heads/feat', ghost))
  expect(ok.code).toBe(0)
  expect(ok.out).toContain('1 个提交')
  expect(push(L('refs/heads/bad', sha.bad!, 'refs/heads/bad', ghost)).code).toBe(1)
})

test('本地提交远端已经有（新分支就建在远端 main 的提交上）：没有新发的提交，0 个提交，过', () => {
  const r = push(L('refs/heads/same', sha.base!, 'refs/heads/same', ZEROS))
  expect(r.code).toBe(0)
  expect(r.out).toContain('0 个提交')
})

test('main 那条路同样：新远端上还没有 main，带号码的拦下，文件清单按整棵树，整套那一档照走', () => {
  const r = pushTo('origin', emptyRemote, L('refs/heads/bad', sha.bad!, 'refs/heads/main', ZEROS))
  expect(r.code).toBe(1)
  expect(r.out).toContain('拦 号码')
  expect(r.out).toContain('整棵树')
  expect(r.out).toContain(FULL_MARK)
})

test('含 NUL 的文本文件里的号码也拦（git 默认当二进制不给内容）', () => {
  const nul = commitOn('with-nul', sha.base!, 'n.txt', new Uint8Array([...new TextEncoder().encode(`号 ${FAKE_NUMBER}`), 0, 10]))
  const r = push(L('refs/heads/with-nul', nul, 'refs/heads/with-nul', ZEROS))
  expect(r.code).toBe(1)
  expect(r.out).toContain('拦 号码')
})

// ---- 输入行格式、本地提交类型 ----
test('输入行格式不对（缺段、提交号不是十六进制）：红，不当成没事跳过', () => {
  for (const line of ['garbage', `refs/heads/feat ${sha.feat!} refs/heads/feat`, `refs/heads/feat zzzz refs/heads/feat ${sha.base!}`, `refs/heads/feat ${sha.feat!} refs/heads/feat nothex`]) {
    const r = push(line)
    expect(r.code).toBe(1)
    expect(r.out).toContain('格式不对')
  }
})

test('本地提交指向 tree、blob（git push origin <blob>:refs/tags/x）：剥不出提交，红', () => {
  const blob = git('rev-parse', `${sha.base!}:.gitignore`)
  const tree = git('rev-parse', `${sha.base!}^{tree}`)
  for (const [what, obj] of [['blob', blob], ['tree', tree]] as const) {
    const r = push(L(obj, obj, 'refs/tags/x', ZEROS))
    expect(r.code).toBe(1)
    expect(r.out).toContain(`是 ${what}`)
  }
  expect(push(L('refs/heads/x', '2'.repeat(40), 'refs/heads/x', ZEROS)).code).toBe(1) // 本地没有的对象
})

// ---- 远端已有的提交太多：当取不到，失败关闭 ----
test('远端分支标签在本地也有的提交超过 150 个：当取不到，扫全部历史，旧历史里的号码拦下', () => {
  // 160 个互不相干的提交，一次用 fast-import 建好
  let stream = ''
  for (let i = 0; i < 160; i++) stream += `commit refs/heads/many/${i}\ncommitter t <t@users.noreply.github.com> ${1700000000 + i} +0000\ndata 2\nm\nM 100644 inline f.txt\ndata 2\nx\n\n`
  const f = Bun.spawnSync(['git', 'fast-import', '--quiet'], { cwd: fake, env: cleanEnv(), stdin: Buffer.from(stream), stdout: 'pipe', stderr: 'pipe' })
  expect(f.exitCode).toBe(0)
  const { topic } = oldHistory()
  const many = bare('many')
  // 远端已经有旧历史（old 分支）：真按远端已有的提交算范围就只剩 topic 上那一个干净提交；超过上限当取不到，才扫全部历史、拦下旧历史里的号码
  git('push', '-q', many, 'refs/heads/many/*:refs/heads/many/*', 'refs/heads/old:refs/heads/old')
  const r = pushTo('origin', many, L('refs/heads/topic', topic, 'refs/heads/topic', ZEROS))
  expect(r.code).toBe(1)
  expect(r.out).toContain(NO_REMOTE_MARK)
  expect(r.out).toContain('拦 号码')
})

// ---- 推送地址：先按远端名问 git，地址被劈开也不怕 ----
test('远端名是已配置的远端：推送地址从 git remote get-url --push 取，后面被劈开的地址片段不管', () => {
  const r = pushTo('dirremote', ['C:/Program', 'Files/x.git'], L('refs/heads/feat', sha.feat!, 'refs/heads/feat', ZEROS))
  expect(r.code).toBe(0)
  expect(r.out).toContain('1 个提交')
  expect(r.out).not.toContain(NO_REMOTE_MARK)
})

test('远端名不是已配置的远端、参数多于两个（地址被劈开）：取不到，失败关闭', () => {
  const r = pushTo('origin', ['C:/Program', 'Files/x.git'], L('refs/heads/bad', sha.bad!, 'refs/heads/bad', ZEROS))
  expect(r.code).toBe(1)
  expect(r.out).toContain(NO_REMOTE_MARK)
})

// ---- ref 名、标签对象文本 ----
test('分支名、tag 名里的号码也拦（本地 ref、远端 ref 都扫）', () => {
  const a = push(L('refs/heads/feat', sha.feat!, `refs/heads/qq_${FAKE_NUMBER}`, ZEROS))
  expect(a.code).toBe(1)
  expect(a.out).toContain('分支名或标签名')
  const b = push(L(`refs/tags/qq_${FAKE_NUMBER}`, sha.feat!, 'refs/tags/v9', ZEROS))
  expect(b.code).toBe(1)
})

test('带标注的 tag：标签对象的说明里有号码也拦', () => {
  git('tag', '-a', '-f', 'tg', '-m', `号 ${FAKE_NUMBER}`, sha.feat!)
  const obj = git('rev-parse', 'tg')
  expect(obj).not.toBe(sha.feat!)
  const r = push(L('refs/tags/tg', obj, 'refs/tags/tg', ZEROS))
  expect(r.code).toBe(1)
  expect(r.out).toContain('标签对象')
})

// ---- 敌对配置：用户配置不能让扫到的内容变样 ----
test('diff.noprefix、textconv、log.showRoot=false、refs/replace 都改不了扫到的内容：带号码的新分支照拦', () => {
  const undo: string[][] = []
  const setCfg = (k: string, v: string) => { git('config', k, v); undo.push(['config', '--unset', k]) }
  try {
    setCfg('diff.noprefix', 'true')
    setCfg('diff.mnemonicPrefix', 'true')
    setCfg('log.showRoot', 'false')
    setCfg('diff.evil.textconv', 'git hash-object')
    writeFileSync(join(fake, '.git/info/attributes'), '*.txt diff=evil\n')
    // 带号码的分支 bad 被替换成干净的 feat：不关 refs/replace 的话，扫到的是 feat 的内容
    git('replace', sha.bad!, sha.feat!)
    const r = push(L('refs/heads/bad', sha.bad!, 'refs/heads/bad', ZEROS))
    expect(r.code).toBe(1)
    expect(r.out).toContain('拦 号码')
    // 空文件名（没有 +++ 行）：noprefix 下没有 a/ b/ 前缀
    const e = commitOn('emptyname', sha.base!, `qq_${FAKE_NUMBER}.md`, '')
    const r2 = push(L('refs/heads/emptyname', e, 'refs/heads/emptyname', ZEROS))
    expect(r2.code).toBe(1)
    expect(r2.out).toContain('拦 号码')
  } finally {
    git('replace', '-d', sha.bad!)
    for (const u of undo) git(...u)
    rmSync(join(fake, '.git/info/attributes'), { force: true })
  }
})
