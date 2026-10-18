package modulecmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"time"
)

// logScanBytes is how much of a log is read: the whole of it up to this, and beyond it the first half and the last half of this, since the assistant writes its markers at the start of a
// step and at the end of the job and what the tools print is in between. The log of a run is some megabytes at the most (the output of the tools is cut at 1 MiB), so a log beyond this
// is not one a run made. A var, for the tests.
var logScanBytes int64 = 8 << 20

// ReadJob says how the job of the module called name is going, from its log and the state of its unit. The unit is looked at BEFORE the log is read (a unit that is gone has written
// everything it wrote), and now is the time of the core's clock. A module that has no log has no job. The log is a regular file that is not reached through a link, and the first
// maxLine bytes of a line are all that is held of it.
func ReadJob(env Env, name string, unit UnitSight, now time.Time) (Job, error) {
	if !nameRe.MatchString(name) {
		return Job{}, errors.New("not a module name")
	}
	env = env.withDefaults()
	r := NewReducer(name, "")
	var written time.Time
	f, err := os.OpenFile(env.Roots.logFile(name), os.O_RDONLY|noFollow|nonBlock, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
	case err != nil:
		return Job{}, err
	default:
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return Job{}, err
		}
		if !info.Mode().IsRegular() {
			return Job{}, errors.New("the log is not a regular file")
		}
		written = info.ModTime()
		if err := scanLog(r, f, info.Size()); err != nil {
			return Job{}, err
		}
	}
	return r.Job(unit, now, written), nil
}

// scanLog gives the reducer the lines of the first size bytes of f, or of its two ends when that is more than logScanBytes.
func scanLog(r *Reducer, f *os.File, size int64) error {
	if size <= logScanBytes {
		return scanLines(r, io.LimitReader(f, size), false)
	}
	half := logScanBytes / 2
	if err := scanLines(r, io.LimitReader(f, half), false); err != nil {
		return err
	}
	// one byte before the window: it is a new line when the window starts on a line, and what is read up to the first new line is skipped, which loses the end of a line that is cut and
	// an empty line
	if _, err := f.Seek(size-half-1, io.SeekStart); err != nil {
		return err
	}
	return scanLines(r, io.LimitReader(f, half+1), true)
}

// scanLines feeds the lines of in to the reducer. A line that ends with a new line is whole; the first maxLine bytes of one that is longer are given, not whole, and the rest is dropped;
// the last line, when it has no new line, is given and is not whole. skipFirst drops what is read up to the first new line.
func scanLines(r *Reducer, in io.Reader, skipFirst bool) error {
	br := bufio.NewReaderSize(in, maxLine)
	skipping := skipFirst
	for {
		fragment, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			if !skipping {
				r.Feed(string(fragment[:len(fragment)-1]), true)
			}
			skipping = false
		case errors.Is(err, bufio.ErrBufferFull):
			if !skipping {
				r.Feed(string(fragment), false)
			}
			skipping = true
		case errors.Is(err, io.EOF):
			if !skipping && len(fragment) > 0 {
				r.Feed(string(fragment), false)
			}
			return nil
		default:
			return fmt.Errorf("the log cannot be read: %w", err)
		}
	}
}

var logNameRe = regexp.MustCompile(`^module-([a-z][a-z0-9-]{0,31})\.log$`)

// JobLogs are the names of the modules that have a log, in the order the folder gives them. A folder that is not there has none; one that cannot be read is an error.
func JobLogs(env Env) ([]string, error) {
	env = env.withDefaults()
	entries, err := os.ReadDir(env.Roots.logs())
	if errors.Is(err, fs.ErrNotExist) || isNotDir(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if m := logNameRe.FindStringSubmatch(e.Name()); m != nil {
			names = append(names, m[1])
		}
	}
	return names, nil
}

// RemoveRefusal is the code that a remove of the module called name is refused with, "" when it is not: there is nothing of the module to remove (CodeNotInstalled), or a package manager
// or the upgrade of the operating system is at work (CodeBusy). It reads and changes nothing.
func RemoveRefusal(ctx context.Context, env Env, name string) string {
	if !nameRe.MatchString(name) {
		return CodeNotInstalled
	}
	return removeRefusal(ctx, env.withDefaults(), name)
}
