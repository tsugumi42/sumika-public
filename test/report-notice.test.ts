import { afterEach, expect, test } from 'bun:test'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { notice, sectionOf } from '../scripts/report-notice'
import { tmpData, cleanTmp } from './helpers/tmp'

const temp = () => { const d = tmpData('report-notice-'); return d }
afterEach(() => { cleanTmp() })

const NOW = new Date('2026-10-19T09:00:00')
const REPORT = '# 复盘 2026-10-18\n\n今天 81 个提交，14 个问题。\n\n## 返工\n\n长长的细节\n\n## 给主会话\n\n1. qq.md:238 还写着"还没接上"\n2. ADR-025 标题没注明\n\n## 正例\n\n不该打出来\n'

test('只打"给主会话"一节，不带别的节', () => {
  const s = sectionOf(REPORT)
  expect(s).toBe('1. qq.md:238 还写着"还没接上"\n2. ADR-025 标题没注明')
})

test('"给主会话"是最后一节时取到文件末尾', () => {
  expect(sectionOf('# x\n\n## 给主会话\n\n- 一条\n- 两条\n')).toBe('- 一条\n- 两条')
})

test('没有那一节、或者写"没有"，就是不用提醒', () => {
  expect(sectionOf('# 巡检\n\n全过，没有异常。\n\n## 细节\n\n…')).toBe('')
  expect(sectionOf('# 巡检\n\n## 给主会话\n\n没有。\n')).toBe('')
  expect(sectionOf('# 巡检\n\n## 给主会话\n\n（无）\n')).toBe('')
  expect(sectionOf('# 巡检\n\n## 给主会话\n\n没有过的：fit 一项\n')).toBe('没有过的：fit 一项')
})

test('全过的巡检不提醒，也不会在之后反复看', () => {
  const d = temp()
  writeFileSync(join(d, '巡检-2026-10-19.md'), '# 巡检\n\n全部正常。\n\n## 给主会话\n\n没有\n')
  expect(notice(d, 'main', '你好', NOW)).toBe('')
  expect(JSON.parse(readFileSync(join(d, '.已提醒.json'), 'utf8')).main.files).toEqual(['巡检-2026-10-19.md'])
})

test('同一个会话每份报告只提醒一次；别的会话照样提醒', () => {
  const d = temp()
  writeFileSync(join(d, '复盘-2026-10-18.md'), REPORT)
  const first = notice(d, 'main', '帮我看看这个', NOW)
  expect(first).toContain('复盘-2026-10-18.md')
  expect(first).toContain('ADR-025 标题没注明')
  expect(first).not.toContain('不该打出来')
  expect(notice(d, 'main', '下一条', NOW)).toBe('')
  expect(notice(d, 'other', '你好', NOW)).toContain('复盘-2026-10-18.md')
  // 新报告来了，只提醒新的那份
  writeFileSync(join(d, '巡检-2026-10-19.md'), '# 巡检\n\n## 给主会话\n\n- check:ui 有 1 项没过\n')
  const next = notice(d, 'main', '继续', NOW)
  expect(next).toContain('巡检-2026-10-19.md')
  expect(next).not.toContain('复盘-2026-10-18.md')
})

test('不提醒：两天前的报告、晨报这类别的文件、定时任务自己的会话', () => {
  const d = temp()
  writeFileSync(join(d, '复盘-2026-10-17.md'), REPORT)
  writeFileSync(join(d, '晨报-2026-10-19.md'), REPORT)
  expect(notice(d, 'main', '你好', NOW)).toBe('')
  writeFileSync(join(d, '复盘-2026-10-18.md'), REPORT)
  expect(notice(d, 'sched', '<scheduled-task name="sumika-daily-review">…', NOW)).toBe('')
  expect(notice(d, 'main', '你好', NOW)).toContain('复盘-2026-10-18.md')
})

test('记录写坏了从空的开始；超过 7 天的会话从记录里删掉', () => {
  const d = temp()
  writeFileSync(join(d, '复盘-2026-10-18.md'), REPORT)
  writeFileSync(join(d, '.已提醒.json'), '{坏的')
  expect(notice(d, 'main', '你好', NOW)).toContain('复盘-2026-10-18.md')
  const seen = JSON.parse(readFileSync(join(d, '.已提醒.json'), 'utf8'))
  seen.old = { files: [], at: '2026-10-01T00:00:00.000Z' }
  writeFileSync(join(d, '.已提醒.json'), JSON.stringify(seen))
  writeFileSync(join(d, '巡检-2026-10-19.md'), '# 巡检\n\n## 给主会话\n\n- 一条\n')
  expect(notice(d, 'main', '再来', NOW)).toContain('巡检-2026-10-19.md')
  expect(Object.keys(JSON.parse(readFileSync(join(d, '.已提醒.json'), 'utf8')))).toEqual(['main'])
})

test('数据目录不存在、没有会话号时什么都不做', () => {
  expect(notice(join(temp(), '没有'), 'main', '你好', NOW)).toBe('')
  const d = temp()
  writeFileSync(join(d, '复盘-2026-10-18.md'), REPORT)
  expect(notice(d, '', '你好', NOW)).toBe('')
})

test('前一晚的早报也会带出来，新会话一样', () => {
  const d = temp()
  writeFileSync(join(d, '早报-2026-10-19.md'), '# 早报\n\n## 给主会话\n\n- 先把这份早报给主人看，逐条验收\n')
  const out = notice(d, 'fresh-session', '早', NOW)
  expect(out).toContain('早报-2026-10-19.md')
  expect(out).toContain('逐条验收')
})

