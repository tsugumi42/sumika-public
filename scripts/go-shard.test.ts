import { expect, test } from 'bun:test'
import { binaryKey, parseTimes, partition, shardCount, shouldShard } from './go-shard'

test('只分最慢的几个包，且没有自己指定范围', () => {
  const root = 'C:\\Temp\\go-build1\\b001\\sumika-native.test.exe'
  expect(shouldShard(root, ['-test.short=true', '-test.count=1'], 4)).toBe(true)
  expect(shouldShard('/tmp/b001/sumika-native.test', [], 4)).toBe(true)
  expect(shouldShard('C:\\Temp\\b002\\turn.test.exe', ['-test.short=true'], 4)).toBe(false)   // 同上
  expect(shouldShard('C:\\Temp\\b002\\app.test.exe', ['-test.short=true'], 4)).toBe(false)   // 包内已并行，不再分
  expect(shouldShard('C:\\Temp\\b002\\llm.test.exe', ['-test.short=true'], 4)).toBe(false)
  expect(shouldShard(root, ['-test.run=TestA'], 4)).toBe(false)
  expect(shouldShard(root, ['-test.run', 'TestA'], 4)).toBe(false)
  expect(shouldShard(root, ['-test.list=.*'], 4)).toBe(false)
  expect(shouldShard(root, [], 1)).toBe(false)
})

test('每条测试都分到且只分到一份，分法稳定', () => {
  const names = ['TestC', 'TestA', 'TestE', 'TestB', 'TestD']
  const parts = partition(names, 2)
  expect(parts).toEqual([['TestA', 'TestC', 'TestE'], ['TestB', 'TestD']])
  expect(parts.flat().sort()).toEqual([...names].sort())
  expect(partition(names, 2)).toEqual(parts)
  // 份数比测试多：不留空份，空的 -test.run 会什么都不跑却算过
  expect(partition(['TestA'], 4)).toEqual([['TestA']])
})

test('份数：默认核数减 2，至少 1，能用环境变量改', () => {
  expect(shardCount({}, 12)).toBe(10)
  expect(shardCount({}, 6)).toBe(4)
  expect(shardCount({}, 2)).toBe(1)
  expect(shardCount({}, 1)).toBe(1)
  expect(shardCount({ SUMIKA_ROOT_SHARDS: '1' }, 12)).toBe(1)
  expect(shardCount({ SUMIKA_ROOT_SHARDS: '8' }, 12)).toBe(8)
  expect(shardCount({ SUMIKA_ROOT_SHARDS: '不是数' }, 12)).toBe(10)
})

test('按耗时分：慢的先分、给最空的一份；每条只在一份里；没记过的按中位数', () => {
  const times = { TestBig1: 25, TestBig2: 24, TestBig3: 18, TestA: 1, TestB: 1, TestC: 1, TestD: 1 }
  const names = [...Object.keys(times), 'TestNew']
  const parts = partition(names, 3, times)
  expect(parts.flat().sort()).toEqual([...names].sort())
  // 三条大的各占一份，不会两条凑一起（按名字轮流分时 Big1、Big3 会同在一份）
  for (const p of parts) expect(p.filter((n) => n.startsWith('TestBig')).length).toBe(1)
  const load = parts.map((p) => p.reduce((s, n) => s + ((times as Record<string, number>)[n] ?? 1), 0))
  expect(Math.max(...load) - Math.min(...load)).toBeLessThanOrEqual(7)
  expect(partition(names, 3, times)).toEqual(parts)   // 同一份记录分法稳定
  // 记录里一条都对不上：退回按名字轮流分
  expect(partition(['TestB', 'TestA'], 2, { TestZ: 3 })).toEqual([['TestA'], ['TestB']])
})

test('从 -test.v 输出取顶层耗时；子测试不算；go test -json 的 test2json 行首控制字符也认', () => {
  const out = [
    '=== RUN   TestA', '--- PASS: TestA (1.25s)', '=== RUN   TestB', '    --- PASS: TestB/子 (0.10s)', '--- FAIL: TestB (3.50s)',
    String.fromCharCode(0x16) + '--- PASS: TestC (0.00s)', '--- SKIP: TestD (0.01s)', 'PASS',
  ].join(String.fromCharCode(10))
  expect(parseTimes(out)).toEqual({ TestA: 1.25, TestB: 3.5, TestC: 0, TestD: 0.01 })
  expect(binaryKey(['C:', 'Temp', 'b001', 'sumika-native.test.exe'].join(String.fromCharCode(92)))).toBe('sumika-native')
  expect(binaryKey('/tmp/b002/app.test')).toBe('app')
})
