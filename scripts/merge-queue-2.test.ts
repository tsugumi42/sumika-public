import { expect, test } from 'bun:test'
import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { addEntry, loadState, runQueue, statusText } from './merge-queue'
import { FakeHub, LINUX, WIN, bad, mainSha, mkBranch, mkRepoWithRemote, ok, quiet, remoteSha, running, sh, useQueueTestEnv } from './merge-queue.testlib'

useQueueTestEnv()

// 合并队列的测试（第 2 份，共三份）：按 GitHub 检查合并的整个流程。远端是本地裸仓库，GitHub 是假的（testlib 的 FakeHub）

test('两条排队：第一条绿了合；第二条落后 main，替它更新分支、等检查；检查绿了再跑一次合上，本机 main 跟上', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n'); mkBranch(c, 'b', 'b.txt', 'b\n')
  hub.open('a', [ok(WIN), bad(LINUX)]); hub.open('b', [ok(WIN)])
  addEntry(c, 'a', 'feat(测试): 合 a', 'a 的正文\n\nCo-Authored-By: x')
  addEntry(c, 'b', 'feat(测试): 合 b', 'b 的正文')

  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges.map((m) => m.number)).toEqual([1])
  expect(hub.merges[0].subject).toBe('feat(测试): 合 a')
  expect(hub.merges[0].body).toContain('a 的正文\n\nCo-Authored-By: x\n\nGitHub 检查（PR #1，头 ')
  expect(hub.merges[0].body).toContain(`${WIN}：绿`)
  expect(hub.merges[0].body).toContain(`${LINUX}：红（FAILURE），不挡合并`)
  expect(hub.updates).toEqual([2])
  let s = loadState(c.statePath)
  expect(s.queue.map((e) => [e.branch, e.waiting])).toEqual([['b', 'PR #2 落后 main，已点“更新分支”，等 GitHub 检查重跑']])
  expect(s.results.map((r) => [r.branch, r.ok, r.pr])).toEqual([['a', true, 'https://example.invalid/pull/1']])
  expect(mainSha(c)).toBe(remoteSha(c))                       // 本机 main 快进到了远端的合并提交
  expect(sh(c.mainDir, 'log', '-1', '--format=%s', 'main')).toBe('feat(测试): 合 a')

  // 检查还在跑：再跑一次，还是等，不合
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges).toHaveLength(1)
  expect(loadState(c.statePath).queue[0].waiting).toBe(`等 GitHub 检查：${WIN}`)

  // 检查出结果（会话收到事件）：再跑一次合上
  hub.set('b', { checks: [ok(WIN, '2026-11-19T03:00:00Z')], mergeState: 'CLEAN' })
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  expect(hub.merges.map((m) => m.number)).toEqual([1, 2])
  s = loadState(c.statePath)
  expect(s.queue).toEqual([])
  expect(s.running).toBeNull()
  expect(s.results.map((r) => [r.branch, r.ok])).toEqual([['a', true], ['b', true]])
  expect(mainSha(c)).toBe(remoteSha(c))
  expect(sh(c.mainDir, 'log', '--first-parent', '--merges', '--format=%s', 'main').split(/\r?\n/)).toEqual(['feat(测试): 合 b', 'feat(测试): 合 a'])
  expect(statusText(c)).toContain('https://example.invalid/pull/2')
})

test('必过检查红：出队记红带链接，不合，远端和本机 main 都不动', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a', [bad(WIN)])
  addEntry(c, 'a')
  const before = remoteSha(c)
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  expect(hub.merges).toEqual([])
  const r = loadState(c.statePath).results[0]
  expect([r.ok, r.reason]).toEqual([false, `GitHub 检查红：${WIN}（FAILURE） https://example.invalid/run/${encodeURIComponent(WIN)}`])
  expect(remoteSha(c)).toBe(before)
  expect(mainSha(c)).toBe(before)
})

test('检查还没出结果：留在队首、后面的不越过它，不合', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n'); mkBranch(c, 'b', 'b.txt', 'b\n')
  hub.open('a', [running(WIN)]); hub.open('b', [ok(WIN)])
  addEntry(c, 'a'); addEntry(c, 'b')
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges).toEqual([])
  const s = loadState(c.statePath)
  expect(s.queue.map((e) => e.branch)).toEqual(['a', 'b'])
  expect(s.queue[0].waiting).toBe(`等 GitHub 检查：${WIN}`)
  expect(s.running).toBeNull()
  expect(statusText(c)).toContain(`【等 GitHub 检查：${WIN}】`)
})

