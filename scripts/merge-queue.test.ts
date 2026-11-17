import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { acquireRunner, addEntry, conclusionOf, git, loadState, makeCtx, mutate, nightlyPath, nightlyReminder, processHead, removeEntry, runnerAlive, runQueue, saveState, statusText, type Ctx, type NightlyRecord, type Verify } from './merge-queue'
import { cleanEnv, green, mainSha, mkBranch, mkRepo, quiet, root, sh, useQueueTestEnv } from './merge-queue.testlib'

useQueueTestEnv()

// 合并队列的测试（第 1 份，共三份；共用的准备在 merge-queue.testlib.ts）

test('按排队顺序一条条合，每条都在前一条合完的 main 上验', async () => {
  const c = mkRepo()
  for (const b of ['a', 'b', 'c']) mkBranch(c, b, `${b}.txt`, `${b}\n`)
  for (const b of ['a', 'b', 'c']) addEntry(c, b, `feat(测试): 合 ${b}`, `${b} 的正文\n\nCo-Authored-By: x`)
  const seen: string[] = []
  const verify: Verify = (a) => { seen.push(`${a.entry.branch}:${['a', 'b', 'c'].filter((x) => existsSync(join(a.wt, `${x}.txt`))).join('')}`); return green(a) }
  expect(await runQueue(c, { ...quiet, verify })).toBe('empty')
  expect(seen).toEqual(['a:a', 'b:ab', 'c:abc'])   // 验 b 的时候 a 已经在树里
  const merges = sh(c.mainDir, 'log', '--first-parent', '--merges', '--format=%s').split(/\r?\n/)
  expect(merges).toEqual(['feat(测试): 合 c', 'feat(测试): 合 b', 'feat(测试): 合 a'])
  const msg = sh(c.mainDir, 'log', '-1', '--format=%B', 'main')
  expect(msg).toContain('c 的正文')
  expect(msg).toContain('合并队列验证：go vet 过、go test 1 个包过')
  expect(sh(c.mainDir, 'rev-parse', 'main^2')).toBe(sh(c.mainDir, 'rev-parse', 'c'))
  const s = loadState(c.statePath)
  expect(s.queue).toEqual([])
  expect(s.running).toBeNull()
  expect(s.results.map((r) => [r.branch, r.ok])).toEqual([['a', true], ['b', true], ['c', true]])
  expect(statusText(c)).toContain('绿')
})

test('验证红：不动 main，原因和输出文件记下来', async () => {
  const c = mkRepo()
  mkBranch(c, 'bad', 'bad.txt', 'bad\n')
  mkBranch(c, 'env', 'env.txt', 'env\n')
  addEntry(c, 'bad'); addEntry(c, 'env')
  const before = mainSha(c)
  const verify: Verify = ({ entry, append }) => {
    if (entry.branch === 'bad') { append('--- FAIL: TestX (0.1s)\nFAIL\tm/b\t3.0s\n有检查红了。没过的 1 个：b\n'); return { ok: false, code: 1 } }
    append('工作区里有 Go 测试会读到、还没提交的改动\n'); return { ok: false, code: 2 }
  }
  await runQueue(c, { ...quiet, verify })
  expect(mainSha(c)).toBe(before)
  const [a, b] = loadState(c.statePath).results
  expect(a.ok).toBe(false)
  expect(a.reason).toContain('验证红')
  expect(a.reason).toContain('FAIL')
  expect(readFileSync(a.outFile!, 'utf8')).toContain('有检查红了')
  expect(a.outFile).toMatch(/queue-bad-\d{8}-\d{6}\.txt$/)
  expect(b.reason).toContain('环境错误')
  expect(loadState(c.statePath).queue).toEqual([])
  expect(statusText(c)).toContain('红')
})

test('只有冲突的那条在跑时 main 不动', async () => {
  const c = mkRepo()
  mkBranch(c, 'x', 'base.txt', 'X\nline2\nline3\n')
  sh(c.mainDir, 'checkout', '-q', 'main')
  writeFileSync(join(c.mainDir, 'base.txt'), 'M\nline2\nline3\n'); sh(c.mainDir, 'commit', '-qam', 'main 改了同一行')
  const before = mainSha(c)
  addEntry(c, 'x')
  await runQueue(c, { ...quiet, verify: () => { throw new Error('不该验证') } })
  expect(mainSha(c)).toBe(before)
  expect(loadState(c.statePath).results[0].reason).toBe('冲突')
})

test('处理中途分支被删：记红不卡队列', async () => {
  const c = mkRepo()
  mkBranch(c, 'a', 'a.txt', 'a\n')
  addEntry(c, 'a')
  sh(c.mainDir, 'branch', '-D', 'a')
  const s = loadState(c.statePath)
  expect(await processHead(c, s.queue[0], { ...quiet, verify: green })).toBe('done')
  expect(loadState(c.statePath).results[0].reason).toContain('不存在')
})

// ---------- 每日整套显示（规格-每日整套 验收第 3 条） ----------
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

test('run --watch 空闲时每小时最多提醒一次', async () => {
  const c = mkRepo()
  const t0 = Date.now()
  writeNightly(c, [nightlyRec(t0 - 40 * HOUR, true)])
  const logs: string[] = []
  const clock = { t: t0 }
  let naps = 0
  await expect(runQueue(c, {
    ...quiet, watch: true, log: (s) => logs.push(s), now: () => clock.t,
    sleep: async () => { naps++; clock.t += 30 * 60_000; if (naps > 4) throw new Error('停') },
  })).rejects.toThrow('停')
  expect(logs.filter((l) => l.includes('超过 36 小时没有绿的整套'))).toHaveLength(3)
})

