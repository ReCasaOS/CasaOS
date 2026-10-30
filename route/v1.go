package route

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	v1 "github.com/ReCasaOS/CasaOS/route/v1"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
)

// skipJWT reports whether the JWT check can be skipped for a request: only
// for one of this box's own services, which come from loopback with the secret
// the gateway wrote for this boot. Loopback alone used to be enough for most of
// the API, with a list of root-privileged routes excluded by hand; but any
// local process reaches 127.0.0.1 -- a container CasaOS itself started on the
// host network, any local account -- and every route here acts as root, the
// file manager on any path. The dashboard always sends a token; the services
// send the secret through CasaOS-Common's clients; nothing else is expected.
func skipJWT(realIP, authorization string) bool {
	return external.IsInternalRequest(realIP, authorization, config.CommonInfo.RuntimePath)
}

// internalNotifyPrefix is where CasaOS-LocalStorage POSTs sys_disk/sys_usb
// every 5s (misc.go sendStorageStats). Logging each call floods journald
// (issue #2211), so loopback callers of these routes skip the access log.
const internalNotifyPrefix = "/v1/notify/"

func isLoopback(realIP string) bool {
	return realIP == "::1" || realIP == "127.0.0.1"
}

// skipAccessLog reports whether the Echo access-log line is dropped for a
// request: only internal notify posts, and only from the host itself.
func skipAccessLog(path, realIP string) bool {
	return strings.HasPrefix(path, internalNotifyPrefix) && isLoopback(realIP)
}

