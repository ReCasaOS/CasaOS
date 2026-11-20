// Package fileaccess says what one request of the file API may reach: the box's roots
// ([file] ManagementRoots, fileroots.Default) for the owner and the box's own services,
// and, for an account of role user, only the folders the owner gave it, a write only in
// the ones it may write (CasaOS#168).
//
// The Gateway opens Files to an account with at least one folder, but it does not see the
// paths of a call (bodies, multipart uploads): this is where they are held to the folders,
// with the same openat2 resolution as the box's roots (a link or `..` does not lead out).
package fileaccess

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
)

// Intent is what a path is reached for.
type Intent int

const (
	Read Intent = iota
	Write
)

var (
	// ErrRoleUnknown: user-service has not said yet what the account is; asked again shortly.
	ErrRoleUnknown = errors.New("the account's role is not known yet: try again in a moment")
	// ErrAccountGone: the account was deleted.
	ErrAccountGone = errors.New("this account no longer exists")
	// ErrNoFolder: no folder of Files is given to the account.
	ErrNoFolder = errors.New("no folder of Files is given to this account")
	// ErrReadOnly: the account may read this folder, not change it.
	ErrReadOnly = errors.New("this folder is read-only for this account")
)

// AccountOf and FoldersOf are what user-service says of an account (variables for the
// tests of this package and of the routes).
var (
	AccountOf = external.Account
	FoldersOf = external.FoldersOf
)

// Scope is what one request may reach.
type Scope struct {
	user        bool
	read, write *fileroots.Roots // the account's folders: all of them, the writable ones
	readable    []string
	writable    []string
}

// Owner reaches what the box's roots allow.
var Owner = &Scope{}

// For is the scope of a request: internal (the box's own services) or the owner's account
// reach the box's roots; an account of role user reaches its folders only. Closed when
// user-service has no fresh answer: a role not known is ErrRoleUnknown, never taken as
// the owner's, and a user's folders are none unless the answer is fresh.
//
// No userID is the box's own: the token check sets it on every signed-in request, and is
// skipped only for an internal one.
func For(runtimePath, userID string, internal bool) (*Scope, error) {
	if internal || userID == "" {
		return Owner, nil
	}
	role, gone, ok := AccountOf(runtimePath, userID)
	switch {
	case !ok:
		return nil, ErrRoleUnknown
	case gone:
		return nil, ErrAccountGone
	case role == "admin":
		return Owner, nil
	}

	s := &Scope{user: true, readable: []string{}, writable: []string{}}
	for _, f := range FoldersOf(runtimePath, userID) {
		s.readable = append(s.readable, f.Path)
		if f.Write {
			s.writable = append(s.writable, f.Path)
		}
	}
	s.read = open(s.readable, ErrNoFolder)
	s.write = open(s.writable, ErrReadOnly)

	return s, nil
}

// open is roots that hold the paths, or that refuse everything with none: an empty Roots
// would be unconfined.
func open(paths []string, none error) *fileroots.Roots {
	if len(paths) == 0 {
		return fileroots.Refusing(none)
	}
	roots, err := fileroots.Open(paths)
	if err != nil {
		return fileroots.Refusing(err)
	}

	return roots
}

// User says whether the scope is an account's folders rather than the box's roots.
func (s *Scope) User() bool { return s.user }

// Readable and Writable are the account's folders; nil for the owner.
func (s *Scope) Readable() []string { return s.readable }
func (s *Scope) Writable() []string { return s.writable }

// Check replaces each *path with its canonical spelling once it is below the box's roots
// and, for an account, below one of its folders (a writable one for Write).
func (s *Scope) Check(intent Intent, paths ...*string) error {
	if err := fileroots.Default.CheckEach(paths...); err != nil {
		return err
	}
	if !s.user {
		return nil
	}
	roots := s.read
	if intent == Write {
		roots = s.write
	}
	if err := roots.CheckEach(paths...); err != nil {
		if errors.Is(err, fileroots.ErrOutside) && intent == Write && s.read.CheckEach(clone(paths)...) == nil {
			return ErrReadOnly
		}
		return err
	}

	return nil
}

// CheckEntries is for what deletes, renames or moves the entry itself: its folder is
// checked, not what it leads to, and a root (an account's folder itself) is refused. An
// account needs a writable folder.
func (s *Scope) CheckEntries(paths ...*string) error {
	if err := fileroots.Default.CheckEntries(paths...); err != nil {
		return err
	}
	if !s.user {
		return nil
	}
	if err := s.write.CheckEntries(paths...); err != nil {
		if errors.Is(err, fileroots.ErrOutside) && s.read.CheckEntries(clone(paths)...) == nil {
			return ErrReadOnly
		}
		return err
	}

	return nil
}

// Listing is the roots a listing follows: above them, a folder shows only the way down.
func (s *Scope) Listing() *fileroots.Roots {
	if s.user {
		return s.read
	}

	return fileroots.Default
}

// FolderOf is the account's folder that holds the canonical path p (as Check gives it),
// and p relative to it ("." for the folder itself); "" when none does.
func (s *Scope) FolderOf(p string) (folder, rel string) {
	for _, f := range s.readable {
		if p == f {
			return f, "."
		}
		if strings.HasPrefix(p, f+"/") {
			return f, strings.TrimPrefix(p, f+"/")
		}
	}

	return "", ""
}

// clone copies the paths, for a check that must not respell them.
func clone(paths []*string) []*string {
	out := make([]*string, len(paths))
	for i, p := range paths {
		c := *p
		out[i] = &c
	}

	return out
}

// ErrPathNotRooted is a route that does not yet reach an account's folders through their
// root: it is not open to an account (see Pin and Root).
var ErrPathNotRooted = errors.New("this is not open to an account")

// Root is, for an account, the os.Root of its folder that holds p, and p relative to it,
// once p passes Check (CheckEntries for an entry renamed or deleted). Whatever is renamed
// after, an operation through the root stays in the folder. folder is the folder's path.
func (s *Scope) Root(intent Intent, p string, entry bool) (root *os.Root, folder, rel string, err error) {
	if !s.user {
		return nil, "", "", ErrPathNotRooted
	}
	if entry {
		err = s.CheckEntries(&p)
	} else {
		err = s.Check(intent, &p)
	}
	if err != nil {
		return nil, "", "", err
	}
	folder, rel = s.FolderOf(p)
	if folder == "" {
		return nil, "", "", fileroots.ErrOutside
	}
	if root, err = fileroots.OpenFolder(folder); err != nil {
		return nil, "", "", err
	}

	return root, folder, rel, nil
}

// Pin opens p for reading, for an account through the root of its folder, and gives the
// path that reaches that very file or folder from now on: /proc/self/fd/N, which a later
// rename does not move. The caller closes f once it is done with pinned.
func (s *Scope) Pin(p string) (pinned string, f *os.File, err error) {
	root, _, rel, err := s.Root(Read, p, false)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	// O_NONBLOCK: a pipe in the file's place does not hold the request
	if f, err = root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0); err != nil {
		return "", nil, err
	}

	return fmt.Sprintf("/proc/self/fd/%d", f.Fd()), f, nil
}
