package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"
)

const (
	reportMarker = "func (s *mspan) reportZombies() {"
	reportOld    = "mbits := s.markBitsForBase()"
	reportNew    = "mbits := markBits{&s.gcmarkBits.x, uint8(1), 0}"
)

type pair struct{ a, b *int }

var (
	anchor *pair
	hidden uintptr
	revive *pair
)

func main() {
	out := flag.String("out", "", "write overlay.json and patched runtime sources here")
	zombie := flag.Bool("zombie", false, "deliberately trigger runtime.reportZombies")
	flag.Parse()

	if *zombie {
		if *out != "" || flag.NArg() != 0 {
			fail("-zombie accepts no other arguments")
		}
		makeZombie()
		return
	}
	if *out == "" || flag.NArg() != 0 {
		fail("-out is required and positional arguments are not accepted")
	}
	writeOverlay(*out)
}

func writeOverlay(out string) {
	out, err := filepath.Abs(out)
	if err != nil {
		fail("resolve output directory: %v", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fail("create output directory: %v", err)
	}
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		fail("find GOROOT: %v", err)
	}
	runtimeDir := filepath.Join(strings.TrimSpace(string(goroot)), "src", "runtime")
	replace := map[string]string{}

	mgcSource := filepath.Join(runtimeDir, "mgcsweep.go")
	mgcDestination := filepath.Join(out, "mgcsweep.go")
	writeReportPatch(mgcSource, mgcDestination)
	replace[mgcSource] = mgcDestination

	data, err := json.Marshal(struct {
		Replace map[string]string
	}{replace})
	if err != nil {
		fail("encode overlay: %v", err)
	}
	overlay := filepath.Join(out, "overlay.json")
	if err := os.WriteFile(overlay, data, 0o644); err != nil {
		fail("write overlay: %v", err)
	}
	fmt.Println(overlay)
}

func writeReportPatch(source, destination string) {
	body, err := os.ReadFile(source)
	if err != nil {
		fail("read %s: %v", source, err)
	}
	text := string(body)
	if strings.Count(text, reportMarker) != 1 {
		fail("expected one %q in %s", reportMarker, source)
	}
	head, tail, _ := strings.Cut(text, reportMarker)
	if strings.Count(tail, reportOld) != 1 {
		fail("expected one %q after %q", reportOld, reportMarker)
	}
	text = head + reportMarker + strings.Replace(tail, reportOld, reportNew, 1)
	if err := os.WriteFile(destination, []byte(text), 0o644); err != nil {
		fail("write %s: %v", destination, err)
	}
}

//go:noinline
func pointerAt(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}

func makeZombie() {
	const count = 400
	all := make([]*pair, count)
	for i := range all {
		all[i] = &pair{}
	}
	anchor = all[0]
	hidden = uintptr(unsafe.Pointer(all[count-1]))
	clear(all)
	all = nil
	runtime.GC()
	runtime.GC()
	revive = (*pair)(pointerAt(hidden))
	runtime.GC()
	runtime.KeepAlive(anchor)
	runtime.KeepAlive(revive)
	fmt.Println("no abort")
}

func fail(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "repro: "+format+"\n", values...)
	os.Exit(1)
}
