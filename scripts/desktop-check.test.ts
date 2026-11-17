import { afterAll, expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { hutchPath, runDesktopCheck } from './desktop-check'

const dirs: string[] = []
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })
const tmp = () => { const d = mkdtempSync(join(tmpdir(), 'sumika-desktop-check-')); dirs.push(d); return d }

test('hutch 先找 HUTCH_HOME/bin，没有就看 PATH，都没有是空', () => {
  const home = tmp()
  mkdirSync(join(home, 'bin'))
  writeFileSync(join(home, 'bin', 'hutch.exe'), '')
  expect(hutchPath({ HUTCH_HOME: home, PATH: '' })).toBe(join(home, 'bin', 'hutch.exe'))
  expect(hutchPath({ HUTCH_HOME: tmp(), PATH: '' })).toBe('')
})

test('没有 desktop/：跳过、不红，结论写明原因', () => {
  const root = tmp()
  const s = runDesktopCheck(root, process.env)
  expect(s.ok).toBe(true)
  expect(s.summary).toBe('desktop 检查：没有 desktop/，跳过')
})

test('有 desktop/、但它引的 server/ 不在、装不上：红，不往下跑', () => {
  const root = tmp()
  mkdirSync(join(root, 'desktop'))
  writeFileSync(join(root, 'desktop', 'package.json'), '{"name":"x"}')
  const s = runDesktopCheck(root, process.env) // 没有 server/
  expect(s.ok).toBe(false)
  expect(s.summary).toContain('server/ 装依赖失败')
})
