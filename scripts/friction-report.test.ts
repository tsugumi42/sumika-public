import { Database } from 'bun:sqlite'
import { afterAll, describe, expect, test } from 'bun:test'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { aggregate, copyTrace, localDate, parseSince, pitsOf, render, run, scanSessions, scanTrace, scratchBase, scrub, shape, similar, type Hit } from './friction-report'

// 所有名字、号码、路径都是编的。
const NOW = Date.now()
const DAY = 86_400_000
mkdirSync(scratchBase(), { recursive: true })
const root = mkdtempSync(join(scratchBase(), 'test-'))
afterAll(() => rmSync(root, { recursive: true, force: true }))
let counter = 0
const newDir = () => { const d = join(root, `case${++counter}`); mkdirSync(d); return d }

// ---------- 造会话记录 ----------

type Ev = Record<string, unknown>
const iso = (ms: number) => new Date(ms).toISOString()
let uid = 0
const T0 = NOW - 3_600_000

function prompt(sid: string, t: number): Ev {
  return { type: 'user', sessionId: sid, timestamp: iso(t), uuid: `u${++uid}`, message: { role: 'user', content: '编的用户提示' } }
}
function use(sid: string, t: number, msgId: string, id: string, name: string, input: Record<string, unknown>, usage?: { total: number; hit?: number; out?: number }): Ev {
  const hit = usage?.hit ?? 0.9
  return {
    type: 'assistant', sessionId: sid, timestamp: iso(t), uuid: `a${++uid}`,
    message: {
      id: msgId, role: 'assistant', content: [{ type: 'tool_use', id, name, input }],
      ...(usage ? { usage: { input_tokens: 0, cache_creation_input_tokens: Math.round(usage.total * (1 - hit)), cache_read_input_tokens: Math.round(usage.total * hit), output_tokens: usage.out ?? 10 } } : {}),
    },
  }
}
function result(sid: string, t: number, id: string, isError: boolean, text = 'ok'): Ev {
  return { type: 'user', sessionId: sid, timestamp: iso(t), uuid: `r${++uid}`, message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: id, is_error: isError, content: text }] } }
}
function write(dir: string, sid: string, evs: Ev[]) {
  writeFileSync(join(dir, `${sid}.jsonl`), evs.map((e) => JSON.stringify(e)).join('\n') + '\n')
}
async function scan(evs: Ev[], sid = 'sess-aaaaaaaa-0011'): Promise<Hit[]> {
  const d = newDir()
  write(d, sid, evs)
  return scanSessions(d, NOW - 8 * DAY)
}
const has = (hits: Hit[], part: string) => hits.filter((h) => h.feature.includes(part))

// ---------- 造 trace 库（按迁移文件建表） ----------

function makeTraceDb(path: string): Database {
  const dir = resolve(import.meta.dir, '..', 'native', 'internal', 'store', 'migrations', 'trace')
  const db = new Database(path)
  for (const f of readdirSync(dir).filter((n) => n.endsWith('.sql')).sort()) {
    const up = readFileSync(join(dir, f), 'utf8').split(/^-- \+goose Down/m)[0].replace(/^-- \+goose.*$/gm, '')
    db.exec(up)
  }
  return db
}
let rid = 0
function addRun(db: Database, o: { ch?: string; conv?: string; stage?: string; at: number; input: number; cacheRead: number; out?: number }) {
  db.query(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, input_tokens, output_tokens, cache_read_tokens, sections_json, injected_json) VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
    .run(`run${++rid}`, o.ch ?? 'char-a', o.conv ?? 'local:private:1', o.stage ?? 'reply', 'fake-model', o.at, o.input, o.out ?? 10, o.cacheRead, '[]', '[]')
}
function addStep(db: Database, o: { turn: string; name: string; args: unknown; err?: boolean; at: number }) {
  db.query(`INSERT INTO steps (id, turn_id, kind, name, args_json, is_error, started_at) VALUES (?,?,?,?,?,?,?)`)
    .run(`st${++rid}`, o.turn, 'tool', o.name, JSON.stringify(o.args), o.err ? 1 : 0, o.at)
}
function addGate(db: Database, o: { ch?: string; conv: string; at: number; verdict: string }) {
  db.query(`INSERT INTO gate_decisions (at, character_id, conversation, seq, batch, frequency, verdict, reason) VALUES (?,?,?,?,?,?,?,?)`)
    .run(o.at, o.ch ?? 'char-a', o.conv, 1, 1, 0.5, o.verdict, '编的原因')
}
function addTask(db: Database, at: number, module: string, cls: string, outcome = 'failed') {
  db.query(`INSERT INTO task_runs (at, module, outcome, class) VALUES (?,?,?,?)`).run(at, module, outcome, cls)
}
const traceScan = (db: Database) => scanTrace(db, NOW - 8 * DAY, NOW - DAY, NOW)

