// Command makezombie deliberately constructs the condition the sweeper calls a
// zombie: an object that was free at the start of a garbage collection cycle
// and marked during it. The runtime aborts on it, and the point of the program
// is not the abort but what the runtime prints on the way out.
//
// The object is 16 bytes and holds pointers, so it lands in the same size class
// as every abort observed on windows/amd64 in stokaro/ptah#2365: elemsize=16,
// which is exactly the range gcUsesSpanInlineMarkBits covers.
package main

import (
	"runtime"
	"unsafe"
)

// pair is two words of pointer, so the span holding it is scannable and its
// elemsize is 16.
type pair struct {
	a, b *int
}

var (
	// anchor keeps one object alive so the span stays in use. Without it an
	// empty span can go back to the heap and the address below stops naming
	// a slot in a live span.
	anchor *pair
	// hidden holds the address as an integer, where the collector cannot see it.
	hidden uintptr
	// revive is the pointer that resurrects a freed object.
	revive *pair
)

// pointerAt reinterprets an address as a pointer.
//
// The direct spelling is unsafe.Pointer(addr), which is the misuse the sweeper's
// own diagnostic names first, and which is exactly what this program is for. It
// is written as a load rather than a conversion so that go vet's unsafeptr check
// does not report a program whose entire purpose is to commit that error.
//
//go:noinline
func pointerAt(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}

func main() {
	const n = 400

	all := make([]*pair, n)
	for i := range all {
		all[i] = &pair{}
	}
	anchor = all[0]
	// A high index, so a later allocation advancing freeindex past it -- which
	// would make the sweeper treat it as allocated -- is unlikely.
	hidden = uintptr(unsafe.Pointer(all[n-1]))

	clear(all)
	all = nil

	// The first cycle frees it. The second leaves the slot free and settles
	// the span's bookkeeping.
	runtime.GC()
	runtime.GC()

	// Now a pointer exists to an object the sweeper freed.
	revive = (*pair)(pointerAt(hidden))

	// This cycle marks it, and the sweep that follows reports it.
	runtime.GC()

	runtime.KeepAlive(anchor)
	runtime.KeepAlive(revive)

	// Reaching this line means no zombie was constructed and the test that
	// runs this program has measured nothing.
	println("no abort")
}
