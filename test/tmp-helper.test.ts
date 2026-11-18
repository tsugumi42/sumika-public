// test/helpers/tmp.ts 自己的行为：测试都靠它建、删临时目录；删不掉会把垃圾留在系统临时目录，建在别处会碰到真实数据
import { expect, test } from 'bun:test'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, join, resolve } from 'node:path'
import { cleanTmp, rmDir, tmpData } from './helpers/tmp'

test('tmpData 建在系统临时目录下，名字带前缀，每次不同', () => {
  const a = tmpData('sumika-helper-'), b = tmpData('sumika-helper-')
  try {
    expect(a).not.toBe(b)
    expect(resolve(dirname(a))).toBe(resolve(tmpdir()))
    expect(basename(a).startsWith('sumika-helper-')).toBe(true)
    expect(existsSync(a)).toBe(true)
  } finally { rmDir(a); rmDir(b) }
})

test('rmDir 连里面的子目录和文件一起删，目录已经没了也不报错', () => {
  const d = tmpData('sumika-helper-')
  mkdirSync(join(d, 'x', 'y'), { recursive: true })
  writeFileSync(join(d, 'x', 'y', 'f.txt'), '1')
  rmDir(d)
  expect(existsSync(d)).toBe(false)
  expect(() => rmDir(d)).not.toThrow()
})

test('tmpData(…, false) 不进清单，cleanTmp 不会删它', () => {
  const kept = tmpData('sumika-helper-', false)
  try {
    cleanTmp()
    expect(existsSync(kept)).toBe(true)
  } finally { rmDir(kept) }
})

test('cleanTmp 删掉所有建过还没删的；删过的不再碰', () => {
  const a = tmpData('sumika-helper-'), b = tmpData('sumika-helper-'), c = tmpData('sumika-helper-')
  writeFileSync(join(a, 'f.txt'), '1')
  rmDir(c)
  cleanTmp()
  expect([a, b, c].map(existsSync)).toEqual([false, false, false])
})

