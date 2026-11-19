// jsjson 的对拍向量（规格-Go后台 四点七 2b）：Bun 的 JSON.stringify(JSON.parse(输入)) 对一批输入的结果，
// Go 版（native/internal/jsjson）跑同一批比。输入 = 手挑的边界 + 固定种子随机拼的几千条（键的先后、像下标的键、重复的键、
// 各种数字、要转义的字、中文和 emoji）。改了这块或者升级了 Bun 以后重跑：bun scripts/go-golden-json.ts
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const HAND = [
  '{}', '[]', 'null', 'true', '0', '-0', '1.0', '1e21', '1e-7', '0.000001', '9'.repeat(24), '0.1', '1.5e300', '-2.5E-3',
  '{"b":1,"a":2}', '{"10":1,"2":2,"a":3,"1":4}', '{"01":1,"1":2}', `{"${2 ** 32 - 2}":1,"${2 ** 32 - 1}":2,"a":3}`, '{"a":1,"a":2,"b":3}',   // 前一个：数组下标的上限（2^32 - 2）和刚超过的
  '{"s":"\\b\\f\\n\\r\\t\\u0001\\u001f\\"\\\\/"}', '{"s":"\\u2028\\u2029"}', '{"s":"中文😀"}', '{"s":"\\ud83d\\ude00"}', '{"path":"C:\\\\Users\\\\x"}',
  '  { "a" : [ 1 , 2 , { "c" : null } ] }  ', '{"n":[1e2,1E+2,100.000,0.5e1]}',
]
let seed = 2025
const rand = () => { seed |= 0; seed = (seed + 0x6d2b79f5) | 0; let t = Math.imul(seed ^ (seed >>> 15), 1 | seed); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 2 ** 32 }
const pick = <T>(xs: T[]) => xs[Math.floor(rand() * xs.length)]
const KEYS = ['a', 'b', 'path', '0', '1', '10', '2', '007', 'x y', '中', '', 'limit', 'offset']
const STRS = ['', 'note.txt', '中文', 'a"b', 'back\\slash', 'line\nbreak', 'tab\there', '😀', '\u0007', '\u2028']
const NUMS = [0, 1, -1, 0.5, 1e21, 1e-7, 123.456, 2 ** 53, -0.0001, 1e100]
function gen(depth: number): unknown {
  const r = rand()
  if (depth > 2 || r < 0.35) return pick<unknown>([null, true, false, pick(NUMS), pick(STRS)])
  if (r < 0.55) return Array.from({ length: Math.floor(rand() * 4) }, () => gen(depth + 1))
  const o: Record<string, unknown> = {}
  for (let i = Math.floor(rand() * 5); i > 0; i--) o[pick(KEYS)] = gen(depth + 1)
  return o
}
// 随机的：先 stringify 成文本（键的先后按 JS 的规则），再拼一个"键的先后乱着写、带空白"的版本当输入
function shuffled(v: unknown): string {
  if (Array.isArray(v)) return `[ ${v.map(shuffled).join(' , ')} ]`
  if (v && typeof v === 'object') {
    const es = Object.entries(v)
    for (let i = es.length - 1; i > 0; i--) { const j = Math.floor(rand() * (i + 1)); [es[i], es[j]] = [es[j], es[i]] }
    return `{ ${es.map(([k, x]) => `${JSON.stringify(k)} : ${shuffled(x)}`).join(' , ')} }`
  }
  return JSON.stringify(v)
}
const random = Array.from({ length: 3000 }, () => shuffled(gen(0)))
const vectors = [...HAND, ...random].map((input) => ({ input, output: JSON.stringify(JSON.parse(input)) }))
const dir = join(import.meta.dir, '..', 'native', 'internal', 'jsjson', 'testdata')
mkdirSync(dir, { recursive: true })
writeFileSync(join(dir, 'vectors.json'), JSON.stringify({ bun: Bun.version, vectors }, null, 1) + '\n')
console.log(`${vectors.length} 条（手挑 ${HAND.length}、随机 ${random.length}）`)
