package ingest_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/testkit"
)

// 离线设备恢复网络后多个进程同时补传同一序号：
// 无论并发度多高，同一 (设备, 序号, 摘要) 只能产生一条 accepted，其余全部 duplicate。
func TestConcurrentBackfillDedup(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	item := testkit.MakeItem(testkit.ItemOpts{
		Seq: 777, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
		Value: "1.20", Unit: "kg", OccurredAt: now.Add(-time.Hour),
	})
	batch := func() ingest.BatchInput {
		return ingest.BatchInput{
			StandardID:   testkit.StandardID,
			DeviceSerial: testkit.ScaleSerial,
			Items:        []json.RawMessage{item},
		}
	}

	const n = 20
	var wg sync.WaitGroup
	results := make([]*ingest.BatchResult, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = w.Ingest.Ingest(testkit.FarmID, batch())
		}(i)
	}
	close(start)
	wg.Wait()

	accepted, duplicated := 0, 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("并发上传 %d 出错: %v", i, errs[i])
		}
		switch results[i].Items[0].Status {
		case domain.RecordAccepted:
			accepted++
		case domain.RecordDuplicate:
			duplicated++
		}
	}
	if accepted != 1 || duplicated != n-1 {
		t.Fatalf("应恰好 1 条接受、%d 条重复，实际 accepted=%d duplicated=%d", n-1, accepted, duplicated)
	}
	// 所有结果（接受 + 重复）都指向同一条读数。
	firstReading := ""
	for _, r := range results {
		id := r.Items[0].ReadingID
		if firstReading == "" {
			firstReading = id
		} else if id != firstReading {
			t.Fatal("重复记录必须指向首次接受的同一条读数")
		}
	}
}