// ---------- 工具摩擦 ①：失败后换参数重试 ----------

describe('工具摩擦 ① 失败后重试', () => {
  test('正：Bash 失败后 3 步内又跑相似命令，且估出多花的 token', async () => {
    const s = 'sess-aaaaaaaa-0011'
    const hits = await scan([
      prompt(s, T0),
      use(s, T0 + 1, 'm1', 't1', 'Bash', { command: 'rg "foo" src/a' }, { total: 1000, out: 50 }),
      result(s, T0 + 2, 't1', true, '出错'),
      use(s, T0 + 3, 'm2', 't2', 'Bash', { command: 'rg "bar" src/b' }, { total: 1500, out: 80 }),
      result(s, T0 + 4, 't2', false),
    ], s)
    const h = has(hits, '失败后换参数重试')
    expect(h.length).toBe(1)
    expect(h[0].est).toBe(80 + 500)
    expect(h[0].where).toContain('***11')
  })
  test('反：失败后换了别的工具，或隔了 3 步以上，或隔了一轮，都不算', async () => {
    const s = 'sess-bbbbbbbb-0022'
    const filler = (i: number) => [use(s, T0 + 10 * i, `f${i}`, `tf${i}`, 'Read', { file_path: `x${i}` }), result(s, T0 + 10 * i + 1, `tf${i}`, false)]
    const hits = await scan([
      prompt(s, T0),
      use(s, T0 + 1, 'm1', 't1', 'Bash', { command: 'rg "foo" src/a' }),
      result(s, T0 + 2, 't1', true),
      use(s, T0 + 3, 'm2', 't2', 'Read', { file_path: 'q' }), result(s, T0 + 4, 't2', false),
      // 第二个失败：之后隔 3 个别的调用才出现相似命令
      use(s, T0 + 5, 'm3', 't3', 'Bash', { command: 'git status' }), result(s, T0 + 6, 't3', true),
      ...filler(1), ...filler(2), ...filler(3),
      use(s, T0 + 100, 'm9', 't9', 'Bash', { command: 'git status' }), result(s, T0 + 101, 't9', false),
      // 第三个失败后换了一轮才重试
      use(s, T0 + 110, 'm10', 't10', 'Bash', { command: 'bun test' }), result(s, T0 + 111, 't10', true),
      prompt(s, T0 + 120),
      use(s, T0 + 121, 'm11', 't11', 'Bash', { command: 'bun test' }), result(s, T0 + 122, 't11', false),
    ], s)
    expect(has(hits, '失败后换参数重试')).toEqual([])
  })
})

// ---------- ② ----------

describe('工具摩擦 ② 同一命令重复', () => {
  test('正：一轮里同一命令跑两次', async () => {
    const s = 'sess-cccccccc-0033'
    const hits = await scan([
      prompt(s, T0),
      use(s, T0 + 1, 'm1', 't1', 'Bash', { command: 'bun test scripts/x.test.ts' }), result(s, T0 + 2, 't1', false),
      use(s, T0 + 3, 'm2', 't2', 'Bash', { command: 'bun   test scripts/x.test.ts' }), result(s, T0 + 4, 't2', false),
    ], s)
    expect(has(hits, '同一命令一轮里重复执行').length).toBe(1)
  })
  test('反：不同轮、或命令不同', async () => {
    const s = 'sess-dddddddd-0044'
    const hits = await scan([
      prompt(s, T0),
      use(s, T0 + 1, 'm1', 't1', 'Bash', { command: 'bun test a' }), result(s, T0 + 2, 't1', false),
      use(s, T0 + 3, 'm2', 't2', 'Bash', { command: 'bun test b' }), result(s, T0 + 4, 't2', false),
      prompt(s, T0 + 5),
      use(s, T0 + 6, 'm3', 't3', 'Bash', { command: 'bun test a' }), result(s, T0 + 7, 't3', false),
    ], s)
    expect(has(hits, '同一命令一轮里重复执行')).toEqual([])
  })
})

// ---------- ③ ----------

