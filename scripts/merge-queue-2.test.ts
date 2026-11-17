import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { acquireRunner, addEntry, conclusionOf, git, loadState, makeCtx, mutate, processHead, removeEntry, runnerAlive, runQueue, saveState, statusText, type Ctx, type Verify } from './merge-queue'
import { cleanEnv, green, mainSha, mkBranch, mkRepo, quiet, root, sh, useQueueTestEnv } from './merge-queue.testlib'

useQueueTestEnv()

// 合并队列的测试（第 2 份，共三份；共用的准备在 merge-queue.testlib.ts）

test('合并有冲突：记红、写冲突文件、不动 main、专用 worktree 回到干净，后面的照常合', async () => {
  const c = mkRepo()
  mkBranch(c, 'x', 'base.txt', 'X\nline2\nline3\n')
  mkBranch(c, 'y', 'base.txt', 'Y\nline2\nline3\n')
  mkBranch(c, 'z', 'z.txt', 'z\n')
  for (const b of ['x', 'y', 'z']) addEntry(c, b)
  const calls: string[] = []
  await runQueue(c, { ...quiet, verify: (a) => { calls.push(a.entry.branch); return green(a) } })
  const [rx, ry, rz] = loadState(c.statePath).results
  expect([rx.ok, ry.ok, rz.ok]).toEqual([true, false, true])
  expect(ry.reason).toBe('冲突')
  expect(ry.files).toEqual(['base.txt'])
  expect(calls).toEqual(['x', 'z'])   // 冲突的没进验证
  expect(sh(c.mainDir, 'show', 'main:base.txt').replace(/\r/g, '')).toBe('X\nline2\nline3')   // y 没进来
  expect(sh(c.queueWt, 'status', '--porcelain')).toBe('')
  expect(existsSync(join(c.common, 'worktrees'))).toBe(true)
  expect(git(c.queueWt, ['rev-parse', '--verify', '-q', 'MERGE_HEAD']).code).not.toBe(0)
})

test('验证途中 main 被别人改了：不快进，重新合、重新验', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  addEntry(c, 'a')
  let calls = 0
  let moved = ''
  const verify: Verify = (x) => {
    if (++calls === 1) {
      writeFileSync(join(c.mainDir, 'other.txt'), 'o\n'); sh(c.mainDir, 'add', 'other.txt'); sh(c.mainDir, 'commit', '-qm', '别人合进来的')
      moved = mainSha(c)
    }
    return green(x)
  }
  await runQueue(c, { ...quiet, verify })
  expect(calls).toBe(2)
  expect(sh(c.mainDir, 'rev-parse', 'main^1')).toBe(moved)   // 新的合并提交接在别人的提交后面
  expect(existsSync(join(c.mainDir, 'other.txt')) && existsSync(join(c.mainDir, 'a.txt'))).toBe(true)
  expect(loadState(c.statePath).results[0].ok).toBe(true)
})

// Bun 那段单独一行（规格-Bun后台第1批 第八节）：go-verify 另打的"合并说明另写："接在结论后面
test('结论行：Bun 那段单独一行接在 Go 结论后面，没有 Go 时写跳过', async () => {
  expect(conclusionOf('合并说明可以写：go vet 过\n合并说明另写：server bun test：12 通过\n合并说明另写：gen-rpc --check：生成文件是新的\n', 0, 1))
    .toBe('go vet 过\nserver bun test：12 通过\ngen-rpc --check：生成文件是新的')
  expect(conclusionOf('合并说明可以写：没有 Go 改动，跳过 Go 的 vet、test、archcheck\r\n合并说明另写：server bun test：3 通过\r\n', 0, 1))
    .toBe('没有 Go 改动，跳过 Go 的 vet、test、archcheck\nserver bun test：3 通过')
  expect(conclusionOf('什么都没有', 0, 4.2)).toBe('go-verify 退出码 0（4 秒）')
  const c = mkRepo()
  mkBranch(c, 'srv', 'server-note.txt', 'x\n')
  addEntry(c, 'srv', 'feat(测试): 合 srv', '正文')
  const verify: Verify = ({ append }) => { append('合并说明可以写：没有 Go 改动，跳过 Go 的 vet、test、archcheck\n合并说明另写：server bun test：7 通过\n'); return { ok: true, code: 0 } }
  expect(await runQueue(c, { ...quiet, verify })).toBe('empty')
  const msg = sh(c.mainDir, 'log', '-1', '--format=%B', 'main')
  expect(msg).toContain('合并队列验证：没有 Go 改动，跳过 Go 的 vet、test、archcheck\nserver bun test：7 通过')
})

