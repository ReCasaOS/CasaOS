package v1

import (
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"github.com/ReCasaOS/CasaOS/pkg/filesearch"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// SearchResp is the hits of a search of Files, and whether a limit stopped it early.
type SearchResp struct {
	Content   []ObjResp `json:"content"`
	Truncated bool      `json:"truncated"`
}

// SearchFiles finds the files and folders whose name holds q (2 to 200 characters, case not
// minded) under path, "/" when absent: under a folder within the roots, or, from a folder
// above them ("/" for the dashboard's Root), under each root it leads to. Names only, links
// not followed, at most filesearch.DefaultLimits (CasaOS#184). The owner's: an account is
// refused, as the Gateway refuses it the route.
//
// @Summary search Files by name
// @Produce application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path query string false "where to search"
// @Param q query string true "part of a name"
// @Success 200 {object} model.Result
// @Router /file/search [get]
func SearchFiles(ctx echo.Context) error {
	query := strings.TrimSpace(ctx.QueryParam("q"))
	if n := utf8.RuneCountInString(query); n < 2 || len(query) > 200 {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: "the search needs 2 to 200 characters"})
	}
	path := ctx.QueryParam("path")
	if path == "" {
		path = "/"
	}
	access, err := scope(ctx)
	if err != nil {
		return outsideRoots(ctx, err)
	}
	if access.User() {
		return outsideRoots(ctx, fileaccess.ErrPathNotRooted)
	}

	var starts []string
	if roots := access.Listing(); roots.IsAncestor(path) {
		starts = rootsUnder(filepath.Clean(path), roots.Paths())
	} else {
		if err := managed(ctx, fileaccess.Read, &path); err != nil {
			return outsideRoots(ctx, err)
		}
		starts = []string{path}
	}

	found := filesearch.Search(ctx.Request().Context(), starts, query, filesearch.DefaultLimits)
	resp := SearchResp{Content: make([]ObjResp, 0, len(found.Hits)), Truncated: found.Truncated}
	for _, h := range found.Hits {
		resp.Content = append(resp.Content, ObjResp{Name: h.Name, Path: h.Path, IsDir: h.IsDir, Size: h.Size, Date: h.Date, Modified: h.Date})
	}

	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: resp})
}

// rootsUnder is the roots at or below dir, without one that lies inside another (walking the
// outer one reads it already).
func rootsUnder(dir string, roots []string) []string {
	under := []string{}
	for _, r := range roots {
		if dir == "/" || r == dir || strings.HasPrefix(r, dir+"/") {
			under = append(under, r)
		}
	}
	out := []string{}
	for _, r := range under {
		inside := false
		for _, other := range under {
			if other != r && strings.HasPrefix(r, other+"/") {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, r)
		}
	}
	return out
}
