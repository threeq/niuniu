package service

// Test-only helpers for ClaudeUsageService. Defined in a _test.go file so the
// production binary doesn't carry them — they are reachable only from other
// _test.go files in package service (currently claude_usage_test.go and
// claude_account_test.go).

// PrimeForTest pre-creates the cache+rlObs entries for an account ID so a
// test can verify EvictAccount actually purges them.
func (s *ClaudeUsageService) PrimeForTest(accountID int64) {
	_ = s.cacheFor(accountID)
	_ = s.rlBagFor(accountID)
}

// HasCacheForTest reports whether a cache entry exists for the account.
func (s *ClaudeUsageService) HasCacheForTest(accountID int64) bool {
	_, ok := s.caches.Load(accountID)
	return ok
}
