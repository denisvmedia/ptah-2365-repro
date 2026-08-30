//go:build !windows

package harnessmin_test

import "sync"

// The address-space churn this models is a windows/amd64 property of the
// affected binary's C allocator; there is nothing to imitate elsewhere, and a
// mmap loop here would only make the arm slower on platforms that do not
// reproduce anyway.
func arenaChurn(stop <-chan struct{}, wg *sync.WaitGroup) { defer wg.Done(); <-stop }
