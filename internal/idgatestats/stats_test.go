package idgatestats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecorder_RecordBlockedAndClear(t *testing.T) {
	r := NewRecorder()
	r.RecordBlocked("gemini", "AIza…abc", "US_DRIVER_LICENSE", 0.9, 0, 2, time.Millisecond)
	r.RecordClear("gemini", "AIza…abc", 1, 2*time.Millisecond)

	snap := r.Snapshot()
	require.True(t, snap["available"].(bool))
	require.Equal(t, int64(2), snap["requests_with_images"])
	require.Equal(t, int64(1), snap["requests_blocked"])
	require.Equal(t, int64(1), snap["requests_cleared"])
	require.Equal(t, int64(3), snap["images_scanned"])

	recent := snap["recent"].([]recentEntry)
	require.Len(t, recent, 2)
	require.Equal(t, "clear", recent[0].Outcome)
	require.Equal(t, "blocked", recent[1].Outcome)
	require.Equal(t, "id_gate", recent[0].Pipeline)
}

func TestRecorderSnapshotStaleDayWithoutTraffic(t *testing.T) {
	r := NewRecorder()
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	r.dayKey = yesterday
	r.requestsWithImages = 99

	snap := r.Snapshot()
	require.Equal(t, today, snap["day"])
	require.Equal(t, int64(0), snap["requests_with_images"])
}