test('add 的拒绝：main、不存在的分支、重复；remove 不删正在跑的', () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  expect(() => addEntry(c, 'main')).toThrow()
  expect(() => addEntry(c, 'nope')).toThrow('没有这个本地分支')
  expect(addEntry(c, 'a').position).toBe(1)
  expect(() => addEntry(c, 'a')).toThrow('已经在队列里')
  mutate(c, (s) => { s.running = { id: s.queue[0].id, branch: 'a', pid: process.pid, since: new Date().toISOString(), phase: '验证' } })
  expect(() => removeEntry(c, 'a')).toThrow('正在跑')
  mutate(c, (s) => { s.running = null })
  removeEntry(c, 'a')
  expect(loadState(c.statePath).queue).toEqual([])
  expect(() => removeEntry(c, 'a')).toThrow('队列里没有')
})

test('状态文件原子写：写到一半失败，原文件完好、没有临时文件残留', () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  addEntry(c, 'a')
  const before = readFileSync(c.statePath, 'utf8')
  const crash = { write: (p: string, body: string) => { writeFileSync(p, body.slice(0, body.length / 2)); throw new Error('写到一半断电') } }
  expect(() => saveState(c.statePath, { queue: [], running: null, results: [] }, crash)).toThrow('断电')
  expect(readFileSync(c.statePath, 'utf8')).toBe(before)
  expect(JSON.parse(before).queue[0].branch).toBe('a')
  expect(readdirSync(c.common).filter((f) => f.endsWith('.tmp'))).toEqual([])
  // 正常写：内容可解析、没有残留
  mutate(c, (s) => { s.results.push({ branch: 'q', title: 't', at: 'x', ok: true, reason: '', seconds: 1 }) })
  expect(JSON.parse(readFileSync(c.statePath, 'utf8')).results.length).toBe(1)
  expect(readdirSync(c.common).filter((f) => f.endsWith('.tmp') || f.endsWith('.lock'))).toEqual([])
  // 写坏的文件当空队列，不抛错
  writeFileSync(c.statePath, '{半份')
  expect(loadState(c.statePath).queue).toEqual([])
})

test('两个执行者互斥：已有活着的执行者时第二个起不来，释放或进程没了以后能接手', async () => {
  const c = mkRepo()
  const first = acquireRunner(c)
  expect('release' in first).toBe(true)
  const second = acquireRunner(c)
  expect('pid' in second && second.pid).toBe(process.pid)
  // 命令行层面：run 退出码 4
  const p = Bun.spawnSync([process.execPath, join(import.meta.dir, 'merge-queue.ts'), 'run'], { cwd: c.mainDir, env: cleanEnv() })
  expect(p.exitCode).toBe(4)
  expect(runQueue(c, { ...quiet, verify: green })).resolves.toBe('busy')
  if ('release' in first) first.release()
  const third = acquireRunner(c)
  expect('release' in third).toBe(true)
  if ('release' in third) third.release()
  // 持有者进程已经没了的锁算过期
  const dead = Bun.spawnSync([process.execPath, '-e', 'process.stdout.write(String(process.pid))'], { env: cleanEnv() })
  const lock = join(c.common, 'sumika-merge-queue.runner')
  writeFileSync(lock, JSON.stringify({ pid: Number(dead.stdout.toString()), at: 'x' }))
  const fourth = acquireRunner(c)
  expect('release' in fourth).toBe(true)
  if ('release' in fourth) fourth.release()
})
