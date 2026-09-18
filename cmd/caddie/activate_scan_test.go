package main

import "testing"

// TestScanShouldSkipFetch pins the contract activateSyncRepos depends on:
// scan's own remote-ahead probe (a per-repo `git fetch --dry-run`, silent
// unless it finds something) must be skipped whenever the caller already
// trusts the remote state, not only when a pull happened this exact call.
//
// A previous version keyed this solely off "did we pull this call"
// (attempted > 0), which meant every pull-cache *hit* (shouldPull == false,
// attempted == 0) still fell through to a full, silent per-repo network
// probe — confirmed on a real machine: shouldPull=false, zero pull output,
// scan alone still took ~10s across ~16 repos, immediately after the
// (correctly working) hourly pull cache had just avoided the pull itself.
func TestScanShouldSkipFetch(t *testing.T) {
	cases := []struct {
		name       string
		attempted  int
		shouldPull bool
		want       bool
	}{
		{"pull_cache_hit_nothing_attempted", 0, false, true},
		{"pull_cache_miss_but_nothing_to_do", 0, true, false},
		{"pull_attempted_this_call", 3, true, true},
		{"forced_pull_with_repos_attempted", 3, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanShouldSkipFetch(tc.attempted, tc.shouldPull)
			if got != tc.want {
				t.Errorf("scanShouldSkipFetch(attempted=%d, shouldPull=%v) = %v, want %v",
					tc.attempted, tc.shouldPull, got, tc.want)
			}
		})
	}
}
