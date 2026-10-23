//go:generate bash -c "mkdir -p codegen && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/casaos/openapi.yaml > codegen/casaos_api.go"
//go:generate bash -c "mkdir -p codegen/message_bus && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,client -package message_bus https://raw.githubusercontent.com/ReCasaOS/CasaOS-MessageBus/v0.4.19/api/message_bus/openapi.yaml > codegen/message_bus/api.go"
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/model"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/command"
	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"

	util_http "github.com/ReCasaOS/CasaOS-Common/utils/http"

	_ "github.com/ReCasaOS/CasaOS/boot" // (runs `casaos module ...` before the other packages are initialised)
	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/alerts"
	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/cache"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/hoststats"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/sqlite"
	"github.com/ReCasaOS/CasaOS/pkg/telemetry"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/ReCasaOS/CasaOS/route"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/coreos/go-systemd/daemon"
	"github.com/shirou/gopsutil/v3/host"
	"go.uber.org/zap"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const LOCALHOST = "127.0.0.1"

var sqliteDB *gorm.DB

var (
	commit = "private build"
	date   = "private build"

	//go:embed api/index.html
	_docHTML string

	//go:embed api/casaos/openapi.yaml
	_docYAML string

	//go:embed build/sysroot/etc/casaos/casaos.conf.sample
	_confSample string

	configFlag  = flag.String("c", "", "config address")
	dbFlag      = flag.String("db", "", "db path")
	versionFlag = flag.Bool("v", false, "version")
)

func init() {
	// "casaos module ..." is the module assistant: it runs and ends the process before the core reads a flag, opens a log or a database
	if rest, ok := modulecmd.Dispatch(os.Args); ok {
		os.Exit(modulecmd.Run(rest, modulecmd.Production()))
	}
	flag.Parse()
	if *versionFlag {
		fmt.Println("v" + common.VERSION)
		return
	}

	println("git commit:", commit)
	println("build date:", date)

	config.InitSetup(*configFlag, _confSample)

	logger.LogInit(config.AppInfo.LogPath, config.AppInfo.LogSaveName, config.AppInfo.LogFileExt)
	if len(*dbFlag) == 0 {
		*dbFlag = config.AppInfo.DBPath + "/db"
	}

	sqliteDB = sqlite.GetDb(*dbFlag)
	// gredis.GetRedisConn(config.RedisInfo),

	service.MyService = service.NewService(sqliteDB, config.CommonInfo.RuntimePath)

	service.Cache = cache.Init()

	service.GetCPUThermalZone()

	route.InitFunction()

	//service.MyService.System().GenreateSystemEntry()
	///
	//service.MountLists = make(map[string]*mountlib.MountPoint)
	//configfile.Install()
}