func InitV1Router() http.Handler {
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
	e.Use(echo_middleware.Recover())
	e.Use(echo_middleware.LoggerWithConfig(echo_middleware.LoggerConfig{
		Format: accessLogFormat,
		Skipper: func(c echo.Context) bool {
			// The registered route is /v1/notify/:path, so match on the request
			// path rather than c.Path().
			return skipAccessLog(c.Request().URL.Path, c.RealIP())
		},
	}))

	e.GET("/v1/sys/version/check", v1.GetSystemCheckVersion)
	e.GET("/v1/sys/version/current", func(ctx echo.Context) error {
		return ctx.String(200, common.VERSION)
	})
	e.GET("/ping", func(ctx echo.Context) error {
		return ctx.String(200, "pong")
	})
	e.GET("/v1/recover/:type", v1.GetRecoverStorage)
	v1Group := e.Group("/v1")
	//	e.Any("/v1/test", v1.CheckNetwork)
	v1Group.Use(echojwt.WithConfig(echojwt.Config{
		Skipper: func(c echo.Context) bool {
			return skipJWT(c.RealIP(), c.Request().Header.Get(echo.HeaderAuthorization))
		},
		ParseTokenFunc: func(c echo.Context, token string) (interface{}, error) {
			claims, err := accessClaims(token)
			if err != nil {
				return nil, err
			}

			c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

			return claims, nil
		},
		TokenLookupFuncs: []echo_middleware.ValuesExtractor{lookupToken},
	}))
	{

		v1SysGroup := v1Group.Group("/sys")
		v1SysGroup.Use()
		{
			// The bug-report template: OS, version, disks, the configuration.
			// It sat outside the group, readable by anyone on the network.
			v1SysGroup.GET("/debug", v1.GetSystemConfigDebug)
			v1SysGroup.GET("/version", v1.GetSystemCheckVersion) // version/check
			registerHostManagement(v1SysGroup)

			// The modules of this release: what can be installed, and the install and the remove, which start a unit as root and so take
			// the access token in the header and no other, as the upgrade's calls do. The list and the status are read-only and take what
			// the group takes. The name in the address is checked before anything else. On an image-managed host the system image's
			// modules (service.ModuleProvider).
			v1SysGroup.GET("/modules", v1.GetSystemModules)
			v1SysGroup.POST("/modules/:name/plan", v1.PlanSystemModule, headerTokenOnly)
			v1SysGroup.POST("/modules/:name/install", v1.InstallSystemModule, headerTokenOnly)
			v1SysGroup.POST("/modules/:name/remove", v1.RemoveSystemModule, headerTokenOnly)
			v1SysGroup.GET("/modules/:name/status", v1.GetSystemModuleStatus)

			v1SysGroup.GET("/hardware", v1.GetSystemHardwareInfo) // hardware/info

			v1SysGroup.GET("/wsssh", v1.WsSsh)
			v1SysGroup.POST("/ssh-login", v1.PostSshLogin)
			// v1SysGroup.GET("/config", v1.GetSystemConfig) //delete
			// v1SysGroup.POST("/config", v1.PostSetSystemConfig)
			v1SysGroup.GET("/logs", v1.GetCasaOSErrorLogs) // error/logs
			// v1SysGroup.GET("/widget/config", v1.GetWidgetConfig)//delete
			// v1SysGroup.POST("/widget/config", v1.PostSetWidgetConfig)//delete

			v1SysGroup.GET("/utilization", v1.GetSystemUtilization)
			// v1SysGroup.GET("/cpu", v1.GetSystemCupInfo)
			// v1SysGroup.GET("/mem", v1.GetSystemMemInfo)
			// v1SysGroup.GET("/disk", v1.GetSystemDiskInfo)
			// v1SysGroup.GET("/network", v1.GetSystemNetInfo)

			v1SysGroup.GET("/server-info", nil)
			v1SysGroup.PUT("/server-info", nil)
			// v1SysGroup.GET("/port", v1.GetCasaOSPort)
			// v1SysGroup.PUT("/port", v1.PutCasaOSPort)
			v1SysGroup.GET("/proxy", v1.GetSystemProxy)
			v1SysGroup.PUT("/state/:state", v1.PutSystemState)
			v1SysGroup.GET("/entry", v1.GetSystemEntry)
			// Statistics of the machine: the item, its series, its alert rules.
			v1SysGroup.GET("/stats", v1.GetStats)
			v1SysGroup.GET("/stats/:id", v1.GetStatsSeries)
			v1SysGroup.GET("/stats/:id/alerts", v1.GetStatsAlerts)
			v1SysGroup.PUT("/stats/:id/alerts", v1.PutStatsAlerts)
			// Anonymous statistics: their state, the heartbeat as it would be
			// sent, and the switch (dashboard notice and settings).
			v1SysGroup.GET("/telemetry", v1.GetTelemetry)
			v1SysGroup.PUT("/telemetry", v1.PutTelemetry)
			// Push alerts: their channels, categories and disk threshold, and a
			// test message (settings).
			v1SysGroup.GET("/alerts", v1.GetAlerts)
			v1SysGroup.PUT("/alerts", v1.PutAlerts)
			v1SysGroup.POST("/alerts/test", v1.PostAlertsTest)
		}
		v1PortGroup := v1Group.Group("/port")
		v1PortGroup.Use()
		{
			v1PortGroup.GET("/", v1.GetPort)              // app/port
			v1PortGroup.GET("/state/:port", v1.PortCheck) // app/check/:port
		}
		v1FileGroup := v1Group.Group("/file")
		v1FileGroup.Use()
		{
			v1FileGroup.GET("", v1.GetDownloadSingleFile) // download/:path
			v1FileGroup.POST("", v1.PostCreateFile)
			v1FileGroup.PUT("", v1.PutFileContent)
			v1FileGroup.PUT("/name", v1.RenamePath)
			// file/rename
			v1FileGroup.GET("/content", v1.GetFilerContent) // file/read

			// File uploads need to be handled separately, and will not be modified here
			// v1FileGroup.POST("/upload", v1.PostFileUpload)
			v1FileGroup.POST("/upload", v1.PostFileUpload)
			v1FileGroup.GET("/upload", v1.GetFileUpload)
			// v1FileGroup.GET("/download", v1.UserFileDownloadCommonService)
			v1FileGroup.GET("/ws", v1.ConnectWebSocket)
			v1FileGroup.GET("/peers", v1.GetPeers)
			v1FileGroup.GET("/trash", v1.GetTrash)
			v1FileGroup.POST("/trash/restore", v1.RestoreTrash)
			v1FileGroup.DELETE("/trash", v1.DeleteTrash)
			v1FileGroup.DELETE("/trash/all", v1.EmptyTrash)
			v1FileGroup.GET("/roots", v1.GetFileRoots)
		}
		v1CloudGroup := v1Group.Group("/cloud")
		v1CloudGroup.Use()
		{
			v1CloudGroup.GET("", v1.ListStorages)
			v1CloudGroup.DELETE("", v1.UmountStorage)
		}
		v1DriverGroup := v1Group.Group("/driver")
		v1DriverGroup.Use()
		{
			v1DriverGroup.GET("", v1.ListDriverInfo)
		}

		v1FolderGroup := v1Group.Group("/folder")
		v1FolderGroup.Use()
		{
			v1FolderGroup.PUT("/name", v1.RenamePath)
			v1FolderGroup.GET("", v1.DirPath)   ///file/dirpath
			v1FolderGroup.POST("", v1.MkdirAll) ///file/mkdir
			v1FolderGroup.GET("/size", v1.GetSize)
			v1FolderGroup.GET("/count", v1.GetFileCount)
		}
		v1BatchGroup := v1Group.Group("/batch")
		v1BatchGroup.Use()
		{

			v1BatchGroup.DELETE("", v1.DeleteFile) // file/delete
			v1BatchGroup.DELETE("/:id/task", v1.DeleteOperateFileOrDir)
			v1BatchGroup.POST("/task", v1.PostOperateFileOrDir) // file/operate
			v1BatchGroup.GET("", v1.GetDownloadFile)
		}
		v1ImageGroup := v1Group.Group("/image")
		v1ImageGroup.Use()
		{
			v1ImageGroup.GET("", v1.GetFileImage)
		}
		v1SambaGroup := v1Group.Group("/samba")
		v1SambaGroup.Use()
		{
			v1ConnectionsGroup := v1SambaGroup.Group("/connections")
			v1ConnectionsGroup.Use()
			{
				v1ConnectionsGroup.GET("", v1.GetSambaConnectionsList)
				v1ConnectionsGroup.POST("", v1.PostSambaConnectionsCreate)
				v1ConnectionsGroup.DELETE("/:id", v1.DeleteSambaConnections)
			}
			v1SharesGroup := v1SambaGroup.Group("/shares")
			v1SharesGroup.Use()
			{
				v1SharesGroup.GET("", v1.GetSambaSharesList)
				v1SharesGroup.POST("", v1.PostSambaSharesCreate)
				v1SharesGroup.PUT("/:id", v1.PutSambaShare)
				v1SharesGroup.DELETE("/:id", v1.DeleteSambaShares)
				v1SharesGroup.GET("/status", v1.GetSambaStatus)
			}

			v1SambaUsersGroup := v1SambaGroup.Group("/users")
			v1SambaUsersGroup.Use()
			{
				v1SambaUsersGroup.GET("", v1.GetSambaUsersList)
				v1SambaUsersGroup.POST("", v1.PostSambaUserCreate)
				v1SambaUsersGroup.PUT("/:username/password", v1.PutSambaUserPassword)
				v1SambaUsersGroup.DELETE("/:username", v1.DeleteSambaUser)
			}
		}
		v1NotifyGroup := v1Group.Group("/notify")
		v1NotifyGroup.Use()
		{
			v1NotifyGroup.POST("/:path", v1.PostNotifyMessage)
			// merge to system
			v1NotifyGroup.POST("/system_status", v1.PostSystemStatusNotify)
		}

		v1OtherGroup := v1Group.Group("/other")
		v1OtherGroup.Use()
		{
			v1OtherGroup.GET("/search", v1.GetSearchResult)
		}
		v1ZerotierGroup := v1Group.Group("/zt")
		v1ZerotierGroup.Use()
		{
			v1ZerotierGroup.Any("/*url", v1.ZerotierProxy)
		}
	}

	return e
}

