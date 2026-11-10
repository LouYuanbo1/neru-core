package compositor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// A compositor CLI is run through os/exec, the same pure-Go pattern the neru
// daemon's compositorcli adapter uses. Nothing here parses a command's output as
// text beyond JSON, and nothing quotes it: what a compositor CLI prints is a
// window title as often as not, and no title belongs in an error.

const (
	// queryTimeout bounds one compositor query. Every query can run on a path
	// that is about to place hints, so a wedged compositor has to become a missing
	// answer rather than a stalled caller. A healthy CLI answers in milliseconds.
	queryTimeout = 500 * time.Millisecond

	// queryPipeGuard bounds how long the read of a CLI's stdout may continue after
	// the deadline has killed the process, for a CLI that leaked a child
	// inheriting the pipe. It is a ceiling on a pathology, not part of the
	// ordinary cost.
	queryPipeGuard = time.Second
)

// queryJSON runs a compositor CLI bounded by ctx and queryTimeout and decodes its
// JSON stdout into dst.
//
// A nil error means the compositor answered. Empty output is treated as an empty
// answer rather than a failure: several of these command lines print nothing
// when no window is focused, which is the compositor saying "nothing", not a
// query that failed.
func queryJSON(ctx context.Context, dst any, name string, args ...string) error {
	callCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, name, args...)
	cmd.WaitDelay = queryPipeGuard

	out, err := cmd.Output()
	if err != nil {
		return commandError(callCtx, name, args, err)
	}

	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}

	err = json.Unmarshal(out, dst)
	if err != nil {
		// The decoder's own complaint is dropped rather than wrapped: it quotes the
		// byte it stopped at, and a compositor's output is window contents. Which
		// command failed and that its answer was undecodable is the whole of what
		// a reader can act on anyway.
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"%s: its answer could not be decoded as JSON",
			commandLine(name, args),
		)
	}

	return nil
}

// commandError maps a failed query onto the library's error vocabulary. The
// distinction is load-bearing: "not installed" and "the session has no such
// compositor" are CodeNotSupported (nobody to ask), while a CLI that is present
// and refused or hung is a failure the caller may retry.
func commandError(ctx context.Context, name string, args []string, err error) error {
	command := commandLine(name, args)

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errs.Wrapf(err, errs.CodeTimeout, "%s did not answer before the query deadline", command)
	case errors.Is(ctx.Err(), context.Canceled):
		return errs.Wrapf(err, errs.CodeContextCanceled, "%s was canceled", command)
	case errors.Is(err, exec.ErrNotFound), errors.As(err, new(*exec.Error)):
		return errs.Wrapf(err, errs.CodeNotSupported,
			"%s is missing or not executable, so the compositor cannot be asked", command)
	case errors.As(err, new(*exec.ExitError)):
		return errs.Wrapf(err, errs.CodeAccessibilityFailed, "%s exited with an error", command)
	default:
		return errs.Wrapf(err, errs.CodeAccessibilityFailed, "%s could not be run", command)
	}
}

// commandLine spells a command as it was asked, so a message names the
// compositor and the question. The name and arguments are literals in this
// package's source; nothing a compositor said reaches here.
func commandLine(name string, args []string) string {
	if len(args) == 0 {
		return name
	}

	return name + " " + strings.Join(args, " ")
}
