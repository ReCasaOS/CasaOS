//go:build !linux

package trash

import (
	"fmt"
	"time"
)

var errUnsupported = fmt.Errorf("%w: the trash needs Linux", ErrUnavailable)

func Put(string, time.Time) error          { return errUnsupported }
func List() []Item                         { return nil }
func Delete(string, string, Allowed) error { return errUnsupported }
func Empty(Allowed) (int, error)           { return 0, errUnsupported }
func Purge(time.Time) (int, error)         { return 0, nil }
func Restore(string, string, Allowed, func(string) (string, error)) (string, error) {
	return "", errUnsupported
}
