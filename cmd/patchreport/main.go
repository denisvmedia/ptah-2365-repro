// Command patchreport makes the sweeper's zombie report name the object it
// aborted on, without giving up the collector the fault needs.
//
// reportZombies reads the marks through markBitsForBase, which for a span with
// inline mark bits returns s.inlineMarkBits().marks -- the array
// moveInlineMarks clears one line earlier. The merged marks the caller decided
// on live in s.gcmarkBits. This is golang/go#80799, and the substitution below
// is the fix that issue proposes.
//
// It is applied as a build overlay rather than by editing GOROOT: `go test -c`
// rebuilds the runtime from the overlaid source, so nothing on the machine is
// modified and no toolchain rebuild is needed.
//
// GOEXPERIMENT=nogreenteagc would also produce a named zombie, and it is the
// wrong instrument here: measured over 44 jobs it appears to remove the fault
// itself, so a run under it is a run that cannot report. This keeps the default
// collector, and therefore the fault, and repairs only the reporting.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	marker = "func (s *mspan) reportZombies() {"
	oldSt  = "mbits := s.markBitsForBase()"
	newSt  = "mbits := markBits{&s.gcmarkBits.x, uint8(1), 0}"

	// The collector's own consistency asserts, off by default. The one worth
	// having here is in moveInlineMarks: for a scannable span it requires the
	// inline marks to equal the inline scans, and that separates the two
	// readings of a lost mark. A write from outside that clears a mark leaves
	// the scan set, so the arrays diverge and this throws at the sweep that
	// notices; a collector that simply never marked the object leaves both
	// clear, so it stays silent and the zombie report is what speaks.
	dcFile = "mgcmark_greenteagc.go"
	dcOld  = "const doubleCheckGreenTea = false"
	dcNew  = "const doubleCheckGreenTea = true"
)

func main() {
	out := flag.String("out", ".", "directory to write the patched file and the overlay into")
	doubleCheck := flag.Bool("doublecheck", false, "also enable the collector's Green Tea consistency asserts")
	flag.Parse()

	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		fail("asking for GOROOT: %v", err)
	}
	src := filepath.Join(strings.TrimSpace(string(goroot)), "src", "runtime", "mgcsweep.go")

	body, err := os.ReadFile(src)
	if err != nil {
		fail("reading %s: %v", src, err)
	}
	text := string(body)

	// The statement appears twice in this file. The other one is in sweep's
	// newly-freed scan, which runs BEFORE moveInlineMarks and so reads the
	// inline bits while they are still populated -- that use is correct and
	// must not be touched. Anchor on the function instead of on the statement.
	if n := strings.Count(text, marker); n != 1 {
		fail("expected exactly one %q, found %d; the runtime has moved and this patch must be re-read", marker, n)
	}
	head, tail, _ := strings.Cut(text, marker)

	if n := strings.Count(tail, oldSt); n != 1 {
		fail("expected exactly one %q after the function opens, found %d", oldSt, n)
	}
	patched := head + marker + strings.Replace(tail, oldSt, newSt, 1)
	if patched == text {
		fail("the substitution changed nothing")
	}

	dst := filepath.Join(*out, "mgcsweep_patched.go")
	if err := os.WriteFile(dst, []byte(patched), 0o644); err != nil {
		fail("writing %s: %v", dst, err)
	}
	replace := map[string]string{src: dst}

	if *doubleCheck {
		dcSrc := filepath.Join(filepath.Dir(src), dcFile)
		dcBody, err := os.ReadFile(dcSrc)
		if err != nil {
			fail("reading %s: %v", dcSrc, err)
		}
		if n := strings.Count(string(dcBody), dcOld); n != 1 {
			fail("expected exactly one %q in %s, found %d", dcOld, dcSrc, n)
		}
		dcDst := filepath.Join(*out, "mgcmark_greenteagc_patched.go")
		if err := os.WriteFile(dcDst, []byte(strings.Replace(string(dcBody), dcOld, dcNew, 1)), 0o644); err != nil {
			fail("writing %s: %v", dcDst, err)
		}
		replace[dcSrc] = dcDst
	}

	overlay := filepath.Join(*out, "overlay.json")
	blob, err := json.Marshal(struct {
		Replace map[string]string
	}{Replace: replace})
	if err != nil {
		fail("encoding the overlay: %v", err)
	}
	if err := os.WriteFile(overlay, blob, 0o644); err != nil {
		fail("writing %s: %v", overlay, err)
	}
	fmt.Println(overlay)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "patchreport: "+format+"\n", args...)
	os.Exit(1)
}