describe('工具摩擦 ③ Edit 找不到原文后重读', () => {
  const edit = (s: string, ok: boolean, readPath: string): Ev[] => [
    prompt(s, T0),
    use(s, T0 + 1, 'm1', 't1', 'Edit', { file_path: 'proj/a.ts', old_string: 'x', new_string: 'y' }),
    result(s, T0 + 2, 't1', !ok, ok ? 'updated' : 'String to replace not found in file.'),
    use(s, T0 + 3, 'm2', 't2', 'Read', { file_path: readPath }), result(s, T0 + 4, 't2', false),
  ]
  test('正', async () => { expect(has(await scan(edit('sess-eeeeeeee-0055', false, 'proj/a.ts'), 'sess-eeeeeeee-0055'), 'Edit：找不到原文').length).toBe(1) })
  test('反：Edit 成功，或读的是别的文件', async () => {
    expect(has(await scan(edit('sess-ffffffff-0066', true, 'proj/a.ts'), 'sess-ffffffff-0066'), 'Edit：找不到原文')).toEqual([])
    expect(has(await scan(edit('sess-gggggggg-0077', false, 'proj/b.ts'), 'sess-gggggggg-0077'), 'Edit：找不到原文')).toEqual([])
  })
})

// ---------- ④ 已知的坑 ----------

describe('工具摩擦 ④ 已知的坑', () => {
  const pos: [string, string][] = [
    ['`/tmp` 路径', 'echo hi > /tmp/out.txt'],
    ['`> nul` 重定向', 'dir C:\\x > nul'],
    ['`cmd /c start`', 'cmd /c start /low /wait /b go test'],
    ['`/c/` 式盘符路径', 'ls /c/Users/fake/a'],
    ['heredoc 写含反斜杠的内容', "cat <<'EOF' > a.txt\nline with \\n inside\nEOF"],
  ]
  const neg: string[] = ['ls /tmpfoo/x', 'echo "a > nullable"', 'cmd /c dir', 'ls /cache/x', "cat <<'EOF' > a.txt\nplain line\nEOF", 'git status']
  test.each(pos)('正：%s', (feature, cmd) => {
    expect(pitsOf(cmd)).toContain(feature)
  })
  test.each(neg)('反：%s', (cmd) => { expect(pitsOf(cmd)).toEqual([]) })
  test('会话里命中后进报告特征；CRLF 警告文字看结果', async () => {
    const s = 'sess-hhhhhhhh-0088'
    const hits = await scan([
      prompt(s, T0),
      use(s, T0 + 1, 'm1', 't1', 'Bash', { command: 'echo hi > /tmp/o' }), result(s, T0 + 2, 't1', false),
      use(s, T0 + 3, 'm2', 't2', 'Bash', { command: 'git add .' }), result(s, T0 + 4, 't2', false, 'warning: LF will be replaced by CRLF in a.txt'),
      use(s, T0 + 5, 'm3', 't3', 'Bash', { command: 'git add .' }), result(s, T0 + 6, 't3', false, '干净'),
    ], s)
    expect(has(hits, '`/tmp` 路径').length).toBe(1)
    expect(has(hits, 'CRLF 换行警告').length).toBe(1)
    expect(has(hits, 'CRLF 换行警告')[0].advice).toBe('观察')
    expect(has(hits, 'CRLF 换行警告')[0].noRepeat).toBe(true)
  })

  const one = async (s: string, cmd: string, err: boolean, after: Ev[] = []) =>
    scan([prompt(s, T0), use(s, T0 + 1, 'm1', 't1', 'Bash', { command: cmd }), result(s, T0 + 2, 't1', err), ...after], s)
  const more = (s: string, name: string, input: Record<string, unknown>, k = 1): Ev[] => [use(s, T0 + 10 * k, `mm${k}`, `tt${k}`, name, input), result(s, T0 + 10 * k + 1, `tt${k}`, false)]

  test('盘符路径：正（同一次调用报错 / 交给 go、python、.exe、powershell）', async () => {
    expect(has(await one('sess-uuuuuuuu-0110', 'ls /d/fake/a', true), '盘符路径').length).toBe(1)
    expect(has(await one('sess-vvvvvvvv-0111', 'go test /d/fake/pkg', false), '盘符路径').length).toBe(1)
    expect(has(await one('sess-wwwwwwww-0112', 'python /c/fake/a.py', false), '盘符路径').length).toBe(1)
    expect(has(await one('sess-xxxxxxxx-0113', '/c/fake/a.exe /c/fake/in.txt', false), '盘符路径').length).toBe(1)
  })
  test('盘符路径：反（Git Bash 自己用、没报错；程序名只是路径的一部分）', async () => {
    expect(has(await one('sess-yyyyyyyy-0114', 'cd /d/fake/proj && ls', false), '盘符路径')).toEqual([])
    expect(has(await one('sess-yyyyyyyy-0122', 'cd /d/fake/proj && go test ./...', false), '盘符路径')).toEqual([])      // go 在那个目录里跑，路径不是它的参数
    expect(has(await one('sess-zzzzzzzz-0115', 'ls /d/fake/google/x', false), '盘符路径')).toEqual([])
  })
  const HD = ["cat <<'EOF' > out/a.txt", 'line with \\n inside', 'EOF'].join('\n')
  test('heredoc：正（同一次调用报错；3 步内又 Write 同一文件；又 heredoc 写同一文件）', async () => {
    expect(has(await one('sess-hd000001-0116', HD, true), 'heredoc').length).toBe(1)
    expect(has(await one('sess-hd000002-0117', HD, false, more('sess-hd000002-0117', 'Write', { file_path: 'out/a.txt' })), 'heredoc').length).toBe(1)
    expect(has(await one('sess-hd000003-0118', HD, false, more('sess-hd000003-0118', 'Bash', { command: HD })), 'heredoc').length).toBe(1)
  })
  test('heredoc：反（成功且之后没再碰那个文件；3 步以后才改；改的是别的文件）', async () => {
    expect(has(await one('sess-hd000004-0119', HD, false), 'heredoc')).toEqual([])
    const s5 = 'sess-hd000005-0120'
    const late = [...more(s5, 'Read', { file_path: 'q1' }, 1), ...more(s5, 'Read', { file_path: 'q2' }, 2), ...more(s5, 'Read', { file_path: 'q3' }, 3), ...more(s5, 'Write', { file_path: 'out/a.txt' }, 4)]
    expect(has(await one(s5, HD, false, late), 'heredoc')).toEqual([])
    expect(has(await one('sess-hd000006-0121', HD, false, more('sess-hd000006-0121', 'Write', { file_path: 'out/other.txt' })), 'heredoc')).toEqual([])
  })
})

