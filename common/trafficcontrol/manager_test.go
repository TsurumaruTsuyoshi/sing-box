package trafficcontrol

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"

	"github.com/sagernet/sing-box/adapter"
)

type testTracker struct {
	metadata TrackerMetadata
}

func (t *testTracker) Metadata() *TrackerMetadata {
	return &t.metadata
}

func (*testTracker) Close() error {
	return nil
}

func TestLeaveSnapshotsCounters(t *testing.T) {
	manager := NewManager(nil)
	require.NoError(t, manager.Start(adapter.StartStateInitialize))
	defer manager.Close()

	subscription, done, err := manager.SubscribeEvents()
	require.NoError(t, err)
	defer manager.UnSubscribeEvents(subscription)

	id, err := uuid.NewV4()
	require.NoError(t, err)
	upload := new(atomic.Int64)
	download := new(atomic.Int64)
	upload.Store(123)
	download.Store(456)
	tracker := &testTracker{metadata: TrackerMetadata{
		ID:        id,
		CreatedAt: time.Now().Add(-time.Second),
		Upload:    upload,
		Download:  download,
	}}

	manager.join(tracker)
	manager.leave(tracker)
	upload.Store(789)
	download.Store(987)

	var closedEvent ConnectionEvent
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for closedEvent.Metadata == nil {
		select {
		case event := <-subscription:
			if event.Type == ConnectionEventClosed {
				closedEvent = event
			}
		case <-done:
			t.Fatal("traffic event subscription closed before the close event")
		case <-timer.C:
			t.Fatal("timed out waiting for the close event")
		}
	}

	require.Equal(t, int64(123), closedEvent.Metadata.Upload.Load())
	require.Equal(t, int64(456), closedEvent.Metadata.Download.Load())
	require.NotSame(t, upload, closedEvent.Metadata.Upload)
	require.NotSame(t, download, closedEvent.Metadata.Download)

	closedConnections := manager.ClosedConnections()
	require.Len(t, closedConnections, 1)
	require.Equal(t, int64(123), closedConnections[0].Upload.Load())
	require.Equal(t, int64(456), closedConnections[0].Download.Load())
	require.NotSame(t, upload, closedConnections[0].Upload)
	require.NotSame(t, download, closedConnections[0].Download)

	uplinkTotal, downlinkTotal := manager.Total()
	require.Equal(t, int64(123), uplinkTotal)
	require.Equal(t, int64(456), downlinkTotal)
}
