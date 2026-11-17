import { expect, test } from 'bun:test'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

// fetch-ort.ts 的验收只含干跑（规格-Go本地模型 GM2-8）：默认不联网、不写任何文件；真下载要主人明说，不在测试里跑。
const root = resolve(import.meta.dir, '..')
const run = (...args: string[]) => Bun.spawnSync([process.execPath, join(root, 'scripts', 'fetch-ort.ts'), ...args], { cwd: root })

test('不带参数是干跑：打印地址和期望的哈希，退出码 0，不产生 vendor/ort', () => {
  const had = existsSync(join(root, 'vendor', 'ort'))
  const p = run()
  const out = p.stdout.toString()
  expect(p.exitCode).toBe(0)
  expect(out).toContain('https://github.com/microsoft/onnxruntime/releases/download/v1.24.4/onnxruntime-win-x64-1.24.4.zip')
  expect(out).toContain('d2319fddfb6ea4db99ccc4b60c85c517bcd855721f5daa6a06d40d7cb2ee2357')
  expect(out).toContain('干跑：没有联网，没有写任何文件')
  expect(out).not.toContain('下载 https')
  expect(existsSync(join(root, 'vendor', 'ort'))).toBe(had)
})

test('不认识的参数直接退出，不当成 --yes', () => {
  const p = run('--download')
  expect(p.exitCode).toBe(2)
  expect(p.stdout.toString()).not.toContain('下载 https')
})

// 目标位置已经有文件时要和钉住的哈希比（复核发现原来只看"在不在"）。把脚本拷进临时目录当仓库根，造一个假 dll，不联网。
// 走到"已存在"这一步前有平台检查，所以只在钉住的平台上跑。
test.skipIf(process.platform !== 'win32' || process.arch !== 'x64')('目标 dll 已存在但哈希不对：退出码 1，提示删掉重下，不覆盖也不联网', () => {
  const fake = mkdtempSync(join(tmpdir(), 'fetch-ort-'))
  try {
    mkdirSync(join(fake, 'scripts'), { recursive: true })
    copyFileSync(join(root, 'scripts', 'fetch-ort.ts'), join(fake, 'scripts', 'fetch-ort.ts'))
    const dir = join(fake, 'vendor', 'ort', '1.24.4')
    mkdirSync(dir, { recursive: true })
    const dll = join(dir, 'onnxruntime.dll')
    writeFileSync(dll, 'not the pinned dll')
    const p = Bun.spawnSync([process.execPath, join(fake, 'scripts', 'fetch-ort.ts'), '--yes'], { cwd: fake })
    expect(p.exitCode).toBe(1)
    expect(p.stderr.toString()).toContain('和钉住的不一样')
    expect(p.stderr.toString()).toContain('重新下载')
    expect(p.stdout.toString()).not.toContain('下载 https')
    expect(readFileSync(dll, 'utf8')).toBe('not the pinned dll')
  } finally {
    rmSync(fake, { recursive: true, force: true })
  }
})
