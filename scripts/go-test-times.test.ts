import { expect, test } from 'bun:test'
import { collect, overBudget } from './go-test-times'

test('只算顶层测试和包，超预算的挑出来', () => {
  const lines = [
    '{"Action":"run","Package":"p","Test":"TestA"}',
    '{"Action":"pass","Package":"p","Test":"TestA/子","Elapsed":9}',
    '{"Action":"pass","Package":"p","Test":"TestA","Elapsed":4}',
    '{"Action":"pass","Package":"p","Test":"TestB","Elapsed":1}',
    '{"Action":"fail","Package":"q","Elapsed":70}',
    '{"Action":"pass","Package":"p","Elapsed":5}',
    '不是 JSON',
  ]
  const t = collect(lines)
  expect(t.tests.map((x) => x.name)).toEqual(['TestA', 'TestB'])
  expect(t.packages[0]).toEqual({ name: 'q', seconds: 70, failed: true })
  const over = overBudget(t)
  expect(over.packages.map((p) => p.name)).toEqual(['q'])
  expect(over.tests.map((x) => x.name)).toEqual(['TestA'])
})
