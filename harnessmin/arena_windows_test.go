//go:build windows

package harnessmin_test

// What modernc.org/sqlite contributes to the affected binary, expressed without
// it. Its allocator does not use the Go heap: on windows/amd64 every page comes
// from VirtualAlloc and goes back through VirtualFree, so a process running it
// is continuously reserving and releasing large regions beside the Go heap's own
// reservations. That is the one thing the C translation does that no amount of
// ordinary Go allocation reproduces, and it needs nothing but syscall.
//
// Sizes and the commit/release mix follow modernc.org/memory on windows: 64 KiB
// mapping granularity, whole-allocation MEM_RELEASE (it cannot free a
// sub-range), and a pool that retains some regions rather than returning them
// all.

import (
	"sync"
	"syscall"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procVirtualAll = kernel32.NewProc("VirtualAlloc")
	procVirtualFre = kernel32.NewProc("VirtualFree")
)

const (
	memCommit     = 0x1000
	memReserve    = 0x2000
	memRelease    = 0x8000
	pageReadWrite = 0x04
)

// arenaChurn reserves and releases regions the way the C allocator in the
// affected binary does, until stop closes.
func arenaChurn(stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	const granularity = 64 << 10
	sizes := []uintptr{
		granularity,
		4 * granularity,
		16 * granularity,
		64 * granularity, // 4 MiB, the hot window the pool keeps resident
	}

	// A retained set, so not every region goes straight back: the allocator
	// pools some and releases others, and a pure alloc/free cycle would leave
	// the address space far tidier than the real thing does.
	retained := make([]uintptr, 0, 64)

	for i := 0; ; i++ {
		select {
		case <-stop:
			for _, p := range retained {
				procVirtualFre.Call(p, 0, memRelease)
			}
			return
		default:
		}

		size := sizes[i%len(sizes)]
		addr, _, _ := procVirtualAll.Call(0, size, memCommit|memReserve, pageReadWrite)
		if addr == 0 {
			continue
		}
		// The region is not touched. MEM_COMMIT already charges the commit, and
		// the property being modelled is the churn in the address space beside
		// the Go heap's reservations, not the working set. Writing through the
		// returned uintptr would also be the one thing in this repository that
		// `go vet` calls a possible misuse of unsafe.Pointer, which is a poor
		// look for a reproducer meant to argue that the fault is not the
		// author's unsafe code.

		if len(retained) < cap(retained) && i%3 == 0 {
			retained = append(retained, addr)
			continue
		}
		if len(retained) > 0 && i%7 == 0 {
			j := i % len(retained)
			procVirtualFre.Call(retained[j], 0, memRelease)
			retained[j] = addr
			continue
		}
		procVirtualFre.Call(addr, 0, memRelease)
	}
}
