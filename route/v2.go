package route

import (
	"crypto/ecdsa"
	"log"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS/codegen"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"

	"github.com/ReCasaOS/CasaOS-Common/external"
	cfile "github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	v2Route "github.com/ReCasaOS/CasaOS/route/v2"
	"github.com/deepmap/oapi-codegen/pkg/middleware"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
)

var (
	_swagger *openapi3.T

	V2APIPath  string
	V2DocPath  string
	V3FilePath string
)

func init() {
	swagger, err := codegen.GetSwagger()
	if err != nil {
		panic(err)
	}

	_swagger = swagger

	u, err := url.Parse(_swagger.Servers[0].URL)
	if err != nil {
		panic(err)
	}

	V2APIPath = strings.TrimRight(u.Path, "/")
	V2DocPath = "/doc" + V2APIPath
	V3FilePath = "/v3/file"
}

func InitV2Router() http.Handler {
	appManagement := v2Route.NewCasaOS()

	e := echo.New()

	e.Use((echo_middleware.CORSWithConfig(echo_middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{echo.POST, echo.GET, echo.OPTIONS, echo.PUT, echo.DELETE},
		AllowHeaders:     []string{echo.HeaderAuthorization, echo.HeaderContentLength, echo.HeaderXCSRFToken, echo.HeaderContentType, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders, echo.HeaderAccessControlAllowMethods, echo.HeaderConnection, echo.HeaderOrigin, echo.HeaderXRequestedWith},
		ExposeHeaders:    []string{echo.HeaderContentLength, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders},
		MaxAge:           172800,
		AllowCredentials: true,
	})))

	e.Use(echo_middleware.Gzip())

	e.Use(echo_middleware.Logger())

	e.Use(echojwt.WithConfig(echojwt.Config{
		Skipper: func(c echo.Context) bool {
			return external.IsInternalRequest(c.RealIP(), c.Request().Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath)
		},
		ParseTokenFunc: func(c echo.Context, token string) (interface{}, error) {
			valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
			if err != nil || !valid {
				return nil, echo.ErrUnauthorized
			}
			c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

			return claims, nil
		},
		TokenLookupFuncs: []echo_middleware.ValuesExtractor{
			func(ctx echo.Context) ([]string, error) {
				if len(ctx.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
					return []string{ctx.Request().Header.Get(echo.HeaderAuthorization)}, nil
				}
				return []string{ctx.QueryParam("token")}, nil
			},
		},
	}))

	// e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
	// 	return func(c echo.Context) error {
	// 		switch c.Request().Header.Get(echo.HeaderContentType) {
	// 		case common.MIMEApplicationYAML: // in case request contains a compose content in YAML
	// 			return middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
	// 				Options: openapi3filter.Options{
	// 					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	// 					// ExcludeRequestBody:  true,
	// 					// ExcludeResponseBody: true,
	// 				},
	// 			})(next)(c)

	// 		default:
	// 			return middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
	// 				Options: openapi3filter.Options{
	// 					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	// 				},
	// 			})(next)(c)
	// 		}
	// 	}
	// })

	e.Use(middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
		Skipper: skipRequestValidation,
		Options: openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))

	codegen.RegisterHandlersWithBaseURL(e, appManagement, V2APIPath)

	return e
}

// skipRequestValidation exempts file uploads, which cannot pass validation
// (https://github.com/deepmap/oapi-codegen/issues/514).
func skipRequestValidation(c echo.Context) bool {
	return strings.Contains(c.Request().Header.Get(echo.HeaderContentType), "multipart/form-data")
}

func InitV2DocRouter(docHTML string, docYAML string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == V2DocPath {
			if _, err := w.Write([]byte(docHTML)); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}

		if r.URL.Path == V2DocPath+"/openapi.yaml" {
			if _, err := w.Write([]byte(docYAML)); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}
	})
}

func InitFile() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if len(token) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "token not found"}`))
			return
		}

		valid, _, errs := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
		if errs != nil || !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "validation failure"}`))
			return
		}
		filePath := r.URL.Query().Get("path")
		fileName := path.Base(filePath)
		w.Header().Add("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(fileName))
		http.ServeFile(w, r, filePath)
		// http.ServeFile(w, r, filePath)
	})
}

func InitDir() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if len(token) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "token not found"}`))
			return
		}

		valid, _, errs := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
		if errs != nil || !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "validation failure"}`))
			return
		}
		t := r.URL.Query().Get("format")
		files := r.URL.Query().Get("files")

		if len(files) == 0 {
			// w.JSON(common_err.CLIENT_ERROR, model.Result{
			// 	Success: common_err.INVALID_PARAMS,
			// 	Message: common_err.GetMsg(common_err.INVALID_PARAMS),
			// })
			return
		}
		list := strings.Split(files, ",")
		for _, v := range list {
			if !file.Exists(v) {
				// return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
				// 	Success: common_err.FILE_DOES_NOT_EXIST,
				// 	Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
				// })
				return
			}
		}
		// handles only single files not folders and multiple files
		//		if len(list) == 1 {

		// filePath := list[0]
		//			info, err := os.Stat(filePath)
		//			if err != nil {

		// w.JSON(http.StatusOK, model.Result{
		// 	Success: common_err.FILE_DOES_NOT_EXIST,
		// 	Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
		// })
		//return
		//			}
		//}

		extension, format, err := cfile.GetCompressionAlgorithm(t)
		if err != nil {
			// w.JSON(common_err.CLIENT_ERROR, model.Result{
			// 	Success: common_err.INVALID_PARAMS,
			// 	Message: common_err.GetMsg(common_err.INVALID_PARAMS),
			// })
			return
		}
		commonDir := file.CommonPrefix(filepath.Separator, list...)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape("_"+filepath.Base(commonDir)+extension))
		w.Header().Set("Cache-Control", "no-cache")
		// The request context ends when the client goes away, which stops the walk and the copy.
		if err := cfile.WriteArchive(r.Context(), w, format, commonDir, list); err != nil {
			log.Printf("Failed to archive %v: %v", list, err)
			// Abort the connection so the browser reports a failed download
			// instead of saving a truncated file.
			panic(http.ErrAbortHandler)
		}
	})
}