// ---------- 成本异常 ----------

describe('成本异常 ① 缓存命中率骤降', () => {
  const seq = (s: string, a: { total: number; hit: number }, b: { total: number; hit: number }, gap = 1000): Ev[] => [
    prompt(s, T0),
    use(s, T0 + 1, 'm1', 't1', 'Read', { file_path: 'a' }, a), result(s, T0 + 2, 't1', false),
    use(s, T0 + 1 + gap, 'm2', 't2', 'Read', { file_path: 'b' }, b), result(s, T0 + 2 + gap, 't2', false),
  ]
  test('正：命中率 0.9 → 0.2 且总量差不多', async () => {
    const hits = await scan(seq('sess-iiiiiiii-0099', { total: 50_000, hit: 0.9 }, { total: 52_000, hit: 0.2 }), 'sess-iiiiiiii-0099')
    expect(has(hits, '缓存命中率掉').length).toBe(1)
  })
  test('反：只掉 20 个百分点；总量翻倍（多半是真的读了新东西）；隔了很久（缓存过期）', async () => {
    expect(has(await scan(seq('sess-jjjjjjjj-0100', { total: 50_000, hit: 0.9 }, { total: 50_000, hit: 0.7 }), 'sess-jjjjjjjj-0100'), '缓存命中率掉')).toEqual([])
    expect(has(await scan(seq('sess-kkkkkkkk-0101', { total: 50_000, hit: 0.9 }, { total: 100_000, hit: 0.2 }), 'sess-kkkkkkkk-0101'), '缓存命中率掉')).toEqual([])
    expect(has(await scan(seq('sess-llllllll-0102', { total: 50_000, hit: 0.9 }, { total: 52_000, hit: 0.2 }, 2 * 3_600_000), 'sess-llllllll-0102'), '缓存命中率掉')).toEqual([])
  })
  test('间隔：4 分钟内的骤降算，6 分钟以上缓存已过期不算', async () => {
    expect(has(await scan(seq('sess-pppppppp-0105', { total: 50_000, hit: 0.9 }, { total: 52_000, hit: 0.2 }, 4 * 60_000), 'sess-pppppppp-0105'), '缓存命中率掉').length).toBe(1)
    expect(has(await scan(seq('sess-qqqqqqqq-0106', { total: 50_000, hit: 0.9 }, { total: 52_000, hit: 0.2 }, 6 * 60_000), 'sess-qqqqqqqq-0106'), '缓存命中率掉')).toEqual([])
  })
  test('trace 的 model_runs 同样判（总量 = input + cacheRead）', () => {
    const db = makeTraceDb(':memory:')
    addRun(db, { at: NOW - 5000, input: 1000, cacheRead: 9000 })
    addRun(db, { at: NOW - 4000, input: 8200, cacheRead: 2000 })
    expect(has(traceScan(db).hits, '缓存命中率掉').length).toBe(1)
    const db2 = makeTraceDb(':memory:')
    addRun(db2, { at: NOW - 5000, input: 1000, cacheRead: 9000 })
    addRun(db2, { at: NOW - 4000, input: 2000, cacheRead: 8000 })
    expect(has(traceScan(db2).hits, '缓存命中率掉')).toEqual([])
  })
})

