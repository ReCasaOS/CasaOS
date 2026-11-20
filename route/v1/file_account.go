package v1

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"syscall"

	cfile "github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// The changes an account makes in Files (CasaOS#168), each through the os.Root of the folder
// it was given: the kernel resolves every name relative to that folder, so a folder swapped
// for a link out of it, by the account or by an app writing there, leads nowhere outside.
// The owner's requests keep their own paths (route/v1/file.go).

// pinned is, for an account, *p opened through the root of its folder and replaced with
// the path that reaches that very file from now on (/proc/self/fd/N); done closes it. For
// the owner, *p checked against the box's roots and kept.
func pinned(ctx echo.Context, p *string) (done func(), err error) {
	done = func() {}
	s, err := scope(ctx)
	if err != nil {
		return done, err
	}
	if !s.User() {
		return done, s.Check(fileaccess.Read, p)
	}
	at, f, err := s.Pin(*p)
	if err != nil {
		return done, err
	}
	*p = at
	return func() { f.Close() }, nil
}

// rooted is, for an account, the root of its folder that holds p and p relative to it,
// once p passes its policy (entry: a name renamed or deleted, not what it leads to). For
// the owner, a nil root and p checked as managed and managedEntry check it.
func rooted(ctx echo.Context, intent fileaccess.Intent, p *string, entry bool) (root *os.Root, rel string, err error) {
	s, err := scope(ctx)
	if err != nil {
		return nil, "", err
	}
	if !s.User() {
		if entry {
			return nil, "", s.CheckEntries(p)
		}
		return nil, "", s.Check(intent, p)
	}
	root, _, rel, err = s.Root(intent, *p, entry)
	return root, rel, err
}

// accountMkdir makes the folder rel, and what leads to it.
func accountMkdir(root *os.Root, rel string) int {
	if _, err := root.Lstat(rel); err == nil {
		return common_err.DIR_ALREADY_EXISTS
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return common_err.SERVICE_ERROR
	}
	return common_err.SUCCESS
}

// accountCreate makes the empty file rel; a name already taken is left as it is.
func accountCreate(root *os.Root, rel string) int {
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return common_err.FILE_OR_DIR_EXISTS
	}
	if err != nil {
		return common_err.SERVICE_ERROR
	}
	f.Close()
	return common_err.SUCCESS
}

// accountRename renames from to to, both in the folder of root; a name taken is refused.
func accountRename(root *os.Root, from, to string) int {
	if _, err := root.Lstat(to); err == nil {
		return common_err.DIR_ALREADY_EXISTS
	}
	if err := root.Rename(from, to); err != nil {
		return common_err.SERVICE_ERROR
	}
	return common_err.SUCCESS
}

// accountSave replaces the regular file rel with content, keeping its mode: written aside
// in its folder, then moved in place, so a failure leaves the old one whole.
func accountSave(root *os.Root, rel string, content []byte) error {
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a file")
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(rel), ".casaos-save-"+hex.EncodeToString(random))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	// the file keeps its owner, as the owner's save keeps it: an app still reads its config
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		err = f.Chown(int(st.Uid), int(st.Gid))
	}
	if err == nil {
		err = f.Chmod(info.Mode().Perm()) // the chown cleared nothing worth keeping: no setuid copied
	}
	if err == nil {
		_, err = f.Write(content)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}

// policyEntry: an entry a request moves, held to what the account may reach, for work that
// then goes through the root of its folders.
func policyEntry(ctx echo.Context, p *string) error {
	s, err := scope(ctx)
	if err != nil {
		return err
	}
	return s.CheckEntries(p)
}

