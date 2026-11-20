import { expect, test } from 'bun:test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { addEntry, decide, nightlyPath, nightlyReminder, normChecks, pickPr, removeEntry, type RawPr, runQueue, statusText, type Ctx, type NightlyRecord } from './merge-queue'
import { LINUX, WIN, bad, mkBranch, mkRepo, ok, pr, quiet, running, useQueueTestEnv } from './merge-queue.testlib'

useQueueTestEnv()

// 合并队列的测试（第 1 份，共三份；共用的准备在 merge-queue.testlib.ts）：能不能合的判断、整套提醒

test('必过检查都绿、能合：合，说明里每条检查一行，不挡合并的红也写上', () => {
  const d = decide(pr({ checks: [ok(WIN), bad(LINUX)] }), [WIN])
  expect(d.kind).toBe('merge')
  if (d.kind !== 'merge') return
  expect(d.summary).toContain(`${WIN}：绿 https://example.invalid/run/`)
  expect(d.summary).toContain(`${LINUX}：红（FAILURE），不挡合并`)
})

test('两条必过：一条绿一条没出结果、一条绿一条没有记录都不合；一条红出队', () => {
  const req = [WIN, LINUX]
  expect(decide(pr({ checks: [ok(WIN), running(LINUX)] }), req)).toEqual({ kind: 'wait', why: `等 GitHub 检查：${LINUX}` })
  expect(decide(pr({ checks: [ok(WIN)] }), req)).toEqual({ kind: 'wait', why: `等 GitHub 检查：${LINUX}` })
  expect(decide(pr({ checks: [] }), req)).toEqual({ kind: 'wait', why: `等 GitHub 检查：${WIN}、${LINUX}` })
  const red = decide(pr({ checks: [ok(WIN), bad(LINUX)] }), req)
  expect(red.kind).toBe('red')
  expect(red.kind === 'red' && red.why).toContain(`GitHub 检查红：${LINUX}（FAILURE） https://example.invalid/run/`)
  expect(decide(pr({ checks: [ok(WIN), ok(LINUX)] }), req).kind).toBe('merge')
})

test('只有 SUCCESS 算绿：NEUTRAL、SKIPPED、CANCELLED、TIMED_OUT、没有结论都不合', () => {
  for (const c of ['NEUTRAL', 'SKIPPED', 'CANCELLED', 'TIMED_OUT', 'ACTION_REQUIRED', 'STALE', '']) {
    expect(decide(pr({ checks: [bad(WIN, c)] }), [WIN]).kind).toBe('red')
  }
})

test('分支保护没有必过检查、或必过名单是空的：不合', () => {
  expect(decide(pr(), [])).toEqual({ kind: 'wait', why: 'main 的分支保护里没有必过检查，不合（先在 GitHub 上设好）' })
})

test('PR 状态：已合、已关、冲突、草稿、落后、GitHub 还没算出来', () => {
  expect(decide(pr({ state: 'MERGED' }), [WIN]).kind).toBe('merged')
  expect(decide(pr({ state: 'CLOSED' }), [WIN])).toEqual({ kind: 'red', why: 'PR #7 已关闭，没合' })
  expect(decide(pr({ mergeState: 'DIRTY' }), [WIN]).kind).toBe('red')
  expect(decide(pr({ mergeState: 'DRAFT' }), [WIN]).kind).toBe('wait')
  expect(decide(pr({ mergeState: 'BEHIND' }), [WIN])).toEqual({ kind: 'update' })
  expect(decide(pr({ mergeState: 'BEHIND', checks: [running(WIN)] }), [WIN])).toEqual({ kind: 'update' })   // 旧头上的结果反正作废
  expect(decide(pr({ mergeState: 'BEHIND', checks: [bad(WIN)] }), [WIN]).kind).toBe('red')                  // 红了先修，不替它更新
  expect(decide(pr({ mergeState: 'UNKNOWN' }), [WIN]).kind).toBe('wait')
  expect(decide(pr({ mergeState: '' }), [WIN]).kind).toBe('wait')
  expect(decide(pr({ mergeState: 'BLOCKED' }), [WIN]).kind).toBe('wait')    // 必过都绿 GitHub 还说挡着：不交给 gh 去试
  expect(decide(pr({ draft: true }), [WIN]).kind).toBe('wait')                 // 草稿（DRAFT 状态已弃用，看 isDraft）
  expect(decide(pr({ mergeState: 'UNSTABLE' }), [WIN]).kind).toBe('merge')  // 不挡合并的检查红
})

