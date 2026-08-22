package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestUpstreamMultiplierSnapshotKeepsLastValidValue(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer func() { _ = st.Close() }()

	updatedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := st.SaveUpstreamMultiplier(101, 1.25, updatedAt); err != nil {
		t.Fatalf("保存有效倍率失败: %v", err)
	}
	for _, invalid := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if err := st.SaveUpstreamMultiplier(101, invalid, time.Now()); err == nil {
			t.Fatalf("非法倍率 %v 应被拒绝", invalid)
		}
	}

	snapshots, err := st.UpstreamMultipliers()
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snapshot := snapshots[101]; snapshot.Value != 1.25 || !snapshot.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("旧快照被覆盖: %+v", snapshot)
	}

	if err := st.PruneUpstreamMultipliers(map[int64]struct{}{202: {}}); err != nil {
		t.Fatalf("清理快照失败: %v", err)
	}
	snapshots, _ = st.UpstreamMultipliers()
	if len(snapshots) != 0 {
		t.Fatalf("已删除账号的快照未清理: %#v", snapshots)
	}
}
