package service

import (
	"sync"
	"testing"
)

func TestAccessCacheConcurrent(t *testing.T) {
	cache := NewAccessCache()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		userID := int64(i % 10)
		go func() {
			defer wg.Done()
			cache.Put(userID, &AccessibleOwners{UserID: userID, OrgIDs: []int64{userID}})
		}()
		go func() {
			defer wg.Done()
			_, _ = cache.Get(userID)
		}()
	}
	wg.Wait()
}
