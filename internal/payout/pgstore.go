package payout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// PGBatchStore Postgres 批次持久化（M4）：payment_batches 一行一批 +
// payments 一行一地址（矿工打款历史/API 自查）。崩溃恢复（docs/05 场景A）
// 的 Unfinished 扫描由此获得跨重启的真持久化。
type PGBatchStore struct {
	h    *sql.DB
	coin string
}

var _ BatchStore = (*PGBatchStore)(nil)

func NewPGBatchStore(h *sql.DB, coin string) *PGBatchStore {
	return &PGBatchStore{h: h, coin: coin}
}

func (s *PGBatchStore) NextBatchID() (int64, error) {
	var id int64
	err := s.h.QueryRow(`SELECT nextval('payment_batches_id_seq')`).Scan(&id)
	return id, err
}

// Save 批次 UPSERT + payments 行同步（同一事务：状态永远一致）。
func (s *PGBatchStore) Save(b *Batch) error {
	ctx := context.Background()
	tx, err := s.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	sum, err := sumOutputs(b.Outputs)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO payment_batches (id, poolid, kind, status, plannedtxid, rawtx, txid, total, created)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9)
		ON CONFLICT (id) DO UPDATE SET
		  status=EXCLUDED.status, plannedtxid=EXCLUDED.plannedtxid, rawtx=EXCLUDED.rawtx,
		  txid=EXCLUDED.txid, updated=now()`,
		b.ID, s.coin, b.Kind, string(b.Status), nullStr(b.PlannedTxID), nullStr(b.RawTx),
		nullStr(b.TxID), sum, batchTime(b)); err != nil {
		return err
	}
	for a, amt := range b.Outputs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO payments (poolid, address, amount, batchid, transactionconfirmationdata, status, created)
			VALUES ($1,$2,$3::numeric,$4,$5,$6,$7)
			ON CONFLICT (poolid, batchid, address) DO UPDATE SET
			  transactionconfirmationdata=EXCLUDED.transactionconfirmationdata,
			  status=EXCLUDED.status, updated=now()`,
			s.coin, a, amt, b.ID, nullStr(b.TxID), string(b.Status), batchTime(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PGBatchStore) Load(id int64) (*Batch, bool, error) {
	b, err := s.loadOne(`WHERE poolid=$1 AND id=$2`, s.coin, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *PGBatchStore) Unfinished() ([]*Batch, error) {
	return s.list(`WHERE poolid=$1 AND status NOT IN ('confirmed','failed') ORDER BY id`, s.coin)
}

func (s *PGBatchStore) All() ([]*Batch, error) {
	return s.list(`WHERE poolid=$1 ORDER BY id DESC`, s.coin)
}

const batchCols = `SELECT id, kind, status, COALESCE(plannedtxid,''), COALESCE(rawtx,''), COALESCE(txid,''), created FROM payment_batches `

func (s *PGBatchStore) loadOne(where string, args ...any) (*Batch, error) {
	row := s.h.QueryRow(batchCols+where, args...)
	return scanBatch(s.h, s.coin, row)
}

func (s *PGBatchStore) list(where string, args ...any) ([]*Batch, error) {
	rows, err := s.h.Query(batchCols+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Batch
	for rows.Next() {
		b, err := scanBatch(s.h, s.coin, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type batchScanner interface{ Scan(dest ...any) error }

func scanBatch(h *sql.DB, coin string, r batchScanner) (*Batch, error) {
	b := &Batch{Outputs: map[string]string{}}
	var status string
	var created time.Time
	if err := r.Scan(&b.ID, &b.Kind, &status, &b.PlannedTxID, &b.RawTx, &b.TxID, &created); err != nil {
		return nil, err
	}
	b.Status = core.PaymentStatus(status)
	b.CreatedAt = created
	rows, err := h.Query(
		`SELECT address, amount::text FROM payments WHERE poolid=$1 AND batchid=$2`, coin, b.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, amt string
		if err := rows.Scan(&a, &amt); err != nil {
			return nil, err
		}
		b.Outputs[a] = amt
	}
	return b, rows.Err()
}

// sumOutputs 合计（十进制字符串加法交给 DB 不放心？这里只做展示列，直接串给 NUMERIC 求和式）。
func sumOutputs(outputs map[string]string) (string, error) {
	if len(outputs) == 0 {
		return "0", nil
	}
	// 交给 Postgres 算会引入一轮 round-trip；纯字符串十进制加法在 Go 侧做小数位对齐即可。
	// 打款金额都来自 Ledger 的 formatAmount（定长小数），这里按最长小数位对齐相加。
	maxFrac := 0
	for _, v := range outputs {
		if i := strings.IndexByte(v, '.'); i >= 0 {
			if f := len(v) - i - 1; f > maxFrac {
				maxFrac = f
			}
		}
	}
	var totalSat int64
	for _, v := range outputs {
		sat, err := parseAmountSat(v, maxFrac)
		if err != nil {
			return "", fmt.Errorf("非法金额 %q: %w", v, err)
		}
		totalSat += sat
	}
	return formatAmountSat(totalSat, maxFrac), nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func batchTime(b *Batch) time.Time {
	if b.CreatedAt.IsZero() {
		return time.Now().UTC()
	}
	return b.CreatedAt.UTC()
}