test('判断以后 PR 头又变了：GitHub 拒绝合并（--match-head-commit），留在队首；新头的检查绿了才合', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a', [ok(WIN)])
  addEntry(c, 'a')
  // 在“看了检查”和“合并”之间有人往分支推了新提交
  const realMerge = hub.merge.bind(hub)
  let raced = false
  hub.merge = (p, subject, body) => {
    if (!raced) {
      raced = true
      sh(c.mainDir, 'checkout', '-q', 'a'); writeFileSync(join(c.mainDir, 'a2.txt'), 'x\n'); sh(c.mainDir, 'add', '.'); sh(c.mainDir, 'commit', '-qm', '又一个'); sh(c.mainDir, 'checkout', '-q', 'main')
      sh(c.mainDir, 'push', '-q', 'origin', 'a')
      hub.set('a', { head: remoteSha(c, 'a'), checks: [running(WIN, '2026-11-19T02:00:00Z')], mergeState: 'BLOCKED' })
    }
    return realMerge(p, subject, body)
  }
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges).toEqual([])
  expect(loadState(c.statePath).queue[0].waiting).toContain('gh pr merge 没成功：Head branch was modified')
  // 再跑：新头的检查还在跑，等
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(loadState(c.statePath).queue[0].waiting).toBe(`等 GitHub 检查：${WIN}`)
  hub.set('a', { checks: [ok(WIN, '2026-11-19T03:00:00Z')], mergeState: 'CLEAN' })
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  expect(hub.merges.map((m) => m.head)).toEqual([sh(c.mainDir, 'rev-parse', 'a')])
  expect(sh(c.mainDir, 'show', 'main:a2.txt')).toBe('x')
})

test('本地分支有没推上去的提交：不合，提示先推；推了以后照常', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a', [ok(WIN)])
  sh(c.mainDir, 'checkout', '-q', 'a'); writeFileSync(join(c.mainDir, 'late.txt'), 'late\n'); sh(c.mainDir, 'add', '.'); sh(c.mainDir, 'commit', '-qm', '后补'); sh(c.mainDir, 'checkout', '-q', 'main')
  addEntry(c, 'a')
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges).toEqual([])
  expect(loadState(c.statePath).queue[0].waiting).toMatch(/^本地 a（[0-9a-f]{8}）有没推上去的提交，PR 头是 [0-9a-f]{8}：推上去再合$/)
  sh(c.mainDir, 'push', '-q', 'origin', 'a')
  hub.set('a', { head: remoteSha(c, 'a'), checks: [ok(WIN, '2026-11-19T03:00:00Z')] })
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  expect(sh(c.mainDir, 'show', 'main:late.txt')).toBe('late')
})

test('PR 头比本地新（点过“更新分支”、本地没拉）：照常合', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'x', 'x.txt', 'x\n'); mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('x'); hub.open('a')
  addEntry(c, 'x'); addEntry(c, 'a')
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')   // x 合了，a 被更新、等检查
  expect(sh(c.mainDir, 'rev-parse', 'a')).not.toBe(hub.prs.get('a')!.head)
  hub.set('a', { checks: [ok(WIN, '2026-11-19T03:00:00Z')], mergeState: 'CLEAN' })
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  expect(loadState(c.statePath).results.map((r) => [r.branch, r.ok])).toEqual([['x', true], ['a', true]])
})

test('没有 PR、PR 已关：出队记红；PR 已经在 GitHub 上合了：记绿、本机 main 跟上', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'none', 'n.txt', 'n\n'); mkBranch(c, 'closed', 'c.txt', 'c\n'); mkBranch(c, 'done', 'd.txt', 'd\n')
  hub.open('closed'); hub.set('closed', { state: 'CLOSED' })
  const p = hub.open('done')
  hub.merge(p, '网页上合的', '')
  addEntry(c, 'none'); addEntry(c, 'closed'); addEntry(c, 'done')
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  const rs = loadState(c.statePath).results
  expect(rs.map((r) => [r.branch, r.ok])).toEqual([['none', false], ['closed', false], ['done', true]])
  expect(rs[0].reason).toBe('没找到 none 合进 main 的 PR：先推到 origin、gh pr create 再排')
  expect(rs[1].reason).toBe('PR #1 已关闭，没合')
  expect(rs[2].reason).toBe('PR #2 已经在 GitHub 上合了')
  expect(mainSha(c)).toBe(remoteSha(c))
  expect(hub.merges).toHaveLength(1)   // 只有上面手动那一次
})

test('同名分支以前合过一个 PR、之后又有新提交：不算合了，记红', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.merge(hub.open('a'), '以前合的', '')
  sh(c.mainDir, 'checkout', '-q', 'a'); writeFileSync(join(c.mainDir, 'more.txt'), 'm\n'); sh(c.mainDir, 'add', '.'); sh(c.mainDir, 'commit', '-qm', '又改'); sh(c.mainDir, 'checkout', '-q', 'main')
  addEntry(c, 'a')
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  expect(loadState(c.statePath).results[0]).toMatchObject({ ok: false, reason: 'a 的 PR #1 以前合过，之后的提交不在 main 里：再开一个 PR' })
})