describe('成本异常 ② 单轮输入总量超过中位数 3 倍', () => {
  const totals = (s: string, last: number): Ev[] => {
    const evs: Ev[] = [prompt(s, T0)]
    ;[10_000, 10_000, 10_000, 10_000, last].forEach((total, i) => evs.push(use(s, T0 + i * 1000, `m${i}`, `t${i}`, 'Read', { file_path: `f${i}` }, { total, hit: 0.9 }), result(s, T0 + i * 1000 + 1, `t${i}`, false)))
    return evs
  }
  test('正：40k 对中位数 10k', async () => {
    expect(has(await scan(totals('sess-mmmmmmmm-0103', 40_000), 'sess-mmmmmmmm-0103'), '超过本会话中位数 3 倍').length).toBe(1)
  })
  test('反：刚好 3 倍不算', async () => {
    expect(has(await scan(totals('sess-nnnnnnnn-0104', 30_000), 'sess-nnnnnnnn-0104'), '超过本会话中位数 3 倍')).toEqual([])
  })
  test('反：总量不到 20k 不算（即便超过 3 倍）', async () => {
    const s = 'sess-rrrrrrrr-0107'
    const evs: Ev[] = [prompt(s, T0)]
    ;[2000, 2000, 2000, 2000, 9000].forEach((total, i) => evs.push(use(s, T0 + i * 1000, `m${i}`, `t${i}`, 'Read', { file_path: `f${i}` }, { total }), result(s, T0 + i * 1000 + 1, `t${i}`, false)))
    expect(has(await scan(evs, s), '超过本会话中位数 3 倍')).toEqual([])
  })
  test('反：会话第一轮不算；压缩后的第一轮不算；正：压缩之后第二轮的暴涨照算', async () => {
    const s = 'sess-ssssssss-0108'
    const first: Ev[] = [prompt(s, T0)]
    ;[80_000, 10_000, 10_000, 10_000, 10_000].forEach((total, i) => first.push(use(s, T0 + i * 1000, `m${i}`, `t${i}`, 'Read', { file_path: `f${i}` }, { total }), result(s, T0 + i * 1000 + 1, `t${i}`, false)))
    expect(has(await scan(first, s), '超过本会话中位数 3 倍')).toEqual([])
    const s2 = 'sess-tttttttt-0109'
    const comp: Ev[] = [prompt(s2, T0)]
    ;[10_000, 10_000, 10_000, 10_000].forEach((total, i) => comp.push(use(s2, T0 + i * 1000, `m${i}`, `t${i}`, 'Read', { file_path: `f${i}` }, { total }), result(s2, T0 + i * 1000 + 1, `t${i}`, false)))
    comp.push({ type: 'system', subtype: 'compact_boundary', sessionId: s2, timestamp: iso(T0 + 5000), uuid: 'sys1', message: {} })
    comp.push(use(s2, T0 + 6000, 'mc1', 'tc1', 'Read', { file_path: 'c1' }, { total: 60_000 }), result(s2, T0 + 6001, 'tc1', false))
    expect(has(await scan(comp, s2), '超过本会话中位数 3 倍')).toEqual([])
    comp.push(use(s2, T0 + 7000, 'mc2', 'tc2', 'Read', { file_path: 'c2' }, { total: 60_000 }), result(s2, T0 + 7001, 'tc2', false))
    expect(has(await scan(comp, s2), '超过本会话中位数 3 倍').length).toBe(1)
  })
})

