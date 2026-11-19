// ci-test-report：缺依赖而跳过的用例在 CI 上判红，其余跳过只汇总不判；输入是 go test -json 的原样输出。
import { afterAll, expect, test } from 'bun:test'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { MUST_HAVE, parse, skipReason, summarize } from './ci-test-report'
import { download, sha256 } from './ci-assets'

const dirs: string[] = []
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })

// 一条用例在 go test -json 里的样子：RUN、输出几行、结论
const ev = (pkg: string, test: string | undefined, action: string, output?: string) => JSON.stringify({ Action: action, Package: pkg, Test: test, Output: output })
function run(pkg: string, name: string, action: 'pass' | 'fail' | 'skip', lines: string[] = []) {
  return [ev(pkg, name, 'run'), ev(pkg, name, 'output', `=== RUN   ${name}\n`), ...lines.map((l) => ev(pkg, name, 'output', l + '\n')), ev(pkg, name, 'output', `--- ${action.toUpperCase()}: ${name} (0.00s)\n`), ev(pkg, name, action)]
}
const sample = [
  ...run('m/linkcard', 'TestTweet', 'skip', ['    tweet_test.go:59: 找不到文渊圆体（native/fonts/WenYuanRoundedSCVF.ttf 不进 git），跳过要真字体的卡片测试']),
  ...run('m/linkcard', 'TestEmoji', 'skip', ['    emoji_test.go:19: 找不到 Twemoji（native/twemoji 不进 git），跳过彩色表情的断言']),
  ...run('m/tools', 'TestSameAsBun/grep-basic', 'skip', ['    golden_test.go:150: 原因：这条用真 rg 的行为，vendor/bin 和 PATH 里都没有它']),
  ...run('m/media', 'TestVideo', 'skip', ['    video_test.go:126: 没有 ffmpeg/ffprobe']),
  ...run('m/ui', 'TestShot', 'skip', ['    shot_test.go:10: 设 CHATUI_SHOT 才出图']),
  ...run('m/ui', 'TestNoReason', 'skip'),
  ...run('m/ui', 'TestRed', 'fail', ['    red_test.go:3: 编的失败原因']),
  ...run('m/ui', 'TestOK', 'pass'),
  ev('m/ui', undefined, 'output', 'FAIL\n'),
  ev('m/ui', undefined, 'fail'),
  '不是 JSON 的一行（go 自己打的）',
].join('\n')

test('按原因汇总跳过；缺依赖的四类挑出来，出图开关、没写原因的不算', () => {
  const s = summarize(parse(sample))
  expect([...s.skips.keys()]).toContain('设 CHATUI_SHOT 才出图')
  expect(s.skips.get('（没写原因）')).toEqual(['m/ui TestNoReason'])
  expect(s.missing.map((m) => [m.need.split('（')[0], m.tests])).toEqual([
    ['文渊圆体', ['m/linkcard TestTweet']],
    ['Twemoji', ['m/linkcard TestEmoji']],
    ['rg、fd', ['m/tools TestSameAsBun/grep-basic']],
    ['ffmpeg、ffprobe', ['m/media TestVideo']],
  ])
  expect(s.failed.map((f) => f.name)).toEqual(['m/ui TestRed'])
  expect(s.failed[0]!.output.join('')).toContain('编的失败原因')
})

test('跳过原因去掉“文件:行号:”前缀，取最后一句话', () => {
  expect(skipReason(['=== RUN   TestX\n', '    a_test.go:12: 第一句\n', '    b.go:3: 真正的原因\n', '--- SKIP: TestX (0.00s)\n'])).toBe('真正的原因')
  expect(skipReason(['=== RUN   TestX\n', '--- SKIP: TestX (0.00s)\n'])).toBe('（没写原因）')
})

test('每一类缺依赖的说法都认得出（改坏：把某条的说法改掉，这里就红）', () => {
  const said: Record<string, string> = {
    文渊圆体: '本机没有文渊圆体（open : 找不到文件），量不了正式字体',
    Twemoji: '本机没有 Twemoji（native/twemoji/）',
    'rg、fd': '原因：这条用真工具读文件，要 ripgrep（rg）在 PATH 或 vendor/bin 里，这台机器上没有',
    'ffmpeg、ffprobe': '没有 ffmpeg：生成不了真实短片',
    中文字体: '原因：这台机器画不出中文字（公开仓库不带字体文件）',
  }
  for (const m of MUST_HAVE) expect(m.match.test(said[m.need.split('（')[0]!]!)).toBe(true)
})

test('命令行：有缺依赖的跳过就退出码 1 并点名；没有就 0（go test 自己红不红不归它判）', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ci-report-')); dirs.push(dir)
  const bad = join(dir, 'bad.json'), good = join(dir, 'good.json')
  writeFileSync(bad, sample)
  writeFileSync(good, [...run('m/ui', 'TestShot', 'skip', ['    shot_test.go:10: 设 CHATUI_SHOT 才出图']), ...run('m/ui', 'TestRed', 'fail')].join('\n'))
  const saved = join(dir, '存一份.txt')
  const r1 = Bun.spawnSync([process.execPath, join(import.meta.dir, 'ci-test-report.ts'), bad, saved], { stdout: 'pipe', stderr: 'pipe' })
  expect(r1.exitCode).toBe(1)
  expect(r1.stderr.toString()).toContain('::error::缺 文渊圆体')
  expect(r1.stdout.toString()).toContain('FAIL\tm/ui')
  // 第二个参数：打出来的内容原样再存一份（nightly.yml 传成附件），错误行和包结论都在
  const text = readFileSync(saved, 'utf8')
  expect(text).toContain('FAIL\tm/ui')
  expect(text).toContain('::error::缺 文渊圆体')
  expect(text).toContain('编的失败原因')
  const r2 = Bun.spawnSync([process.execPath, join(import.meta.dir, 'ci-test-report.ts'), good], { stdout: 'pipe', stderr: 'pipe' })
  expect(r2.exitCode).toBe(0)
  expect(r2.stdout.toString()).toContain('跳过了 1 条')
})

test('ci-assets：摘要对得上才交回内容，对不上报错', async () => {
  const body = new TextEncoder().encode('编的字体内容')
  const fake = (async () => new Response(body)) as unknown as typeof fetch
  expect(await download('https://编的/字体.ttf', sha256(body), fake)).toEqual(body)
  await expect(download('https://编的/字体.ttf', '0'.repeat(64), fake)).rejects.toThrow('摘要对不上')
  const notFound = (async () => new Response('', { status: 404 })) as unknown as typeof fetch
  await expect(download('https://编的/字体.ttf', sha256(body), notFound)).rejects.toThrow('下载失败：404')
})