// registerHostManagement: what changes the host itself — the OS upgrade, the
// release update and automatic updates, apt and Docker packages, the core's own
// stop. An image-managed host ([server] ImageManaged) gets none of these routes:
// 404, as from a core without the feature, so the dashboard shows none of it
// (its rows appear only where the core answers). Modules are not here: their
// routes stay, answered by the image's provider (service.ModuleProvider).
func registerHostManagement(v1SysGroup *echo.Group) {
	if config.ServerInfo.ImageManaged {
		return
	}

	// The operating system and how long its security updates last: it only reads.
	v1SysGroup.GET("/os", v1.GetSystemOS)
	// Whether this machine could be upgraded to the next release, and why not: a report, that
	// only reads the machine. It takes up to twenty seconds.
	v1SysGroup.GET("/os/readiness", v1.GetSystemOSReadiness)
	// The plan of the upgrade to the next release: what apt would do, planned in a sandbox with the
	// machine unchanged. Starting one runs apt-get update against a temporary copy of the sources, as
	// the update of Docker runs apt: the token goes in the header, and is not the refresh token. The
	// runbook has the lines of the box's own sources as the files have them (a URL's password among
	// them): the same. The plan itself is read-only and takes what the group takes.
	v1SysGroup.POST("/os/upgrade/plan", v1.StartSystemOSPlan, headerTokenOnly)
	v1SysGroup.GET("/os/upgrade/plan", v1.GetSystemOSPlan)
	v1SysGroup.GET("/os/upgrade/runbook", v1.GetSystemOSRunbook, headerTokenOnly)
	// The upgrade itself, which the plan prepares. Its status is read-only and takes what the group takes; every call that
	// acts, or that switches the feature, takes the access token in the header and no other, as the plan's start does, and
	// the runbook of the run that is open (?run=current) is the same route as the plan's: it holds the owner's sources. All
	// of them but the refresh, the start and the switch work with the feature off, so that a machine that is half upgraded is never
	// trapped behind it.
	v1SysGroup.GET("/os/upgrade/status", v1.GetSystemOSUpgradeStatus)
	v1SysGroup.POST("/os/upgrade/refresh", v1.StartSystemOSUpgradeRefresh, headerTokenOnly)
	v1SysGroup.POST("/os/upgrade/start", v1.StartSystemOSUpgrade, headerTokenOnly)
	v1SysGroup.POST("/os/upgrade/reboot", v1.RebootSystemOSUpgrade, headerTokenOnly)
	v1SysGroup.POST("/os/upgrade/resume", v1.ResumeSystemOSUpgrade, headerTokenOnly)
	v1SysGroup.POST("/os/upgrade/abort", v1.AbortSystemOSUpgrade, headerTokenOnly)
	v1SysGroup.POST("/os/upgrade/ack", v1.AckSystemOSUpgrade, headerTokenOnly)
	v1SysGroup.POST("/os/upgrade/beta", v1.SetSystemOSUpgradeBeta, headerTokenOnly)

	v1SysGroup.POST("/update", v1.SystemUpdate, headerTokenOnly)
	v1SysGroup.GET("/packages", v1.GetSystemPackageUpdates)
	v1SysGroup.POST("/packages/update", v1.StartSystemPackageUpdate)
	v1SysGroup.GET("/packages/update/status", v1.GetSystemPackageUpdateStatus)
	v1SysGroup.GET("/docker/containers", v1.GetSystemDockerContainers)
	// Updating Docker stops every container: the token goes in the header, and is not the
	// refresh token. The status is read-only and takes what the group takes.
	v1SysGroup.POST("/docker/update", v1.StartDockerUpdate, headerTokenOnly)
	v1SysGroup.GET("/docker/update/status", v1.GetDockerUpdateStatus)

	v1SysGroup.POST("/stop", v1.PostKillCasaOS)

	// Automatic updates: their switch, window and state (settings).
	v1SysGroup.GET("/autoupdate", v1.GetAutoUpdate)
	v1SysGroup.PUT("/autoupdate", v1.PutAutoUpdate, headerTokenOnly)
}
