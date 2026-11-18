// check-ui-skill.ts 的验收：全是编的小样本，不读真仓库。
import { expect, test } from 'bun:test'
import { checkUiSkill, type CheckDeps } from './check-ui-skill'

const goMod = 'module sumika-native\n\nrequire (\n\tgithub.com/egoist/mygo v0.9.0\n)\n'

const deps = (over: Partial<CheckDeps> = {}): CheckDeps => ({
  pathExists: (p) => p === 'native/internal/winshell',
  hasName: (pkg, name) => pkg === 'winshell' && name === 'Kit',
  ...over,
})

test('超过 150 行报错', () => {
  const skill = 'mygo v0.9.0\n' + 'x\n'.repeat(150)
  const r = checkUiSkill(skill, goMod, deps())
  expect(r.errors.some((e) => e.includes('150'))).toBe(true)
})

test('internal 路径没有对应目录报错，带行号', () => {
  const r = checkUiSkill('看 `internal/nope`。\n\nmygo v0.9.0\n', goMod, deps())
  expect(r.errors).toHaveLength(1)
  expect(r.errors[0]).toContain('第 1 行')
  expect(r.errors[0]).toContain('internal/nope')
})

test('包里有名字找不到报错', () => {
  const r = checkUiSkill('用 `winshell.Missing`。\n\nmygo v0.9.0\n', goMod, deps())
  expect(r.errors).toHaveLength(1)
  expect(r.errors[0]).toContain('第 1 行')
  expect(r.errors[0]).toContain('winshell.Missing')
  expect(r.errors[0]).toContain('找不到')
})

test('版本和 go.mod 不一致报错', () => {
  const r = checkUiSkill('`winshell.Kit`\n\nmygo v0.1.0\n', goMod, deps())
  expect(r.errors.some((e) => e.includes('v0.1.0') && e.includes('v0.9.0'))).toBe(true)
})

test('一处都没写版本报错', () => {
  const r = checkUiSkill('`winshell.Kit`\n', goMod, deps())
  expect(r.errors.some((e) => e.includes('没写版本'))).toBe(true)
})

test('全部正确时没有错误，并数出包和名字', () => {
  const r = checkUiSkill('看 `internal/winshell`，用 `winshell.Kit`。\n\n版本 mygo v0.9.0。\n', goMod, deps())
  expect(r.errors).toEqual([])
  expect(r.packages).toBe(1)
  expect(r.names).toBe(1)
  expect(r.skipped).toEqual([])
})

test('不是本仓库的包跳过，不报错', () => {
  const r = checkUiSkill('用 `ui.NewTester`。\n\nmygo v0.9.0\n', goMod, deps())
  expect(r.errors).toEqual([])
  expect(r.skipped).toContain('ui.NewTester')
})
