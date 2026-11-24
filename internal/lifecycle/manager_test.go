package lifecycle

import (
	"sync"
	"testing"
	"time"
)

func TestWriteWaitsForReadersAndBlocksNewReaders(t *testing.T) {
	resource := Source(42)
	releaseFirst := Read(resource)
	writerAcquired := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		release := Write(resource)
		close(writerAcquired)
		<-releaseWriter
		release()
		close(writerDone)
	}()

	select {
	case <-writerAcquired:
		t.Fatal("writer acquired while reader was active")
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirst()
	select {
	case <-writerAcquired:
	case <-time.After(time.Second):
		t.Fatal("writer did not acquire after reader released")
	}

	secondReaderAcquired := make(chan struct{})
	secondReaderDone := make(chan struct{})
	go func() {
		release := Read(resource)
		close(secondReaderAcquired)
		release()
		close(secondReaderDone)
	}()
	select {
	case <-secondReaderAcquired:
		t.Fatal("reader acquired while writer was active")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseWriter)
	select {
	case <-secondReaderAcquired:
	case <-time.After(time.Second):
		t.Fatal("reader did not acquire after writer released")
	}
	// Both goroutines must finish releasing before the next test inspects the
	// shared registry; acquiring a lease and cleaning up the registry entry are
	// not the same instant.
	<-writerDone
	<-secondReaderDone
}

func TestMultipleKeysUseStableOrderAndRegistryIsReleased(t *testing.T) {
	keys := []Key{User(9), Source(3), Source(1), User(9)}
	var wait sync.WaitGroup
	wait.Add(2)
	for _, ordered := range [][]Key{keys, {Source(1), Source(3), User(9)}} {
		go func(items []Key) {
			defer wait.Done()
			release := Write(items...)
			release()
		}(ordered)
	}
	wait.Wait()

	defaultManager.mu.Lock()
	defer defaultManager.mu.Unlock()
	if len(defaultManager.entries) != 0 {
		t.Fatalf("released locks remained in registry: %d", len(defaultManager.entries))
	}
}

func TestIndependentManagersAreIsolated(t *testing.T) {
	m1 := NewManager()
	m2 := NewManager()

	releaseWrite := m1.Write(Source(7))
	defer releaseWrite()

	acquired := make(chan struct{})
	go func() {
		releaseRead := m2.Read(Source(7))
		releaseRead()
		close(acquired)
	}()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("m2.Read blocked on m1.Write despite isolated Manager instances")
	}
}
