package retention

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type Verification struct {
	OK            bool             `json:"ok"`
	Segments      int              `json:"segments"`
	Rows          int64            `json:"rows"`
	Anchor        model.AuditHead  `json:"anchor"`
	Chain         model.AuditCheck `json:"chain"`
	BrokenSegment int64            `json:"broken_segment"`
	Error         string           `json:"error,omitempty"`
}

func (s *Service) Verify(ctx context.Context) (*Verification, error) {
	return Verify(ctx, s.st, s.dir)
}

func Verify(ctx context.Context, st store.Store, dir string) (*Verification, error) {
	segs, err := listSegments(ctx, st)
	if err != nil {
		return nil, err
	}
	v := &Verification{OK: true}
	var anchor model.AuditHead
	for _, seg := range segs {
		if seg.Status == StatusRestored {
			continue
		}
		if msg := verifySegment(dir, seg, &anchor); msg != "" {
			v.OK, v.BrokenSegment, v.Error = false, seg.ID, msg
			return v, nil
		}
		v.Segments++
		v.Rows += seg.Rows
	}
	v.Anchor = anchor
	err = st.View(ctx, func(tx store.Tx) (e error) { v.Chain, e = tx.VerifyAudit(); return })
	if err != nil {
		return nil, err
	}
	if !v.Chain.OK {
		v.OK, v.Error = false, fmt.Sprintf("audit chain broken at id %d", v.Chain.BrokenID)
	}
	return v, nil
}

func verifySegment(dir string, seg Segment, anchor *model.AuditHead) string {
	rows, err := readSegment(dir, seg)
	if err != nil {
		return err.Error()
	}
	if seg.Kind == Transcripts && seg.Status == StatusArchived {
		return verifyFiles(dir, rows)
	}
	if seg.Kind != Audit {
		return ""
	}
	if seg.PrevHash != anchor.Hash {
		return fmt.Sprintf("audit segment %d does not continue the previous segment", seg.Seq)
	}
	c := audit.Chain{HeadID: anchor.ID, HeadHash: anchor.Hash}
	for _, r := range rows {
		if !c.Add(record(r), r.str("hash")) {
			return fmt.Sprintf("audit chain broken at id %d in segment %d", r.int("id"), seg.Seq)
		}
	}
	if len(rows) == 0 || rows[0].int("id") != seg.FirstID || c.HeadID != seg.LastID || c.HeadHash != seg.LastHash {
		return fmt.Sprintf("audit segment %d does not match its manifest", seg.Seq)
	}
	*anchor = model.AuditHead{ID: c.HeadID, Hash: c.HeadHash}
	return ""
}
