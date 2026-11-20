import { expect, test } from 'bun:test'
import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { acquireRunner, addEntry, loadState, mutate, processHead, removeEntry, repoOf, runQueue, saveState } from './merge-queue'
import { FakeHub, cleanEnv, mkBranch, mkRepo, mkRepoWithRemote, quiet, root, sh, useQueueTestEnv } from './merge-queue.testlib'

useQueueTestEnv()

// 合并队列的测试（第 3 份，共三份）：排队、状态文件、锁、命令行

test('add 的拒绝：main、不存在的分支、重复；remove 不删正在跑的', () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  expect(() => addEntry(c, 'main')).toThrow()
  expect(() => addEntry(c, 'nope')).toThrow('没有这个本地分支')
  expect(addEntry(c, 'a').position).toBe(1)
  expect(() => addEntry(c, 'a')).toThrow('已经在队列里')
  mutate(c, (s) => { s.running = { id: s.queue[0].id, branch: 'a', pid: process.pid, since: new Date().toISOString(), phase: '看 PR' } })
  expect(() => removeEntry(c, 'a')).toThrow('正在跑')
  mutate(c, (s) => { s.running = null })
  removeEntry(c, 'a')
  expect(loadState(c.statePath).queue).toEqual([])
  expect(() => removeEntry(c, 'a')).toThrow('队列里没有')
})

test('处理中途本地分支被删：记红不卡队列', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a')
  addEntry(c, 'a')
  sh(c.mainDir, 'branch', '-D', 'a')
  expect(await processHead(c, loadState(c.statePath).queue[0], { ...quiet, hub })).toBe('done')
  expect(loadState(c.statePath).results[0].reason).toContain('不存在')
  expect(hub.merges).toEqual([])
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
  mutate(c, (s) => { s.results.push({ branch: 'q', title: 't', at: 'x', ok: true, reason: '', seconds: 1 }) })
  expect(JSON.parse(readFileSync(c.statePath, 'utf8')).results.length).toBe(1)
  expect(readdirSync(c.common).filter((f) => f.endsWith('.tmp') || f.endsWith('.lock'))).toEqual([])
  writeFileSync(c.statePath, '{半份')
  expect(loadState(c.statePath).queue).toEqual([])
})

test('两个执行者互斥：已有活着的执行者时第二个起不来，释放或进程没了以后能接手', async () => {
  const c = mkRepo()
  const first = acquireRunner(c)
  expect('release' in first).toBe(true)
  const second = acquireRunner(c)
  expect('pid' in second && second.pid).toBe(process.pid)
  const p = Bun.spawnSync([process.execPath, join(import.meta.dir, 'merge-queue.ts'), 'run'], { cwd: c.mainDir, env: cleanEnv() })
  expect(p.exitCode).toBe(4)
  expect(await runQueue(c, quiet)).toBe('busy')
  if ('release' in first) first.release()
  const third = acquireRunner(c)
  expect('release' in third).toBe(true)
  if ('release' in third) third.release()
  const dead = Bun.spawnSync([process.execPath, '-e', 'process.stdout.write(String(process.pid))'], { env: cleanEnv() })
  writeFileSync(join(c.common, 'sumika-merge-queue.runner'), JSON.stringify({ pid: Number(dead.stdout.toString()), at: 'x' }))
  const fourth = acquireRunner(c)
  expect('release' in fourth).toBe(true)
  if ('release' in fourth) fourth.release()
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

test('run --watch 不再有：退出码 2，说明改成收事件再跑', () => {
  const c = mkRepo()
  const p = Bun.spawnSync([process.execPath, join(import.meta.dir, 'merge-queue.ts'), 'run', '--watch'], { cwd: c.mainDir, env: cleanEnv() })
  expect(p.exitCode).toBe(2)
  expect(p.stdout.toString()).toContain('不再有 --watch')
})

test('repoOf：从远端地址推出 owner/name，两种写法都认；不是 GitHub 的报错', () => {
  const c = mkRepo()
  sh(c.mainDir, 'remote', 'add', 'origin', 'https://github.com/tsugumi42/sumika-public.git')
  expect(repoOf(c)).toBe('tsugumi42/sumika-public')
  sh(c.mainDir, 'remote', 'set-url', 'origin', 'git@github.com:tsugumi42/sumika-public')
  expect(repoOf(c)).toBe('tsugumi42/sumika-public')
  expect(repoOf({ ...c, repo: 'a/b' })).toBe('a/b')
  sh(c.mainDir, 'remote', 'set-url', 'origin', 'D:\\别处\\repo.git')
  expect(() => repoOf(c)).toThrow('不是 GitHub 仓库')
  expect(() => repoOf({ ...c, remote: 'nope' })).toThrow('没有这个远端')
})