test('同名检查有好几次（重跑）：按最新一次；同一时刻有一份不绿就不合', () => {
  const t1 = '2026-11-19T01:00:00Z', t2 = '2026-11-19T02:00:00Z'
  expect(decide(pr({ checks: [bad(WIN, 'FAILURE', t1), ok(WIN, t2)] }), [WIN]).kind).toBe('merge')
  expect(decide(pr({ checks: [ok(WIN, t1), bad(WIN, 'FAILURE', t2)] }), [WIN]).kind).toBe('red')
  expect(decide(pr({ checks: [ok(WIN, t1), running(WIN, t2)] }), [WIN]).kind).toBe('wait')
  expect(decide(pr({ checks: [ok(WIN, t2), bad(WIN, 'FAILURE', t2)] }), [WIN]).kind).toBe('red')
})

test('同名检查新的一份没有时间（排队中、gh 给零值时间）：当它最新，旧的绿压不住', () => {
  const t1 = '2026-11-19T01:00:00Z'
  for (const at of ['', '0001-01-01T00:00:00Z', '不是时间']) {
    expect(decide(pr({ checks: [ok(WIN, t1), running(WIN, at)] }), [WIN]).kind).toBe('wait')
    expect(decide(pr({ checks: [ok(WIN, t1), bad(WIN, 'FAILURE', at)] }), [WIN]).kind).toBe('red')
  }
  // 带时区偏移的也按真实先后比：+08:00 的 09:30 早于 Z 的 02:00
  expect(decide(pr({ checks: [bad(WIN, 'FAILURE', '2026-11-19T09:30:00+08:00'), ok(WIN, '2026-11-19T02:00:00Z')] }), [WIN]).kind).toBe('merge')
})

test('pickPr：只认本仓库、合进 main 的 PR；fork 里同名分支的、合进别的分支的不算；同时开着两个报错', () => {
  const raw = (n: number, over: Partial<RawPr> = {}): RawPr => ({ number: n, state: 'OPEN', headRefOid: 'a'.repeat(40), mergeStateStatus: 'CLEAN', url: `u${n}`, statusCheckRollup: [], isCrossRepository: false, baseRefName: 'main', ...over })
  expect(pickPr([raw(9, { isCrossRepository: true }), raw(3)], 'main')!.number).toBe(3)
  expect(pickPr([raw(9, { isCrossRepository: true })], 'main')).toBeNull()
  expect(pickPr([raw(9, { baseRefName: 'feat/x' })], 'main')).toBeNull()
  expect(pickPr([raw(9, { isCrossRepository: undefined })], 'main')).toBeNull()   // 字段缺了不当本仓库
  expect(pickPr([raw(5, { state: 'MERGED' }), raw(6)], 'main')!.number).toBe(6)
  expect(pickPr([raw(5, { state: 'MERGED' }), raw(4, { state: 'CLOSED' })], 'main')!.number).toBe(5)
  expect(() => pickPr([raw(5), raw(6)], 'main')).toThrow('这个分支有 2 个开着的 PR（#5、#6）')
  expect(pickPr([raw(5, { isDraft: true })], 'main')!.draft).toBe(true)
})

test('normChecks：Actions 的检查和外部写的提交状态统一成一种', () => {
  const cs = normChecks([
    { __typename: 'CheckRun', name: WIN, status: 'COMPLETED', conclusion: 'SUCCESS', detailsUrl: 'u1', startedAt: 't1' },
    { __typename: 'CheckRun', name: LINUX, status: 'IN_PROGRESS', conclusion: '', detailsUrl: 'u2', startedAt: 't2' },
    { __typename: 'StatusContext', context: 'local-verify', state: 'PENDING', targetUrl: 'u3', startedAt: 't3' },
    { __typename: 'StatusContext', context: 'ci/x', state: 'success', targetUrl: 'u4' },
  ])
  expect(cs).toEqual([
    { name: WIN, done: true, conclusion: 'SUCCESS', url: 'u1', at: 't1' },
    { name: LINUX, done: false, conclusion: '', url: 'u2', at: 't2' },
    { name: 'local-verify', done: false, conclusion: 'PENDING', url: 'u3', at: 't3' },
    { name: 'ci/x', done: true, conclusion: 'SUCCESS', url: 'u4', at: '' },
  ])
  expect(normChecks(null)).toEqual([])
  // 外部写的提交状态当必过：PENDING 等，SUCCESS 合，ERROR 红
  expect(decide(pr({ checks: cs }), ['local-verify']).kind).toBe('wait')
  expect(decide(pr({ checks: cs }), ['ci/x']).kind).toBe('merge')
  expect(decide(pr({ checks: normChecks([{ __typename: 'StatusContext', context: 'ci/x', state: 'ERROR' }]) }), ['ci/x']).kind).toBe('red')
})

