import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { acquireRunner, addEntry, conclusionOf, git, loadState, makeCtx, mutate, processHead, removeEntry, runnerAlive, runQueue, saveState, statusText, type Ctx, type Verify } from './merge-queue'
import { cleanEnv, green, mainSha, mkBranch, mkRepo, quiet, root, sh, useQueueTestEnv } from './merge-queue.testlib'

useQueueTestEnv()

// 合并队列的测试（第 3 份，共三份；共用的准备在 merge-queue.testlib.ts）

test('主工作区有没提交的改动：不快进、留在队首；干净后接着快进，不重验', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  mkBranch(c, 'b', 'b.txt', 'b\n')
  addEntry(c, 'a'); addEntry(c, 'b')
  writeFileSync(join(c.mainDir, 'base.txt'), '没提交的改动\n')
  const before = mainSha(c)
  let calls = 0
  const verify: Verify = (x) => { calls++; return green(x) }
  expect(await runQueue(c, { ...quiet, verify })).toBe('blocked')
  expect(mainSha(c)).toBe(before)
  expect(readFileSync(join(c.mainDir, 'base.txt'), 'utf8')).toBe('没提交的改动\n')   // 没碰用户的改动
  let s = loadState(c.statePath)
  expect(s.queue.map((e) => e.branch)).toEqual(['a', 'b'])   // a 还在队首，b 不抢先
  expect(s.queue[0].waiting).toContain('等主工作区干净')
  expect(s.results).toEqual([])
  expect(statusText(c)).toContain('等主工作区干净')
  // 未跟踪文件不算脏
  sh(c.mainDir, 'checkout', '--', 'base.txt')
  writeFileSync(join(c.mainDir, 'untracked.txt'), 'u\n')
  expect(await runQueue(c, { ...quiet, verify })).toBe('empty')
  expect(calls).toBe(2)   // a 只验了一次（第二轮直接用上一轮验好的），b 验一次
  s = loadState(c.statePath)
  expect(s.results.map((r) => [r.branch, r.ok])).toEqual([['a', true], ['b', true]])
  expect(existsSync(join(c.mainDir, 'b.txt'))).toBe(true)
})

test('main 一直在变：重合三轮后记红，不动 main 也不无限转', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  addEntry(c, 'a')
  let calls = 0
  const verify: Verify = (x) => { sh(c.mainDir, 'commit', '--allow-empty', '-qm', `抢 ${++calls}`); return green(x) }
  await runQueue(c, { ...quiet, verify })
  expect(calls).toBe(3)
  const r = loadState(c.statePath).results[0]
  expect(r.ok).toBe(false)
  expect(r.reason).toContain('一直在变')
  expect(existsSync(join(c.mainDir, 'a.txt'))).toBe(false)
})

test('几个进程同时 add 不丢条目（读改写有锁）', async () => {
  const c = mkRepo()
  const names = ['p1', 'p2', 'p3', 'p4', 'p5', 'p6']
  for (const b of names) mkBranch(c, b, `${b}.txt`, `${b}\n`)
  const bodyFile = join(root, 'body.txt')
  writeFileSync(bodyFile, '正文第一行\r\n第二行\r\n\r\nCo-Authored-By: x\r\n')
  const procs = names.map((b) => Bun.spawn([process.execPath, join(import.meta.dir, 'merge-queue.ts'), 'add', b, '--title', `feat(测试): 合 ${b}`, '--body-file', bodyFile], { cwd: c.mainDir, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' }))
  const codes = await Promise.all(procs.map((p) => p.exited))
  expect(codes).toEqual(names.map(() => 0))
  const s = loadState(c.statePath)
  expect(s.queue.map((e) => e.branch).sort()).toEqual(names)
  expect(s.queue[0].body).toBe('正文第一行\n第二行\n\nCo-Authored-By: x')
  expect(s.queue.find((e) => e.branch === 'p3')!.title).toBe('feat(测试): 合 p3')
  expect(readdirSync(c.common).filter((f) => f.endsWith('.tmp') || f.endsWith('.lock'))).toEqual([])
})

test('main 没在主工作区检出时用带旧值的 update-ref 推进，且 main 被抢先动了就不覆盖', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  sh(c.mainDir, 'checkout', '-q', '-b', 'other')
  addEntry(c, 'a')
  await runQueue(c, { ...quiet, verify: green })
  expect(sh(c.mainDir, 'rev-parse', 'main^2')).toBe(sh(c.mainDir, 'rev-parse', 'a'))
  expect(existsSync(join(c.mainDir, 'a.txt'))).toBe(false)   // 主工作区在 other 上，没被动
})

test('分支已经在 main 里：记绿、没有要合的', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  sh(c.mainDir, 'merge', '-q', '--ff-only', 'a')
  addEntry(c, 'a')
  const before = mainSha(c)
  await runQueue(c, { ...quiet, verify: () => { throw new Error('不该验证') } })
  expect(mainSha(c)).toBe(before)
  expect(loadState(c.statePath).results[0].reason).toContain('已经在 main 里')
})

test('runnerAlive：执行者锁文件里的 pid 活着才算有执行者（常驻执行者空闲时 running 是空的）', () => {
  const d = mkdtempSync(join(tmpdir(), 'mq-runner-'))
  try {
    const ctx = { statePath: join(d, 'sumika-merge-queue.json') } as Ctx
    expect(runnerAlive(ctx)).toBe(false)                                         // 没有锁文件
    writeFileSync(join(d, 'sumika-merge-queue.runner'), JSON.stringify({ pid: process.pid }))
    expect(runnerAlive(ctx)).toBe(true)                                          // 自己的进程当然活着
    writeFileSync(join(d, 'sumika-merge-queue.runner'), JSON.stringify({ pid: 2147483646 }))
    expect(runnerAlive(ctx)).toBe(false)                                         // 早没了的进程
    writeFileSync(join(d, 'sumika-merge-queue.runner'), '写了一半')
    expect(runnerAlive(ctx)).toBe(false)
  } finally { rmSync(d, { recursive: true, force: true }) }
})