describe('成本异常 ③ 群聊里没有放行记录却调了回复模型', () => {
  const conv = 'qq:group:13579'
  const F = '没有任何放行记录'
  test('正：群里的回复调用，前 10 分钟内没有 enter / forced / merged', () => {
    const db = makeTraceDb(':memory:')
    addRun(db, { conv, at: NOW - 9_000, input: 100, cacheRead: 0, out: 20 })
    const h = has(traceScan(db).hits, F)
    expect(h.length).toBe(1)
    expect(h[0].est).toBe(120)
  })
  test('正：只有 skip，或放行是 11 分钟前', () => {
    const db = makeTraceDb(':memory:')
    addGate(db, { conv, at: NOW - 10_000, verdict: 'skip' })
    addGate(db, { conv: 'qq:group:2468', at: NOW - 11 * 60_000, verdict: 'enter' })
    addRun(db, { conv, at: NOW - 9_000, input: 100, cacheRead: 0 })
    addRun(db, { conv: 'qq:group:2468', at: NOW - 9_000, input: 100, cacheRead: 0 })
    expect(has(traceScan(db).hits, F).length).toBe(2)
  })
  test('反：先 skip 后 enter 再调用（2026-11-11 核实的真实形态）；forced；merged；别的角色的放行不算数的反面见下', () => {
    for (const verdict of ['enter', 'forced', 'merged']) {
      const db = makeTraceDb(':memory:')
      addGate(db, { conv, at: NOW - 80_000, verdict: 'skip' })
      addGate(db, { conv, at: NOW - 10_000, verdict })
      addRun(db, { conv, at: NOW - 9_000, input: 100, cacheRead: 0 })
      expect(has(traceScan(db).hits, F), verdict).toEqual([])
    }
  })
  test('反：私聊不管；放行在调用后 1 秒内（记录先后不一定对齐）也算', () => {
    const db = makeTraceDb(':memory:')
    addRun(db, { conv: 'qq:private:555', at: NOW - 9_000, input: 100, cacheRead: 0 })
    addGate(db, { conv: 'qq:group:1004', at: NOW - 8_500, verdict: 'enter' })
    addRun(db, { conv: 'qq:group:1004', at: NOW - 9_000, input: 100, cacheRead: 0 })
    expect(has(traceScan(db).hits, F)).toEqual([])
  })
  test('正：放行在调用之后 5 秒才记，不算这次调用的放行', () => {
    const db = makeTraceDb(':memory:')
    addGate(db, { conv, at: NOW - 4_000, verdict: 'enter' })
    addRun(db, { conv, at: NOW - 9_000, input: 100, cacheRead: 0 })
    expect(has(traceScan(db).hits, F).length).toBe(1)
  })
  test('别的角色的放行不算数', () => {
    const db = makeTraceDb(':memory:')
    addGate(db, { ch: 'char-b', conv, at: NOW - 10_000, verdict: 'enter' })
    addRun(db, { conv, at: NOW - 9_000, input: 100, cacheRead: 0 })
    expect(has(traceScan(db).hits, F).length).toBe(1)
  })
})

// ---------- trace 里的工具调用（和会话记录同一套规则） ----------

describe('trace steps 的工具规则', () => {
  test('正：失败后相似调用、同一命令重复、已知的坑', () => {
    const db = makeTraceDb(':memory:')
    addStep(db, { turn: 'turn-1', name: 'exec', args: { command: 'rg "a" dirA' }, err: true, at: NOW - 5000 })
    addStep(db, { turn: 'turn-1', name: 'exec', args: { command: 'rg "b" dirB' }, at: NOW - 4000 })
    addStep(db, { turn: 'turn-1', name: 'exec', args: { command: 'rg "b" dirB' }, at: NOW - 3000 })
    addStep(db, { turn: 'turn-1', name: 'exec', args: { command: 'echo x > /tmp/y' }, at: NOW - 2000 })
    const h = traceScan(db).hits
    expect(has(h, '失败后换参数重试').length).toBe(1)
    expect(has(h, '同一命令一轮里重复执行').length).toBe(1)
    expect(has(h, '`/tmp` 路径').length).toBe(1)
    expect(has(h, '失败后换参数重试')[0].est).toBeNull()
  })
  test('反：不同回合不算重复', () => {
    const db = makeTraceDb(':memory:')
    addStep(db, { turn: 'turn-1', name: 'exec', args: { command: 'bun test x' }, at: NOW - 5000 })
    addStep(db, { turn: 'turn-2', name: 'exec', args: { command: 'bun test x' }, at: NOW - 4000 })
    expect(has(traceScan(db).hits, '同一命令')).toEqual([])
  })
})

// ---------- 插件和网络 ①：task_runs 和前 7 天比 ----------

describe('插件失败次数和前 7 天平均比', () => {
  test('正：本期 4 次、之前没有', () => {
    const db = makeTraceDb(':memory:')
    for (let i = 0; i < 4; i++) addTask(db, NOW - 1000 * (i + 1), 'link', 'network')
    expect(traceScan(db).plugins.map((p) => `${p.module}/${p.cls}/${p.n}`)).toEqual(['link/network/4'])
  })
  test('反：只有 2 次；或前 7 天一直这么多；成功不算', () => {
    const db = makeTraceDb(':memory:')
    for (let i = 0; i < 2; i++) addTask(db, NOW - 1000 * (i + 1), 'link', 'network')
    for (let i = 0; i < 4; i++) addTask(db, NOW - 1000 * (i + 1), 'send', 'timeout', 'ok')
    for (let i = 0; i < 4; i++) addTask(db, NOW - 3000 - i, 'media', 'network')
    for (let i = 0; i < 70; i++) addTask(db, NOW - DAY - (i + 1) * 3_600_000 * 2, 'media', 'network')
    expect(traceScan(db).plugins).toEqual([])
  })
})