// @title casaOS API
// @version 1.0.0
// @contact.name lauren.pan
// @contact.url https://www.zimaboard.com
// @contact.email lauren.pan@icewhale.org
// @description casaOS v1版本api
// @host 192.168.2.217:8089
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization
// @BasePath /v1
func main() {
	if *versionFlag {
		return
	}

	// The plan of the upgrade of the operating system makes a directory in /var/tmp and removes it when it ends. The one that a
	// core did not live to end left its directory behind: those older than ten minutes are removed now, and the end of this
	// core removes the one it has.
	removed, err := service.RemoveStaleOSPlanDirs()
	if err != nil {
		logger.Error("the directories of plans that did not end could not all be removed", zap.Error(err), zap.Strings("removed", removed))
	} else if len(removed) > 0 {
		logger.Info("removed the directories of plans that did not end", zap.Strings("removed", removed))
	}
	go stopThePlanWhenTheCoreIsToldToStop()

	// The installer's --no-telemetry leaves a marker: fold it into
	// telemetry.json before the API serves and before the first check. If it
	// cannot be folded, the checks do not start: an opt-out is never lost.
	if err := telemetry.Default.ApplyOffMarker(); err != nil {
		logger.Error("anonymous statistics stay off: cannot apply the telemetry-off marker", zap.Error(err))
	} else {
		go telemetry.Default.Run(context.Background())
	}
	// Push alerts end with the dashboard's address, look for a newer release
	// with the update button's own check while automatic updates are off, and
	// hear from automatic updates how each attempt ended: set before those
	// settle the attempt that restarted the core.
	alerts.Default.Address = dashboardAddress
	alerts.Default.Latest = func() string { return service.MyService.Casa().GetCasaosVersion().Version }
	alerts.Default.AutoUpdates = func() bool { return autoupdate.Default.Status().Enabled }
	autoupdate.Default.Notify = alerts.Default.AutoUpdate
	// The update of Docker stops every container, and each app's watch says so: those events are
	// dropped while it runs and for ten minutes after. When a run ends the owner is told once, from
	// the run's own log and systemd, so with the dashboard closed and across a restart of the core:
	// the alerts look when the core starts (a run that ended or goes on) and when the button
	// starts a run. Set before Run, which starts the first look.
	alerts.Default.Muted = func() bool {
		system := service.MyService.System()
		// an upgrade of the operating system stops and starts the containers as well, for as long as it holds the machine and ten
		// minutes after it lets go of it
		return system.DockerUpdateMuted() || system.OSUpgradeMuted()
	}
	alerts.Default.LastDockerRun = lastDockerRun
	service.MyService.System().OnDockerUpdateQueued(alerts.Default.WatchDockerUpdate)
	// What became of an upgrade of the operating system, and that it waits for the owner, is told through the same alerts, once for
	// each thing (the core keeps its own record, which survives a restart). Set before the loop that looks at the upgrade starts.
	service.MyService.System().SetOSUpgradeNotifier(func(key, sentence string) { alerts.Default.OSUpgradeNotice(key, sentence) })
	// An operating system whose security updates are about to end, or have ended, is told once for
	// each state: the alerts look at what the dashboard's badge reads, five minutes after the start
	// and every day.
	alerts.Default.OSSupport = service.MyService.System().GetSystemOS
	go alerts.Default.Run(context.Background())
	// Automatic updates start the button's own update, read the button's own
	// version.json, and settle an update that restarted the core before checking.
	autoupdate.Default.Releases = service.MyService.Casa()
	autoupdate.Default.Start = func(release string) error {
		err := service.MyService.System().UpdateSystemVersion(release)
		if errors.Is(err, service.ErrSystemMaintenanceBusy) {
			return fmt.Errorf("%w: %v", autoupdate.ErrBusy, err)
		}
		return err
	}
	autoupdate.Default.Busy = service.MyService.System().MaintenanceBusy
	// The update of Docker stops every container: it waits for the apps that AppManagement says
	// are being installed, updated, backed up or restored, and does not start when it cannot ask.
	service.MyService.System().SetAppOperations(func(ctx context.Context) ([]string, error) {
		operations, err := autoupdate.AppOperations(ctx, config.CommonInfo.RuntimePath)
		if err != nil {
			return nil, err
		}
		apps := make([]string, 0, len(operations))
		for _, operation := range operations {
			apps = append(apps, operation.App)
		}
		return apps, nil
	})
	go autoupdate.Default.Run(context.Background())
	v1Router := route.InitV1Router()

	v2Router := route.InitV2Router()
	v2DocRouter := route.InitV2DocRouter(_docHTML, _docYAML)
	v3File := route.InitFile()
	mux := &util_http.HandlerMultiplexer{
		HandlerMap: map[string]http.Handler{
			"v1":  v1Router,
			"v2":  v2Router,
			"v3":  v3File,
			"doc": v2DocRouter,
		},
	}

	crontab := cron.New(cron.WithSeconds())
	if _, err := crontab.AddFunc("@every 5s", route.SendAllHardwareStatusBySocket); err != nil {
		logger.Error("add crontab error", zap.Error(err))
	}

	crontab.Start()
	defer crontab.Stop()

	listener, err := net.Listen("tcp", net.JoinHostPort(LOCALHOST, "0"))
	if err != nil {
		panic(err)
	}
	routers := []string{
		"/v1/sys",
		"/v1/port",
		"/v1/file",
		"/v1/folder",
		"/v1/batch",
		"/v1/image",
		"/v1/samba",
		"/v1/notify",
		"/v1/driver",
		"/v1/cloud",
		"/v1/recover",
		"/v1/other",
		"/v1/zt",
		"/v1/test",
		route.V2APIPath,
		route.V2DocPath,
		route.V3FilePath,
	}
	for _, apiPath := range routers {
		err = service.RetryGateway(func() error {
			return service.MyService.Gateway().CreateRoute(&model.Route{
				Path:   apiPath,
				Target: "http://" + listener.Addr().String(),
			})
		})
		if err != nil {
			fmt.Println("err", err)
			panic(err)
		}
	}

	// register at message bus: once whatever the time, then again until the start's deadline
	for i := 0; i < 10 && (i == 0 || time.Now().Before(service.StartDeadline)); i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		response, err := service.MyService.MessageBus().RegisterEventTypesWithResponse(ctx, common.EventTypes)
		cancel()
		if err != nil {
			logger.Error("error when trying to register one or more event types - some event type will not be discoverable", zap.Error(err))
		}
		if response != nil && response.StatusCode() != http.StatusOK {
			logger.Error("error when trying to register one or more event types - some event type will not be discoverable", zap.String("status", response.Status()), zap.String("body", string(response.Body)))
		}
		// nil when the bus could not be reached at all -- which is what a service
		// starting a second before the bus sees, and what used to be a nil pointer
		// panic on the first start of every fresh install
		if response != nil && response.StatusCode() == http.StatusOK {
			break
		}
		time.Sleep(time.Second)
	}

	// The host's statistics start after the event types are registered (building the service can
	// emit alerts rebuilt from its history), so after the alerts (their Handle exists) and before
	// the cron push, which is the order the registration loop leaves. Without them the core goes on: the routes answer 503. On a stop the
	// sampler's context ends first and the service is closed after it, never while it ticks.
	statsCtx, stopStats := context.WithCancel(context.Background())
	defer stopStats()
	if svc, err := hoststats.New(hostStatsConfig()); err != nil {
		logger.Error("host statistics are off", zap.Error(err))
	} else {
		hoststats.Default = svc
		running := make(chan struct{})
		go func() { svc.Run(statsCtx); close(running) }()
		stop := func() {
			stopStats()
			<-running
			if err := svc.Close(); err != nil {
				logger.Error("host statistics did not close cleanly", zap.Error(err))
			}
		}
		stopHostStats.Store(&stop)
	}

	urlFilePath := filepath.Join(config.CommonInfo.RuntimePath, "casaos.url")
	if err := file.CreateFileAndWriteContent(urlFilePath, "http://"+listener.Addr().String()); err != nil {
		logger.Error("error when creating address file", zap.Error(err),
			zap.Any("address", listener.Addr().String()),
			zap.Any("filepath", urlFilePath),
		)
	}

	if supported, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		logger.Error("Failed to notify systemd that casaos main service is ready", zap.Any("error", err))
	} else if supported {
		logger.Info("Notified systemd that casaos main service is ready")
	} else {
		logger.Info("This process is not running as a systemd service.")
	}
	// The loop that looks at an upgrade of the operating system that is open, and does the one thing that is next, starts when the
	// core is ready to serve and not before: a look may start the unit of the upgrade. It looks at once, and then every half
	// minute; with no upgrade on the machine it does nothing that can be seen.
	service.MyService.System().StartSystemOSUpgradeSettle(context.Background())

	// start.d holds post-start hooks (the dashboard's registers its event types
	// with the bus) and nothing here waits for them. They run after READY: each
	// may take up to a minute, so a few of them before it would outlast
	// systemd's start timeout and get the core killed and restarted in a loop.
	go func() {
		if err := command.ExecuteScripts(filepath.Join(constants.DefaultConfigPath, "start.d")); err != nil {
			logger.Error("one or more start scripts failed", zap.Error(err))
		}
	}()
	// http.HandleFunc("/v1/file/test", func(w http.ResponseWriter, r *http.Request) {

	// 	//http.ServeFile(w, r, r.URL.Path[1:])
	// 	http.ServeFile(w, r, "/DATA/test.img")
	// })
	// go http.ListenAndServe(":8081", nil)

	s := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // fix G112: Potential slowloris attack (see https://github.com/securego/gosec)
	}

	logger.Info("CasaOS main service is listening...", zap.Any("address", listener.Addr().String()))
	// defer service.MyService.Storage().UnmountAllStorage()
	err = s.Serve(listener) // not using http.serve() to fix G114: Use of net/http serve function that has no support for setting timeouts (see https://github.com/securego/gosec)
	if err != nil {
		panic(err)
	}
}

