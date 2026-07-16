package velkarrpc

// mempool.go —— 打款确认追踪所需的节点侧只读能力。
// ⚠ Kaspa 无 txindex：交易一旦被接受入块即离开内存池，无法按 txid 查历史确认深度。
// 故 velkar 的打款确认信号 = 内存池成员关系：「在池 = 尚未入块（0 确认）；离池 = 已被接受入块」。
// 上层 velkarwallet.TxConfirmations 据此 + virtual DAA 增量估算确认深度。

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc/protowire"
)

// getMempoolEntry 查询单个 txid 的内存池条目。节点对未知/已入块 txid 在响应里回 Error
// （非传输错误），调用方据 Entry/Error 判定，不在此当作 Go error。
func (c *client) getMempoolEntry(ctx context.Context, txid string) (*protowire.GetMempoolEntryResponseMessage, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_GetMempoolEntryRequest{
		GetMempoolEntryRequest: &protowire.GetMempoolEntryRequestMessage{TxId: txid},
	}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_GetMempoolEntryResponse)
		return ok
	})
	if err != nil {
		return nil, err
	}
	return msg.Payload.(*protowire.VelkardMessage_GetMempoolEntryResponse).GetMempoolEntryResponse, nil
}

// TxInMempool 报告交易当前是否仍在节点内存池。
// 节点对不在池的 txid 回响应级 Error → 视为不在池（false, nil），仅传输/流错误才返回 error。
func (a *Adapter) TxInMempool(ctx context.Context, txid string) (bool, error) {
	resp, err := a.c.getMempoolEntry(ctx, txid)
	if err != nil {
		return false, err
	}
	if resp.GetError() != nil {
		return false, nil // "not found in mempool" 是正常态
	}
	return resp.GetEntry() != nil, nil
}
