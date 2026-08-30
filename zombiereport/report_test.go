// Package zombiereport_test measures what the runtime tells you when it finds a
// pointer to a free object.
//
// The sweeper decides to report from s.gcmarkBits, which moveInlineMarks has
// just filled in. reportZombies then reads the marks back through
// markBitsForBase, which for a span with inline mark bits returns
// s.inlineMarkBits().marks -- the array moveInlineMarks cleared one line
// earlier. So under the default collector the report reads a bitmap that is
// guaranteed to be zero, names no object, dumps no memory, and aborts anyway.
//
// This matters for stokaro/ptah#2365: every abort observed there is
// elemsize=16, which is inside the range that takes the inline path, so every
// crash dump collected for it was empty of evidence by construction rather
// than by chance.
package zombiereport_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// dumpRow matches one object line of the sweeper's report.
var dumpRow = regexp.MustCompile(`(?m)^0x[0-9a-f]+ (?:alloc|free ) (?:un)?marked`)

// zombieRow matches an object line the report actually identified.
var zombieRow = regexp.MustCompile(`(?m)^0x[0-9a-f]+ .*zombie`)

// markedRow matches an object line whose mark bit the report read as set.
var markedRow = regexp.MustCompile(`(?m)^0x[0-9a-f]+ (?:alloc|free ) marked`)

// hexdumpRow matches a line of the memory dump that follows an identified zombie.
var hexdumpRow = regexp.MustCompile(`(?m)^0*[0-9a-f]+:  ?[0-9a-f]{8}`)

// report builds the zombie constructor under one GOEXPERIMENT and returns what
// the runtime printed on its way out.
func report(t *testing.T, experiment string) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "makezombie")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	build := exec.Command("go", "build", "-o", binary, "./cmd/makezombie")
	build.Env = append(os.Environ(), "GOEXPERIMENT="+experiment)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building with GOEXPERIMENT=%q: %v\n%s", experiment, err, out)
	}

	out, err := exec.Command(binary).CombinedOutput()
	text := string(out)
	if err == nil {
		t.Fatalf("GOEXPERIMENT=%q: the program exited cleanly, so no zombie was constructed "+
			"and this test measured nothing:\n%s", experiment, text)
	}
	if !strings.Contains(text, "fatal error: found pointer to free object") {
		t.Fatalf("GOEXPERIMENT=%q: exited with %v but not on the zombie abort:\n%s", experiment, err, text)
	}
	return text
}

// TestDefaultCollectorNamesNoObject is the defect. The report is emitted, it is
// the right length, and every row of it says unmarked.
func TestDefaultCollectorNamesNoObject(t *testing.T) {
	text := report(t, "")

	rows := len(dumpRow.FindAllString(text, -1))
	if rows == 0 {
		t.Fatalf("no object rows in the report at all:\n%s", text)
	}
	if got := len(zombieRow.FindAllString(text, -1)); got != 0 {
		t.Errorf("identified zombies = %d, want 0 for the defect to be present", got)
	}
	if got := len(markedRow.FindAllString(text, -1)); got != 0 {
		t.Errorf("rows read as marked = %d, want 0: the report read the cleared inline bits", got)
	}
	if got := len(hexdumpRow.FindAllString(text, -1)); got != 0 {
		t.Errorf("hexdump lines = %d, want 0: nothing was identified to dump", got)
	}
	t.Logf("default collector: %d object rows, none marked, none identified", rows)
}

// TestWithoutGreenTeaTheObjectIsNamed is the control. Same program, same
// zombie, a collector that does not use inline mark bits -- and the report
// names the object and dumps its contents.
func TestWithoutGreenTeaTheObjectIsNamed(t *testing.T) {
	text := report(t, "nogreenteagc")

	zombies := zombieRow.FindAllString(text, -1)
	if len(zombies) != 1 {
		t.Fatalf("identified zombies = %d, want 1:\n%s", len(zombies), text)
	}
	if got := len(markedRow.FindAllString(text, -1)); got == 0 {
		t.Error("no row was read as marked, so the marks were not visible here either")
	}
	if got := len(hexdumpRow.FindAllString(text, -1)); got == 0 {
		t.Errorf("the zombie was named but its memory was not dumped:\n%s", text)
	}
	t.Logf("without green tea: %s", strings.TrimSpace(zombies[0]))
}