test('问 GitHub 出错（找 PR、读分支保护）：留在队首写原因，不合', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a')
  addEntry(c, 'a')
  hub.requiredErr = '读不到 main 的分支保护（HTTP 404）'
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(loadState(c.statePath).queue[0].waiting).toBe('问 GitHub 出错：读不到 main 的分支保护（HTTP 404）')
  hub.requiredErr = ''; hub.findErr = 'gh pr list 失败：network'
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges).toEqual([])
  hub.findErr = ''; hub.requiredNames = []
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(loadState(c.statePath).queue[0].waiting).toBe('main 的分支保护里没有必过检查，不合（先在 GitHub 上设好）')
  expect(hub.merges).toEqual([])
})

test('本机 main 对不齐时不硬来：工作区有改动、本机 main 有远端没有的提交都只写说明；main 没检出时直接挪指针', async () => {
  // 工作区有没提交的改动
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a'); addEntry(c, 'a')
  writeFileSync(join(c.mainDir, 'base.txt'), '改了没提交\n')
  const before = mainSha(c)
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  let r = loadState(c.statePath).results[0]
  expect(r.ok).toBe(true)
  expect(r.reason).toContain('本机 main 没对齐：')
  expect(r.reason).toContain('有没提交的改动，干净后 git pull --ff-only')
  expect(mainSha(c)).toBe(before)

  // 本机 main 有远端没有的提交
  const c2 = mkRepoWithRemote()
  const hub2 = new FakeHub(c2)
  mkBranch(c2, 'a', 'a.txt', 'a\n')
  hub2.open('a'); addEntry(c2, 'a')
  writeFileSync(join(c2.mainDir, 'local.txt'), 'l\n'); sh(c2.mainDir, 'add', '.'); sh(c2.mainDir, 'commit', '-qm', '本机自己的')
  const local = mainSha(c2)
  expect(await runQueue(c2, { ...quiet, hub: hub2 })).toBe('empty')
  r = loadState(c2.statePath).results[0]
  expect(r.reason).toContain(`本机 main 有远端没有的提交（${local.slice(0, 8)}），没对齐`)
  expect(mainSha(c2)).toBe(local)

  // main 没在任何工作区检出
  const c3 = mkRepoWithRemote()
  const hub3 = new FakeHub(c3)
  mkBranch(c3, 'a', 'a.txt', 'a\n')
  hub3.open('a'); addEntry(c3, 'a')
  sh(c3.mainDir, 'checkout', '-q', '-b', 'other')
  expect(await runQueue(c3, { ...quiet, hub: hub3 })).toBe('empty')
  expect(loadState(c3.statePath).results[0].reason).not.toContain('没对齐')
  expect(mainSha(c3)).toBe(remoteSha(c3))
})

test('GitHub 还说能合（状态没重算），但 PR 头没包含最新 main：当落后处理，替它更新分支，不合', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a')
  addEntry(c, 'a')
  // 别人在网页上合了别的 PR，main 前进了；GitHub 对 a 的状态还是旧的 CLEAN
  mkBranch(c, 'other', 'o.txt', 'o\n')
  hub.merge(hub.open('other'), '别人合的', '')
  hub.set('a', { mergeState: 'CLEAN' })
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges.map((m) => m.subject)).toEqual(['别人合的'])
  expect(hub.updates).toEqual([1])
  expect(loadState(c.statePath).queue[0].waiting).toBe('PR #1 落后 main，已点“更新分支”，等 GitHub 检查重跑')
})

test('分支保护没开“要求分支是最新的”：不合', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a'); addEntry(c, 'a')
  hub.strict = false
  expect(await runQueue(c, { ...quiet, hub })).toBe('blocked')
  expect(hub.merges).toEqual([])
  expect(loadState(c.statePath).queue[0].waiting).toBe('main 的分支保护没开“要求分支是最新的”，不合（先在 GitHub 上设好）')
})

test('main 检出在一个目录已经删掉的 worktree 里：合并照常记绿，本机 main 照样跟上，不卡队列', async () => {
  const c = mkRepoWithRemote()
  const hub = new FakeHub(c)
  mkBranch(c, 'a', 'a.txt', 'a\n')
  hub.open('a'); addEntry(c, 'a')
  sh(c.mainDir, 'checkout', '-q', '-b', 'other')
  const gone = join(c.mainDir, '..', `gone-${Date.now()}`)
  sh(c.mainDir, 'worktree', 'add', '-q', gone, 'main')
  rmSync(gone, { recursive: true, force: true })
  expect(await runQueue(c, { ...quiet, hub })).toBe('empty')
  const r = loadState(c.statePath).results[0]
  expect(r.ok).toBe(true)
  expect(sh(c.mainDir, 'rev-parse', 'refs/heads/main')).toBe(remoteSha(c))
})
