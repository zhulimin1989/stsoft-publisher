package outbox

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestOrderedClaimedBatchesUsesEarliestPersistedRowID(t *testing.T) {
	olderBatch := uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	newerBatch := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	rows := []OutboxRow{
		// ClaimDue/UPDATE RETURNING may interleave batches and rows. UUID order and
		// input order intentionally put the newer batch first.
		{ID: 21, BatchID: newerBatch, Seq: 1},
		{ID: 10, BatchID: olderBatch, Seq: 3},
		{ID: 22, BatchID: newerBatch, Seq: 0},
		{ID: 11, BatchID: olderBatch, Seq: 2},
	}
	wantCallerRows := append([]OutboxRow(nil), rows...)

	groups := orderedClaimedBatches(rows)
	if len(groups) != 2 {
		t.Fatalf("group count=%d want 2", len(groups))
	}
	if groups[0].batchID != olderBatch || groups[1].batchID != newerBatch {
		t.Fatalf("batch order=[%s %s], want oldest persisted row first [%s %s]",
			groups[0].batchID, groups[1].batchID, olderBatch, newerBatch)
	}
	if got := []int64{groups[0].rows[0].ID, groups[0].rows[1].ID}; !reflect.DeepEqual(got, []int64{11, 10}) {
		t.Fatalf("older batch row order=%v want Seq-first IDs [11 10]", got)
	}
	if got := []int64{groups[1].rows[0].ID, groups[1].rows[1].ID}; !reflect.DeepEqual(got, []int64{22, 21}) {
		t.Fatalf("newer batch row order=%v want Seq-first IDs [22 21]", got)
	}
	if !reflect.DeepEqual(rows, wantCallerRows) {
		t.Fatalf("orderedClaimedBatches mutated caller rows:\n got=%+v\nwant=%+v", rows, wantCallerRows)
	}
}
