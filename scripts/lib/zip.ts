// 解 zip（ADR-078）：用内置的 zip.js，不起外部程序（这台电脑上直接起外部程序，头几次要卡 5～12 秒，2026-10-18 选型体检）。
// 下载 rg、fd 的脚本用它（2026-11-01 分仓时从 Bun 后台的 daemon/util/zip.ts 拷来）。包是外面拿来的：路径跳出解包文件夹（..、绝对路径、盘符）的一律拒绝
import { mkdirSync } from 'node:fs'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import { BlobReader, ZipReader, configure } from '@zip.js/zip.js'

configure({ useWebWorkers: false })   // 在 Bun 里跑，压缩用内置的 CompressionStream，不开 Web Worker

/** 把 zip 解到 into 下，一个个文件流式写出（不整个读进内存）；返回解出来的文件数 */
export async function unzipTo(zip: string, into: string) {
  const r = new ZipReader(new BlobReader(Bun.file(zip)))
  let n = 0
  try {
    // zip.js 读目录时自己也会拦不安全的文件名（英文报错），换成给主人看的话
    const entries = await r.getEntries().catch((e: Error) => { throw /unsafe/i.test(e.message) ? new Error('包里有不安全的路径，不解开') : e })
    for (const e of entries) {
      const out = resolve(into, e.filename)
      if (isAbsolute(e.filename) || /^[a-zA-Z]:/.test(e.filename) || relative(resolve(into), out).startsWith('..')) throw new Error(`包里有不安全的路径：${e.filename}`)
      if (e.directory) { mkdirSync(out, { recursive: true }); continue }
      mkdirSync(dirname(out), { recursive: true })
      const sink = Bun.file(out).writer({ highWaterMark: 1 << 20 })
      await e.getData!(new WritableStream<Uint8Array>({ write: (c) => { sink.write(c) }, close: async () => { await sink.end() } }))
      n++
    }
  } finally { await r.close() }
  return n
}