// ---------- 反复出现 + 整份报告 + 隐私 ----------

describe('整份报告', () => {
  const SECRET_USER = 'fakeuser42'
  const SECRET_GROUP = '54321'
  const SECRET_CMD = 'rg "very-secret-pattern-xyz" C:\\Users\\fakeuser42\\proj\\secret-notes'
  const SECRET_TEXT = '聊天里的编造原文-瓜子花生'

  test('反复 3 天出现的写进第三节，2 天的不写；隐私一个都不出现；临时目录用完删掉', async () => {
    const dir = newDir()
    const s = 'sess-oooooooo-0200'
    const evs: Ev[] = []
    // 今天、1 天前、2 天前各踩一次 /tmp；另一个特征只有两天
    const days = [0, 1, 2]
    days.forEach((d, i) => {
      const t = NOW - d * DAY - 60_000
      evs.push(prompt(s, t),
        use(s, t + 1, `p${i}`, `tp${i}`, 'Bash', { command: `echo ${SECRET_TEXT} > /tmp/x` }), result(s, t + 2, `tp${i}`, false, SECRET_TEXT))
    })
    ;[0, 1].forEach((d, i) => {
      const t = NOW - d * DAY - 120_000
      evs.push(use(s, t, `q${i}`, `tq${i}`, 'Bash', { command: 'ls /c/Users/a' }), result(s, t + 1, `tq${i}`, false))
    })
    // 带隐私的失败重试
    evs.push(
      prompt(s, T0),
      use(s, T0 + 1, 'z1', 'tz1', 'Bash', { command: SECRET_CMD }, { total: 1000 }), result(s, T0 + 2, 'tz1', true, `出错 ${SECRET_TEXT} C:\\Users\\${SECRET_USER}\\x`),
      use(s, T0 + 3, 'z2', 'tz2', 'Bash', { command: SECRET_CMD + ' -n' }, { total: 1300 }), result(s, T0 + 4, 'tz2', false),
    )
    write(dir, s, evs.sort((a, b) => Date.parse(a.timestamp as string) - Date.parse(b.timestamp as string)))

    const tracePath = join(dir, 'trace.db')
    const tdb = makeTraceDb(tracePath)
    tdb.exec('PRAGMA journal_mode=WAL')
    addGate(tdb, { conv: `qq:group:${SECRET_GROUP}`, at: NOW - 10_000, verdict: 'skip' })
    addRun(tdb, { conv: `qq:group:${SECRET_GROUP}`, at: NOW - 9_000, input: 500, cacheRead: 0 })
    addStep(tdb, { turn: `turn-${SECRET_GROUP}`, name: 'exec', args: { command: `cat C:\\Users\\${SECRET_USER}\\notes > /tmp/a` }, at: NOW - 5000 })
    // 不关库：模拟正在被写（WAL 里有没合并的内容）

    const before = new Set(readdirSync(scratchBase()))
    const out = join(dir, 'out.md')
    const { report } = await run({ since: NOW - DAY, now: NOW, trace: tracePath, sessions: dir, out, network: false, snowluma: join(dir, 'none') })
    tdb.close()

    for (const secret of [SECRET_USER, SECRET_GROUP, 'very-secret-pattern-xyz', 'secret-notes', SECRET_TEXT, 'C:\\Users', 'sess-oooooooo', 'turn-' + SECRET_GROUP]) {
      expect(report).not.toContain(secret)
    }
    expect(report).toContain('## 一、摩擦榜')
    expect(report).toContain('## 二、成本和网络')
    expect(report).toContain('## 三、反复出现')
    expect(report).toContain('未查（--no-network）')
    const repeatSection = report.split('## 三、反复出现')[1]
    expect(repeatSection).toContain('`/tmp` 路径')
    expect(repeatSection).toContain('3 天出现')
    expect(repeatSection).not.toContain('`/c/` 式盘符路径')
    // 摩擦榜按估计 token 排：带估计的重试在没估计的前面，且用的是命令形状
    expect(report).toContain('rg <模式> <路径>')
    // trace 里的群聊对话也打了码
    expect(report).toContain('***21')
    // 临时副本删干净了
    expect(new Set(readdirSync(scratchBase()))).toEqual(before)
  })

  test('常驻命中第三节最多两行，其余写“另有 N 条常驻”；CRLF 警告不进反复出现', async () => {
    const dir = newDir()
    const s = 'sess-resident-0300'
    const evs: Ev[] = []
    for (let d = 0; d < 7; d++) {
      const t = NOW - d * DAY - 60_000
      let k = 0
      evs.push(prompt(s, t - 1))
      for (const cmd of ['echo a > /tmp/x', 'dir z > nul', 'cmd /c start foo', 'git add .', 'ls /c/fake/q']) {
        if (cmd.startsWith('ls') && d >= 4) continue      // 只有 4 天出现、而且报了错的盘符路径
        const id = `r${d}_${k++}`
        evs.push(use(s, t + k, `m${id}`, `t${id}`, 'Bash', { command: cmd }), result(s, t + k + 0.5, `t${id}`, cmd.startsWith('ls'), cmd === 'git add .' ? 'LF will be replaced by CRLF' : 'ok'))
      }
    }
    write(dir, s, evs.sort((a, b) => Date.parse(a.timestamp as string) - Date.parse(b.timestamp as string)))
    const { report } = await run({ since: NOW - DAY, now: NOW, trace: join(dir, '无.db'), sessions: dir, out: join(dir, 'o.md'), network: false, snowluma: dir })
    const sec = report.split('## 三、反复出现')[1]
    expect(sec.match(/7 天出现/g)?.length).toBe(2)
    expect(sec).toContain('另有 1 条常驻')
    expect(sec).toContain('盘符路径，最近 7 天里 4 天出现')      // 不到 6 天的不算常驻，照常列出
    expect(sec).not.toContain('CRLF')
  })

  test('没有命中写“没有”', async () => {
    const dir = newDir()
    const { report } = await run({ since: NOW - DAY, now: NOW, trace: join(dir, '不存在.db'), sessions: dir, out: join(dir, 'o.md'), network: false, snowluma: dir })
    expect(report).toContain('没有')
    expect(report).toContain('trace 库：不存在')
  })
})

