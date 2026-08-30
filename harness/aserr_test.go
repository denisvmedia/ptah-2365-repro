package harness_test

import (
	"errors"
	"os/exec"
)

// asExitError keeps the assertion in the caller free of a branch, so the
// workload reads as a sequence of steps.
func asExitError(err error, target **exec.ExitError) bool { return errors.As(err, target) }
