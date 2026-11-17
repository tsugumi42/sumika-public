// run-low.ps1 的名额：bun test 和 go test 排同一把锁；已经拿着名额的子孙进程不再抢（否则嵌套会永远等）。
// 用事件同步，不量墙钟：A 拿到名额后写“开始了”、等“放行”文件才结束；A 没结束时 B 不能开始。
// 锁是全机的：测试用 SUMIKA_SLOT_NS 给锁名加一个这次运行独有的后缀，和真实名额完全分开。
// 以前直接借 dev 档，有代理正占着 dev 档时三条各等满 60 秒、合并队列红了（2026-11-12）。
import { expect, test } from 'bun:test'
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const RL = join(import.meta.dir, 'run-low.ps1')
const NS = `rltest-${process.pid}-${Date.now().toString(36)}`
const env = (extra: Record<string, string> = {}) => {
  const e: Record<string, string | undefined> = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_') && k !== 'SUMIKA_SLOT_HELD'))
  return { ...e, SUMIKA_TEST_LANE: 'dev', SUMIKA_SLOT_NS: NS, ...extra }
}
const until = async (ok: () => boolean, ms = 60_000) => { const end = Date.now() + ms; while (!ok()) { if (Date.now() > end) throw new Error('等太久了'); await Bun.sleep(50) } }
const lowBun = (cwd: string, file: string, extra: Record<string, string> = {}) =>
  Bun.spawn(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', RL, process.execPath, 'test', file], { cwd, env: env(extra), stdout: 'pipe', stderr: 'pipe' })

// 写一个测试文件：开始时写 started-<名>，等到 release-<名> 出现才结束
function holder(dir: string, name: string) {
  const f = join(dir, `${name}.test.ts`)
  writeFileSync(f, [
    "import { test } from 'bun:test'",
    "import { existsSync, writeFileSync } from 'node:fs'",
    "import { join } from 'node:path'",
    `test('${name}', async () => {`,
    `  writeFileSync(join(import.meta.dir, 'started-${name}'), '')`,
    `  while (!existsSync(join(import.meta.dir, 'release-${name}'))) await Bun.sleep(50)`,
    '}, 120_000)',
  ].join('\n'))
  return `./${name}.test.ts`
}

test('bun test 也排名额：A 拿着时 B 不开始，A 放行后 B 才开始', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-slot-'))
  try {
    const a = lowBun(dir, holder(dir, 'a'))
    await until(() => existsSync(join(dir, 'started-a')))
    const b = lowBun(dir, holder(dir, 'b'))
    await Bun.sleep(1500)   // 给 B 足够的时间：要是不排队，它这时早就开始了
    expect(existsSync(join(dir, 'started-b'))).toBe(false)
    writeFileSync(join(dir, 'release-a'), '')
    expect(await a.exited).toBe(0)
    await until(() => existsSync(join(dir, 'started-b')))
    writeFileSync(join(dir, 'release-b'), '')
    expect(await b.exited).toBe(0)
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 180_000)

test('已经拿着这一档名额（SUMIKA_SLOT_HELD）的不再抢：A 拿着时，标了已持有的 C 照样开始', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-held-'))
  try {
    const a = lowBun(dir, holder(dir, 'a'))
    await until(() => existsSync(join(dir, 'started-a')))
    const c = lowBun(dir, holder(dir, 'c'), { SUMIKA_SLOT_HELD: 'dev' })
    await until(() => existsSync(join(dir, 'started-c')))   // 不排队：A 还拿着名额它也开始了
    writeFileSync(join(dir, 'release-c'), '')
    expect(await c.exited).toBe(0)
    writeFileSync(join(dir, 'release-a'), '')
    expect(await a.exited).toBe(0)
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 180_000)

test('红的 bun test 退出码原样带出来', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-red-'))
  try {
    writeFileSync(join(dir, 'bad.test.ts'), "import { expect, test } from 'bun:test'\ntest('坏', () => { expect(1).toBe(2) })\n")
    expect(await lowBun(dir, './bad.test.ts').exited).toBe(1)
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 60_000)

// 内存上限（2026-11-17）：单个进程超过 SUMIKA_TEST_PROC_MEM_GB 再申请内存就失败，测试红而不是拖垮整台电脑。
// 起一个小 bun 脚本按块申请并写满内存；不经名额（不是 test 子命令），和真实名额无关
const lowScript = (cwd: string, mb: number, extra: Record<string, string>) => {
  const f = join(cwd, 'eat.ts')
  writeFileSync(f, `const keep: Uint8Array[] = []\nfor (let i = 0; i < ${mb / 25}; i++) keep.push(new Uint8Array(25 << 20).fill(1))\nconsole.log('吃完了', keep.length)\n`)
  return Bun.spawn(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', RL, process.execPath, f], { cwd, env: env(extra), stdout: 'pipe', stderr: 'pipe' })
}

test('单个进程超过内存上限会被拦下，退出码非零并明说碰到了上限', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-mem-'))
  try {
    const p = lowScript(dir, 1500, { SUMIKA_TEST_PROC_MEM_GB: '0.5' })
    const code = await p.exited
    const out = (await new Response(p.stdout).text()) + (await new Response(p.stderr).text())
    expect(code).not.toBe(0)
    expect(out).not.toContain('吃完了')
    expect(out).toMatch(/内存峰值|内存上限/)
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 120_000)

test('没超上限照常跑完', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-mem-ok-'))
  try {
    const p = lowScript(dir, 300, { SUMIKA_TEST_PROC_MEM_GB: '2' })
    const code = await p.exited
    const out = await new Response(p.stdout).text()
    expect(code).toBe(0)
    expect(out).toContain('吃完了')
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 120_000)

test('上限写错就报错退出，不悄悄当成不设', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-mem-bad-'))
  try {
    for (const bad of ['八个G', 'NaN', 'Infinity', '1e30', '0.01', '-1']) {
      const p = lowScript(dir, 50, { SUMIKA_TEST_PROC_MEM_GB: bad })
      expect(await p.exited).toBe(2)
    }
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 60_000)

// Go 进程撞上限后有时卡住不退（复核实测卡过五分钟）：run-low 自己盯峰值，到九成五整串收掉，不能等它自己退出
test('Go 测试吃到上限九成五以上后卡住，run-low 自己整串收掉并给出 137', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'run-low-mem-go-'))
  try {
    writeFileSync(join(dir, 'go.mod'), 'module memprobe\n\ngo 1.21\n')
    writeFileSync(join(dir, 'eat_test.go'), [
      // 吃到上限（2 GB）的九成六左右就停住一直睡：模拟“分配失败后卡住不退”，只有 run-low 自己收才会结束
      'package memprobe',
      'import ("testing"; "time")',
      'var keep [][]byte',
      'func TestEat(t *testing.T) {',
      '	for i := 0; i < 246; i++ {',
      '		b := make([]byte, 8<<20)',
      '		for j := range b { b[j] = 1 }',
      '		keep = append(keep, b)',
      '	}',
      '	time.Sleep(time.Hour)',
      '}',
    ].join('\n'))
    const p = Bun.spawn(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', RL, 'go', 'test', '-count=1', '-timeout=2h', '.'], { cwd: dir, env: env({ SUMIKA_TEST_PROC_MEM_GB: '2' }), stdout: 'pipe', stderr: 'pipe' })
    const code = await Promise.race([p.exited, Bun.sleep(150_000).then(() => 'stuck')])
    if (code === 'stuck') { p.kill(); throw new Error('到了上限九成五以上，150 秒还没收掉') }
    const out = (await new Response(p.stdout).text()) + (await new Response(p.stderr).text())
    expect(code).toBe(137)
    expect(out).toContain('碰到了单进程内存上限')
  } finally { rmSync(dir, { recursive: true, force: true }) }
}, 200_000)