// ---------- 小函数 ----------

describe('小函数', () => {
  test('parseSince', () => {
    expect(parseSince('24h', 1_000_000_000)).toBe(1_000_000_000 - 24 * 3_600_000)
    expect(parseSince('3d', 1_000_000_000)).toBe(1_000_000_000 - 3 * DAY)
    expect(parseSince('2026-11-10T00:00:00Z', 0)).toBe(Date.parse('2026-11-10T00:00:00Z'))
    expect(() => parseSince('昨天', 0)).toThrow()
  })
  test('shape 只留程序名和形状', () => {
    expect(shape('rg "secret thing" C:\\Users\\bob\\proj')).toBe('rg <模式> <路径>')
    expect(shape('git commit -m "msg with secrets"')).toBe('git commit -m <参数>')
    expect(shape('FOO=bar bun test scripts/a.test.ts | head -5')).toBe('bun test <路径> | head -5')
  })
  test('similar：30% 以内算相似', () => {
    expect(similar('rg <模式> <路径>', 'rg <模式> <路径> -n')).toBe(true)
    expect(similar('git status', 'bun test scripts')).toBe(false)
  })
  test('scrub 打码用户名路径和长数字', () => {
    expect(scrub('C:\\Users\\bob\\x 与 /home/amy/y 群 13579')).toBe('C:\\Users\\***\\x 与 /home/***/y 群 ***79')
  })
  test('aggregate 按估计 token 排序，未估的靠后', () => {
    const h = (feature: string, est: number | null): Hit => ({ cat: '工具摩擦', feature, ts: 1, where: 'w', est, advice: '观察' })
    expect(aggregate([h('a', null), h('b', 5), h('b', 7), h('c', 9)]).map((r) => `${r.feature}:${r.n}:${r.est}`)).toEqual(['b:2:12', 'c:1:9', 'a:1:null'])
  })
  test('copyTrace 复制到临时目录且清理干净', () => {
    const dir = newDir()
    const src = join(dir, 'a.db')
    makeTraceDb(src).close()
    const c = copyTrace(src)
    expect(existsSync(c.path)).toBe(true)
    c.done()
    expect(existsSync(c.path)).toBe(false)
    expect(localDate(NOW)).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})

test('CRLF 换行提示不进摩擦榜，只在榜下记个数', () => {
  const T = 1_790_000_000_000
  const crlfHit = { cat: '工具摩擦', feature: 'Bash：输出里有 CRLF 换行警告', ts: T, where: '会话 x', est: null, advice: '观察', noRepeat: true }
  const md = render({ since: T - 1000, now: T + 1000, hits: [crlfHit as any, crlfHit as any], all: [], net: null, notes: [] })
  const top = md.split('## 二')[0]!
  expect(top).not.toContain('| 工具摩擦 | Bash：输出里有 CRLF')
  expect(top).toContain('CRLF 换行提示 2 次')
})
