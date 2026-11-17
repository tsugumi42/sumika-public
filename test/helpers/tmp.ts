// 临时数据目录（ADR-122 第 5 条）：测试和检查脚本只用临时目录，绝不碰真实的 data/（AGENTS.md 验收）。
// tmpData 建在系统临时目录下并记下来；cleanTmp 把记下的都删掉（测试里 afterEach(cleanTmp)）；
// 需要先关库再删的，用 rmDir 删单个。
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const made = new Set<string>()

/**
 * 建一个临时目录，prefix 是目录名前缀（带上用途，出问题时好认）。
 * track 默认 true：记下来，等 cleanTmp 一起删。cleanTmp 是整个进程共用的清单（Bun 把各测试文件按顺序跑在一个进程里），
 * 所以在文件最顶层建、要用到整个文件跑完的目录传 false，自己用 rmDir 在 afterAll 里删，别被前面文件的 cleanTmp 误删。
 */
export function tmpData(prefix = 'sumika-test-', track = true): string {
  const dir = mkdtempSync(join(tmpdir(), prefix))
  if (track) made.add(dir)
  return dir
}

/** 删一个目录（连里面的）；Windows 上刚关的库文件可能还被占着，多试几次 */
export function rmDir(dir: string): void {
  rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
  made.delete(dir)
}

/** 删掉 tmpData 建过、还没删的所有目录 */
export function cleanTmp(): void {
  for (const d of [...made]) rmDir(d)
}
