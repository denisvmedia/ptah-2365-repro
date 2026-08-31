//go:build windows && amd64

package main

import (
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	heapPage        = uintptr(8 << 10)
	stackSize       = 2 * heapPage
	groups          = 256
	cellsPerGroup   = 1024
	workersPerGroup = 2
	workerCount     = groups * workersPerGroup
	targetPadDelta  = uintptr(0x3208)
	panicSentinel   = "REPRO_CONTROL_PANIC"
)

type cell struct {
	next *cell
	tag  uintptr
}

type faultTarget struct {
	_     [0x118]byte
	value uintptr
}

type workerState struct {
	stackLo     uintptr
	stackMarker uintptr
	selected    bool
	before      [32]byte
	after       [32]byte
	verified    bool
}

var (
	ready     atomic.Uint32
	start     = make(chan struct{})
	done      sync.WaitGroup
	faultMode bool
	nilTarget *faultTarget
)

//go:noinline
func readAt118(target *faultTarget) uintptr {
	return target.value
}

//go:noinline
func exposeAtGuard(state *workerState) {
	var pad [112]byte
	address := uintptr(unsafe.Pointer(&pad[0]))
	if address < state.stackLo || address >= state.stackLo+stackSize {
		panic("REPRO_INVARIANT exposure left the calibrated stack")
	}
	delta := address - state.stackLo
	pad[delta%uintptr(len(pad))] = byte(delta)
	if delta > targetPadDelta {
		exposeAtGuard(state)
	} else if delta < targetPadDelta {
		panic("REPRO_INVARIANT exposure skipped the calibrated offset")
	} else if faultMode {
		value := readAt118(nilTarget)
		runtime.KeepAlive(value)
	} else {
		panic(panicSentinel)
	}
	runtime.KeepAlive(&pad)
}

//go:noinline
func exposeOnce(state *workerState) {
	var phase [96]byte
	phase[0] = 1
	defer func() {
		snapshot(&state.after, state.stackLo-uintptr(len(state.after)))
		print("REPRO_TAIL before=")
		print(hex(&state.before))
		print(" after=")
		println(hex(&state.after))
		recovered := recover()
		if faultMode {
			if _, ok := recovered.(runtime.Error); !ok {
				panic("REPRO_INVARIANT hardware fault was not recovered")
			}
		} else if recovered != panicSentinel || state.before != state.after {
			panic("REPRO_INVARIANT panic control changed the adjacent page")
		}
		state.verified = true
	}()
	exposeAtGuard(state)
	runtime.KeepAlive(&phase)
}

//go:noinline
func growAndPark(state *workerState) {
	var hold [4096]byte
	var marker byte
	hold[0] = 1
	address := uintptr(unsafe.Pointer(&marker))
	page := pageBase(address)
	if page < heapPage {
		panic("REPRO_INVARIANT stack address underflow")
	}
	state.stackLo = page - heapPage
	state.stackMarker = address
	ready.Add(1)
	<-start
	if uintptr(unsafe.Pointer(&marker)) != state.stackMarker {
		panic("REPRO_INVARIANT stack moved during layout GC")
	}
	runtime.KeepAlive(&marker)
	runtime.KeepAlive(&hold)
}

func worker(state *workerState) {
	var frame byte
	growAndPark(state)
	if state.selected {
		exposeOnce(state)
	}
	runtime.KeepAlive(&frame)
	done.Done()
}

func seedWorkers(count int) {
	var completed sync.WaitGroup
	completed.Add(count)
	release := make(chan struct{})
	for range count {
		go func() {
			<-release
			completed.Done()
		}()
	}
	close(release)
	completed.Wait()
	runtime.Gosched()
}

func pageBase(address uintptr) uintptr {
	return address &^ (heapPage - 1)
}

func snapshot(destination *[32]byte, address uintptr) {
	copy(destination[:], unsafe.Slice((*byte)(unsafe.Pointer(address)), len(destination)))
}

func hex(data *[32]byte) string {
	const digits = "0123456789abcdef"
	var encoded [64]byte
	for index, value := range data {
		encoded[index*2] = digits[value>>4]
		encoded[index*2+1] = digits[value&0xf]
	}
	return string(encoded[:])
}

func preflight() {
	if len(os.Args) != 2 || (os.Args[1] != "fault" && os.Args[1] != "panic") {
		panic("usage: repro.exe fault|panic")
	}
	faultMode = os.Args[1] == "fault"
	if unsafe.Sizeof(cell{}) != 16 || unsafe.Offsetof(faultTarget{}.value) != 0x118 {
		panic("REPRO_INVARIANT type layout changed")
	}
	sample := []metrics.Sample{{Name: "/gc/stack/starting-size:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 || sample[0].Value.Uint64() != uint64(heapPage) {
		panic("REPRO_INVARIANT starting goroutine stack is not 8192 bytes")
	}
}

func main() {
	runtime.GOMAXPROCS(1)
	debug.SetGCPercent(-1)
	preflight()

	states := make([]workerState, workerCount)
	victims := make([]*cell, groups)
	seedWorkers(workerCount + 32)
	runtime.GC()

	for group := range groups {
		var head *cell
		for index := range cellsPerGroup {
			head = &cell{next: head, tag: uintptr(index)}
		}
		victims[group] = head
		for index := range workersPerGroup {
			workerIndex := group*workersPerGroup + index
			done.Add(1)
			go worker(&states[workerIndex])
		}
		for ready.Load() != uint32((group+1)*workersPerGroup) {
			runtime.Gosched()
		}
	}

	pages := make(map[uintptr]struct{}, groups)
	for _, victim := range victims {
		victim.next = nil
		pages[pageBase(uintptr(unsafe.Pointer(victim)))] = struct{}{}
	}
	runtime.GC()
	runtime.GC()

	selected := -1
	for index := range states {
		stackLo := states[index].stackLo
		if selected < 0 && stackLo >= heapPage {
			if _, paired := pages[stackLo-heapPage]; paired {
				selected = index
			}
		}
	}
	if selected < 0 {
		panic("REPRO_LAYOUT_FAILED no class-16 span before a 16 KiB stack")
	}
	state := &states[selected]
	state.selected = true
	snapshot(&state.before, state.stackLo-uintptr(len(state.before)))
	print("REPRO_PAIR span=")
	print(state.stackLo - heapPage)
	print(" stack_lo=")
	println(state.stackLo)

	close(start)
	done.Wait()
	if !state.verified {
		panic("REPRO_INVARIANT selected worker did not run")
	}

	runtime.GC()
	runtime.GC()
	runtime.KeepAlive(victims)
	runtime.KeepAlive(pages)
	println("REPRO_RESULT clean")
}