// accountDownload sends what an account downloads: one file as it is, else an archive of
// paths in one of its folders, read through that folder's root.
func accountDownload(ctx echo.Context, access *fileaccess.Scope, format string, list []string) error {
	if len(list) == 1 {
		_, f, err := access.Pin(list[0])
		if err != nil {
			return outsideRoots(ctx, err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DOES_NOT_EXIST, Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST)})
		}
		if !info.IsDir() {
			name := path.Base(list[0])
			ctx.Response().Header().Set(echo.HeaderContentDisposition, "attachment; filename*=utf-8''"+url.PathEscape(name))
			http.ServeContent(ctx.Response(), ctx.Request(), name, info.ModTime(), f)
			return nil
		}
	}

	root, folder, _, err := access.Root(fileaccess.Read, list[0], false)
	if err != nil {
		return outsideRoots(ctx, err)
	}
	defer root.Close()
	rels, full := make([]string, len(list)), make([]string, len(list))
	for i, p := range list {
		if err := access.Check(fileaccess.Read, &p); err != nil {
			return outsideRoots(ctx, err)
		}
		f, rel := access.FolderOf(p)
		if f != folder { // one archive, one folder: what the screen selects is in one
			return outsideRoots(ctx, fileaccess.ErrPathNotRooted)
		}
		rels[i], full[i] = rel, p
	}
	extension, arch, err := cfile.GetCompressionAlgorithm(format)
	if err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	commonDir := file.CommonPrefix(filepath.Separator, full...)
	commonRel, err := filepath.Rel(folder, commonDir)
	if err != nil || !filepath.IsLocal(commonRel) && commonRel != "." {
		return outsideRoots(ctx, fileaccess.ErrPathNotRooted)
	}

	h := ctx.Response().Header()
	h.Set(echo.HeaderContentType, "application/octet-stream")
	h.Set(echo.HeaderContentDisposition, "attachment; filename*=utf-8''"+url.PathEscape("_"+filepath.Base(commonDir)+extension))
	h.Set("Cache-Control", "no-cache")
	err = cfile.WriteArchiveIn(ctx.Request().Context(), ctx.Response(), arch, root, folder, filepath.ToSlash(commonRel), rels)
	if err == nil {
		return nil
	}
	if !ctx.Response().Committed {
		h.Del(echo.HeaderContentType)
		h.Del(echo.HeaderContentDisposition)
		h.Del("Cache-Control")
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	log.Printf("Failed to archive %v: %v", list, err)
	panic(http.ErrAbortHandler)
}

// accountDelete deletes for an account, through the root of its folder. The trash, which
// keeps what every account deleted, is the owner's: a move to it is refused as the trash
// refuses a disk it cannot take, and the dashboard offers the permanent delete instead.
func accountDelete(ctx echo.Context, paths []string) error {
	if ctx.QueryParam("to_trash") == "true" {
		refused := make([]trashRefused, len(paths))
		for i, p := range paths {
			refused[i] = trashRefused{Path: p, Reason: "The trash is the owner's: this account deletes for good."}
		}
		return ctx.JSON(http.StatusConflict, model.Result{Success: common_err.TRASH_UNAVAILABLE, Message: common_err.GetMsg(common_err.TRASH_UNAVAILABLE), Data: refused})
	}
	for _, p := range paths {
		root, rel, err := rooted(ctx, fileaccess.Write, &p, true)
		if err != nil {
			return outsideRoots(ctx, err)
		}
		if service.IsMounted(p) || file.CheckNoMountPoint(p) != nil {
			root.Close()
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.MOUNTED_DIRECTIORIES, Message: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES), Data: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES)})
		}
		err = root.RemoveAll(rel)
		root.Close()
		if err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DELETE_ERROR, Message: common_err.GetMsg(common_err.FILE_DELETE_ERROR), Data: err.Error()})
		}
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// errNotMeasured: a size the account may not ask for.
var errNotMeasured = errors.New("not measured")

// measured is the size of p: for an account through the root of its folder, links not
// followed; for the owner, by its path as before.
func measured(ctx echo.Context, p string) (int64, error) {
	s, err := scope(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errNotMeasured, err)
	}
	if !s.User() {
		if err := s.Check(fileaccess.Read, &p); err != nil {
			return 0, fmt.Errorf("%w: %w", errNotMeasured, err)
		}
		return file.GetFileOrDirSize(p)
	}
	root, _, rel, err := s.Root(fileaccess.Read, p, false)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errNotMeasured, err)
	}
	defer root.Close()
	return file.SizeIn(root, rel)
}

// accountListing is what the folder p holds, read through the root of the account's
// folder: an entry is measured as it is, and a link is a folder only when it leads to one
// inside that folder.
func accountListing(access *fileaccess.Scope, p string) ([]model.Path, error) {
	root, _, rel, err := access.Root(fileaccess.Read, p, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	out := make([]model.Path, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue // gone since it was listed
		}
		entry := model.Path{Name: e.Name(), Path: filepath.Join(p, e.Name()), IsDir: e.IsDir(), Date: info.ModTime(), Size: info.Size()}
		if info.Mode()&fs.ModeSymlink != 0 {
			if target, err := root.Stat(path.Join(rel, e.Name())); err == nil {
				entry.IsDir = target.IsDir()
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
