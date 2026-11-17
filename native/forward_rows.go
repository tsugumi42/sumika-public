package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
	"time"
)

const forwardRowBlocks = 8 // 限制节点内一次构建量，长节点后续块继续虚拟化。

type forwardRow struct {
	node, part int
	blocks     []segview.Block
}

func forwardRows(messages []*msg) []forwardRow {
	var rows []forwardRow
	for node, m := range messages {
		part := 0
		appendRow := func(blocks []segview.Block) {
			rows = append(rows, forwardRow{node: node, part: part, blocks: blocks})
			part++
		}
		for _, group := range shellGroups(m.blocks) {
			var chunk []segview.Block
			flush := func() {
				if len(chunk) > 0 {
					appendRow(chunk)
					chunk = nil
				}
			}
			add := func(b segview.Block) {
				chunk = append(chunk, b)
				if len(chunk) == forwardRowBlocks {
					flush()
				}
			}
			for _, b := range group {
				if b.Kind == segview.Images && len(b.Imgs) > albumLimit {
					for start := 0; start < len(b.Imgs); start += albumLimit {
						imageBlock := b
						imageBlock.Imgs = b.Imgs[start:min(start+albumLimit, len(b.Imgs))]
						add(imageBlock)
					}
				} else {
					add(b)
				}
			}
			flush()
		}
		if part == 0 {
			appendRow(nil)
		}
	}
	return rows
}

func (a *app) forwardRow(c *ui.Context, layer *forwardLayer, row forwardRow) {
	m := layer.msgs[row.node]
	first := row.part == 0 && (row.node == 0 || !sameRun(layer.msgs[row.node-1], m))
	lastPart := a.forward.rowAt+1 == len(layer.rows) || layer.rows[a.forward.rowAt+1].node != row.node
	last := lastPart && (row.node+1 == len(layer.msgs) || !sameRun(m, layer.msgs[row.node+1]))
	if len(row.blocks) == 0 && m.quote == nil {
		ui.Text(c, "（空消息）")
		return
	}
	a.messageRows(c, m, row.blocks, first, last, true, false, time.UnixMilli(m.at), row.part == 0, row.part)
}
