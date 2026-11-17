// 在内存里读 .tgz：gunzip 后按 tar 格式切出普通文件，不写磁盘、不执行里面的任何东西。
// 只认普通文件（type 0），路径里有 .. 的丢掉；调用方自己再按名字和内容过一遍白名单。
// gen-mingcute.ts（npm 包）和 update-twemoji.ts（GitHub 发布）用。
import { gunzipSync } from 'node:zlib'

export function readTgz(tgz: Uint8Array): Map<string, Uint8Array> {
  const tar = gunzipSync(tgz)
  const files = new Map<string, Uint8Array>()
  const dec = new TextDecoder()
  let off = 0
  let longName = ''
  while (off + 512 <= tar.length) {
    const h = tar.subarray(off, off + 512)
    if (h.every((b) => b === 0)) break
    const field = (a: number, n: number) => dec.decode(h.subarray(a, a + n)).replace(/\0.*$/s, '')
    const name = field(0, 100)
    const prefix = field(345, 155)
    const size = parseInt(field(124, 12).trim() || '0', 8)
    const type = field(156, 1) || '0'
    off += 512
    const body = tar.subarray(off, off + size)
    off += Math.ceil(size / 512) * 512
    if (type === 'L') { // GNU 长文件名：下一个条目用这里给的名字
      longName = dec.decode(body).replace(/\0.*$/s, '')
      continue
    }
    if (type === 'x' || type === 'g') continue // pax 头（GitHub 的压缩包开头有一个全局的）
    const full = longName || (prefix ? `${prefix}/${name}` : name)
    longName = ''
    if ((type === '0' || type === '') && !full.split('/').includes('..') && !full.startsWith('/')) files.set(full, body.slice())
  }
  return files
}
