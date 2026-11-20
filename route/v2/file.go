package v2

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ReCasaOS/CasaOS/codegen"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// Path: route/v2/file.go

func (s *CasaOS) GetFileTest(ctx echo.Context) error {

	//http.ServeFile(w, r, r.URL.Path[1:])
	http.ServeFile(ctx.Response().Writer, ctx.Request(), "/DATA/test.img")

	return ctx.String(200, "pong")
}

// uploadScope is what this upload may reach: the box's roots, or an account's writable
// folders (pkg/fileaccess).
func uploadScope(ctx echo.Context) (*fileaccess.Scope, error) {
	internal := external.IsInternalRequest(ctx.RealIP(), ctx.Request().Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath)
	return fileaccess.For(config.CommonInfo.RuntimePath, ctx.Request().Header.Get("user_id"), internal)
}

func (c *CasaOS) CheckUploadChunk(ctx echo.Context, params codegen.CheckUploadChunkParams) error {
	identifier := ctx.QueryParam("identifier")
	chunkNumber, err := strconv.ParseInt(ctx.QueryParam("chunkNumber"), 10, 64)
	if err != nil {
		return ctx.NoContent(http.StatusBadRequest)
	}

	// the folder as PostUploadFile reads it: an upload is the identifier and the file it lands in
	path := ctx.QueryParam("path")
	if access, err := uploadScope(ctx); err != nil || access.Check(fileaccess.Write, &path) != nil {
		return ctx.NoContent(http.StatusNoContent)
	}
	err = c.fileUploadService.TestChunk(ctx, identifier, path+"/"+ctx.QueryParam("relativePath"), chunkNumber)
	if err != nil {
		return ctx.NoContent(http.StatusNoContent)
	}
	return ctx.NoContent(http.StatusOK)
}

func (c *CasaOS) PostUploadFile(ctx echo.Context) error {
	path := ctx.FormValue("path")
	relativePath := ctx.FormValue("relativePath")
	// [file] ManagementRoots: the folder, the file it lands in and the `.tmp`
	// it is written to first (pkg/fileroots)
	target, temp := path+"/"+relativePath, path+"/"+relativePath+".tmp"
	access, err := uploadScope(ctx)
	if err == nil {
		err = access.Check(fileaccess.Write, &path, &target, &temp)
	}
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, fileaccess.ErrRoleUnknown) {
			status = http.StatusServiceUnavailable // asked again shortly
		}
		// message and data, as the v1 file API answers (the dashboard shows data)
		return ctx.JSON(status, map[string]string{"message": err.Error(), "data": err.Error()})
	}

	// handle the request
	chunkNumber, err := strconv.ParseInt(ctx.FormValue("chunkNumber"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	chunkSize, err := strconv.ParseInt(ctx.FormValue("chunkSize"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	currentChunkSize, err := strconv.ParseInt(ctx.FormValue("currentChunkSize"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	totalChunks, err := strconv.ParseInt(ctx.FormValue("totalChunks"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	totalSize, err := strconv.ParseInt(ctx.FormValue("totalSize"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}

	identifier := ctx.FormValue("identifier")
	fileName := ctx.FormValue("filename")
	bin, err := ctx.FormFile("file")

	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}

	err = c.fileUploadService.UploadFile(
		ctx,
		path,
		chunkNumber,
		chunkSize,
		currentChunkSize,
		totalChunks,
		totalSize,
		identifier,
		relativePath,
		fileName,
		bin,
	)
	if errors.Is(err, service.ErrInvalidChunk) {
		return ctx.JSON(http.StatusBadRequest, err.Error())
	}
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, err)
	}
	return ctx.NoContent(http.StatusOK)
}
