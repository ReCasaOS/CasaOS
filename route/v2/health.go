package v2

import (
	"net/http"
	"net/url"
	"path/filepath"

	cfile "github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS/codegen"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

func (s *CasaOS) GetHealthServices(ctx echo.Context) error {
	services, err := service.MyService.Health().Services()
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	return ctx.JSON(http.StatusOK, codegen.GetHealthServicesOK{
		Data: &codegen.HealthServices{
			Running:    services[true],
			NotRunning: services[false],
		},
	})
}

func (s *CasaOS) GetHealthPorts(ctx echo.Context) error {
	tcpPorts, udpPorts, err := service.MyService.Health().Ports()
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	return ctx.JSON(http.StatusOK, codegen.GetHealthPortsOK{
		Data: &codegen.HealthPorts{
			TCP: &tcpPorts,
			UDP: &udpPorts,
		},
	})
}
func (c *CasaOS) GetHealthlogs(ctx echo.Context) error {
	const logDir = "/var/log/casaos"
	extension, format, err := cfile.GetCompressionAlgorithm("zip")
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	h := ctx.Response().Header()
	h.Set(echo.HeaderContentType, "application/octet-stream")
	h.Set(echo.HeaderContentDisposition, "attachment; filename*=utf-8''"+url.PathEscape(filepath.Base(logDir)+extension))
	h.Set("Cache-Control", "no-cache")

	err = cfile.WriteArchive(ctx.Request().Context(), ctx.Response(), format, logDir, []string{logDir})
	if err == nil {
		return nil
	}
	if !ctx.Response().Committed {
		// Nothing sent yet: answer with a JSON error, not a file to save.
		h.Del(echo.HeaderContentType)
		h.Del(echo.HeaderContentDisposition)
		h.Del("Cache-Control")
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}
	// Part of the archive is out: abort the connection so the browser reports a
	// failed download instead of saving a truncated file.
	panic(http.ErrAbortHandler)
}