// stopHostStats ends the sampler and closes the service; a no-op while the statistics are off.
var stopHostStats atomic.Pointer[func()]

// hostStatsConfig wires the host's statistics to this core: the same interfaces as
// /v1/sys/utilization, the storages the dashboard lists, the alerts and the message bus.
func hostStatsConfig() hoststats.Config {
	return hoststats.Config{
		Dir:   "/var/lib/casaos/stats/core",
		Clock: stats.SystemClock{},
		Source: &hoststats.SystemSource{
			Interfaces: func() []string { return service.MyService.System().GetNet(true) },
			Volumes: func(ctx context.Context) ([]hoststats.Volume, error) {
				list, err := alerts.StorageVolumes(ctx, config.CommonInfo.RuntimePath)
				if err != nil {
					return nil, err
				}
				volumes := make([]hoststats.Volume, 0, len(list))
				for _, v := range list {
					volumes = append(volumes, hoststats.Volume{MountPoint: v.MountPoint, Size: v.Size, Used: v.Used})
				}
				return volumes, nil
			},
			IsPhysical: func(name string) bool {
				_, err := os.Stat("/sys/block/" + name + "/device")
				return err == nil
			},
		},
		Publish: func(name string, properties map[string]string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := service.MyService.MessageBus().PublishEventWithResponse(ctx, common.SERVICENAME, name, properties)
			if err != nil {
				logger.Error("failed to publish event to message bus", zap.Error(err), zap.String("event", name))
				return
			}
			if response.StatusCode() != http.StatusOK {
				logger.Error("failed to publish event to message bus", zap.String("event", name), zap.String("status", response.Status()))
			}
		},
		Hub:  alerts.Default.Handle,
		Name: func() string { name, _ := os.Hostname(); return name },
		Boot: func() time.Time {
			seconds, err := host.BootTime()
			if err != nil {
				return time.Time{}
			}
			return time.Unix(int64(seconds), 0)
		},
	}
}

