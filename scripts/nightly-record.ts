// 每日整套的结论文件（规格-每日整套 第 2 条）：<git 公共目录>/sumika-nightly.json，最近 30 条。
// nightly.ts 写、merge-queue.ts 读；放一份是为了两边对"哪条算记录"用同一个口径，不各写一个解析器。
import { readFileSync } from 'node:fs'

// ran：从 go-verify 输出里认出的“真的跑了整套”的证据。full=这次跑了整套；reused=同一棵树已经整套验过、这次只认记录。
// reason：没跑成、超时、没认出证据时的原因，给人看，也方便测试和状态排查
export type NightlyRecord = { at: string; commit: string; tree: string; ok: boolean; seconds: number; exit: number; output: string; failed: string[]; ran?: 'full' | 'reused'; reason?: string }
export const KEEP = 30

export function loadNightly(path: string, read: (p: string) => string = (p) => readFileSync(p, 'utf8')): NightlyRecord[] {
  try {
    const v = JSON.parse(read(path))
    return Array.isArray(v)
      ? (v.filter((r) => r && typeof r === 'object' && typeof r.at === 'string' && Number.isFinite(Date.parse(r.at)) && typeof r.commit === 'string' && typeof r.ok === 'boolean') as NightlyRecord[])
      : []
  } catch {
    return []   // 没有或写坏了：当成还没有记录，第一次跑就会补上，不会因此少跑
  }
}