// ---------- 整套提醒 ----------
const HOUR = 60 * 60 * 1000
const nightlyRec = (at: number, ok: boolean, failed: string[] = []): NightlyRecord => ({ at: new Date(at).toISOString(), commit: 'abcdef1234567890', tree: 't', ok, seconds: 60, exit: ok ? 0 : 1, output: 'C:\\tmp\\nightly.txt', failed })
const writeNightly = (c: Ctx, rs: NightlyRecord[]) => writeFileSync(join(c.common, 'sumika-nightly.json'), JSON.stringify(rs))

test('status 第一行：没有记录、绿、红三种文案', () => {
  const c = mkRepo()
  expect(statusText(c).split(/\r?\n/)[0]).toBe('上次整套：还没有整套记录（bun scripts/nightly.ts 跑一次）')
  writeNightly(c, [nightlyRec(Date.now() - 2.5 * HOUR, true)])
  expect(statusText(c).split(/\r?\n/)[0]).toMatch(/^上次整套：\d+ 小时前，绿（abcdef12）$/)
  writeNightly(c, [nightlyRec(Date.now() - 2.5 * HOUR, false, ['a', 'b', 'c'])])
  expect(statusText(c).split(/\r?\n/)[0]).toContain('，红（abcdef12，3 个包，输出 C:\\tmp\\nightly.txt）')
})

test('40 小时前绿：status、add、空队列的 run 都提醒；2 小时前绿不提醒', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  mkBranch(c, 'b', 'b.txt', 'b\n')
  writeNightly(c, [nightlyRec(Date.now() - 40.5 * HOUR, true)])
  expect(statusText(c)).toContain('超过 36 小时没有绿的整套（上次绿：')
  expect(addEntry(c, 'a').reminder).toContain('超过 36 小时没有绿的整套')
  removeEntry(c, 'a')
  const logs: string[] = []
  expect(await runQueue(c, { ...quiet, log: (s) => logs.push(s) })).toBe('empty')
  expect(logs.join('\n')).toContain('超过 36 小时没有绿的整套')

  writeNightly(c, [nightlyRec(Date.now() - 2.5 * HOUR, true)])
  expect(statusText(c)).not.toContain('超过 36 小时')
  expect(addEntry(c, 'b').reminder).toBeUndefined()
  removeEntry(c, 'b')
  const logs2: string[] = []
  expect(await runQueue(c, { ...quiet, log: (s) => logs2.push(s) })).toBe('empty')
  expect(logs2.join('\n')).not.toContain('超过 36 小时')
})

test('36 小时阈值边界：35.9 小时不提醒，36.1 小时提醒', () => {
  const now = Date.now()
  expect(nightlyReminder([nightlyRec(now - 35.9 * HOUR, true)], now)).toBeUndefined()
  expect(nightlyReminder([nightlyRec(now - 36.1 * HOUR, true)], now)).toContain('超过 36 小时没有绿的整套')
})

test('旧的绿加更新的红：按最后一次绿算，不按最新一条', () => {
  const now = Date.now()
  expect(nightlyReminder([nightlyRec(now - 2 * HOUR, true), nightlyRec(now - HOUR, false)], now)).toBeUndefined()
  expect(nightlyReminder([nightlyRec(now - 40 * HOUR, true), nightlyRec(now - HOUR, false)], now)).toContain('超过 36 小时没有绿的整套（上次绿：')
})

test('比现在晚 10 分钟以上的绿记录不算，5 分钟内的算', () => {
  const now = Date.now()
  expect(nightlyReminder([nightlyRec(now + 11 * 60_000, true)], now)).toContain('还没有绿的整套记录')
  expect(nightlyReminder([nightlyRec(now + 5 * 60_000, true)], now)).toBeUndefined()
})

test('nightlyPath 认 SUMIKA_NIGHTLY_RECORD', () => {
  const c = mkRepo()
  const old = process.env.SUMIKA_NIGHTLY_RECORD
  try {
    process.env.SUMIKA_NIGHTLY_RECORD = 'D:\\临时\\nightly.json'
    expect(nightlyPath(c)).toBe('D:\\临时\\nightly.json')
  } finally {
    if (old === undefined) delete process.env.SUMIKA_NIGHTLY_RECORD
    else process.env.SUMIKA_NIGHTLY_RECORD = old
  }
})