// sigintIgnoredAtStart says whether SIGINT was ignored when the process began (the job of a shell that is sent to the background
// is): it is looked at here, where nothing of the core can have listened to it yet, since listening to a signal that was ignored
// makes it one that is no longer.
var sigintIgnoredAtStart = signal.Ignored(syscall.SIGINT)

// stopThePlanWhenTheCoreIsToldToStop cancels the plan that is being made when the core is told to stop (SIGTERM, and SIGINT
// unless the core was started ignoring it), and waits a few seconds for it to remove the directory it made in /var/tmp; it ends
// the loop that looks at an upgrade of the operating system first, and waits for the look that is being made, so that no unit is
// started by a core that is going; then it lets the signal end the core as it would have: nothing else of how the core stops is
// changed.
func stopThePlanWhenTheCoreIsToldToStop() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, service.OSPlanStopSignals(sigintIgnoredAtStart)...)
	received := <-signals
	service.MyService.System().StopSystemOSUpgradeSettle(service.OSPlanStopWait)
	service.MyService.System().StopSystemOSPlan(service.OSPlanStopWait)
	// the plan ends first; then the sampler, whose last tick no longer waits for a late refresh
	if f := stopHostStats.Load(); f != nil {
		(*f)()
	}
	// the default again, and the signal again
	signal.Stop(signals)
	if number, ok := received.(syscall.Signal); ok {
		_ = syscall.Kill(os.Getpid(), number)
		time.Sleep(time.Second)
		os.Exit(128 + int(number))
	}
	os.Exit(1)
}

// lastDockerRun is the last run of the Docker update as the alerts tell it, read from the run's
// log and from systemd: it asks Docker nothing and takes no lock of the update.
func lastDockerRun() alerts.DockerRun {
	status, nonce := service.MyService.System().DockerUpdateRun()
	run := alerts.DockerRun{Nonce: nonce, Outcome: status.Outcome, ErrorCode: status.ErrorCode, To: status.To}
	for _, container := range status.NotReturned {
		run.NotReturned = append(run.NotReturned, alerts.DockerNotReturned{Name: container.Name, RestartPolicy: container.RestartPolicy})
	}
	return run
}

// dashboardAddress is the dashboard's address on the box's first network, or
// "" when it has none.
func dashboardAddress() string {
	info := service.MyService.System().GetDeviceInfo()
	if len(info.LanIpv4) == 0 {
		return ""
	}
	address := "http://" + info.LanIpv4[0]
	if info.Port != 80 {
		address += ":" + strconv.Itoa(info.Port)
	}
	return address
}
