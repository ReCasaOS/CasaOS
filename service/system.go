package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	net2 "net"
	"net/http"
	"os"
	osExec "os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/command"
	exec2 "github.com/ReCasaOS/CasaOS-Common/utils/exec"

	"github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/cpufreq"
	"github.com/ReCasaOS/CasaOS/pkg/hwmon"
	"github.com/ReCasaOS/CasaOS/pkg/installersig"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/pkg/utils/httper"
	"github.com/ReCasaOS/CasaOS/pkg/utils/ip_helper"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

type SystemService interface {
	// UpdateSystemVersion installs the release the version announcement names (version is not
	// read), once its installer's signature and release are checked.
	UpdateSystemVersion(version string) error
	// UpdateSystemRelease is UpdateSystemVersion for the release tagged tag, not the latest.
	UpdateSystemRelease(tag string) error
	// MaintenanceBusy says whether a package update, an update of Docker or a package
	// manager is changing the box now.
	MaintenanceBusy() bool
	GetSystemPackageUpdates() (SystemPackageUpdates, error)
	StartSystemPackageUpdate() (SystemPackageUpdateStatus, error)
	GetSystemPackageUpdateStatus() SystemPackageUpdateStatus
	// GetSystemDockerContainers lists the running containers, which an update of Docker stops.
	GetSystemDockerContainers() SystemDockerContainers
	// StartDockerUpdate updates the Docker engine to the plan the owner confirmed (its id), going ahead
	// of a hold on the engine's packages only if allowHeld, the owner's word (dpkg drops the hold of
	// the packages it unpacks: the unit puts it back). A *DockerUpdateRefusal says why it did not
	// start; the status comes with the error.
	StartDockerUpdate(planID string, allowHeld bool) (SystemDockerUpdateStatus, error)
	// DockerUpdateStatus says how the update of Docker is going, or how it went.
	DockerUpdateStatus() SystemDockerUpdateStatus
	// SetAppOperations tells how to ask which apps have an operation in progress; the update of
	// Docker is refused while it cannot be asked.
	SetAppOperations(list AppOperationsFunc)
	// DockerUpdateRun is DockerUpdateStatus and the nonce of the run it is about ("" with none).
	DockerUpdateRun() (SystemDockerUpdateStatus, string)
	// DockerUpdateMuted says whether the alerts about the apps' containers are to be kept quiet:
	// the update of Docker runs, or ended less than ten minutes ago. It takes no lock and is cheap.
	DockerUpdateMuted() bool
	// OnDockerUpdateQueued sets what is called when a run of the Docker update has its log: it
	// must return at once.
	OnDockerUpdateQueued(queued func())
	// GetSystemOS is the operating system and how long its security updates last: it only reads.
	GetSystemOS() ossupport.Report
	// GetSystemOSReadiness is the report of whether the box could be upgraded to the next release of
	// its operating system, and why not: it only reads, and takes up to twenty seconds.
	GetSystemOSReadiness() osreadiness.Answer
	// StartSystemOSPlan starts the job that makes the plan of the upgrade of the operating system: what apt would
	// do, in a sandbox, with the machine unchanged. A job that runs is the answer (AlreadyRunning) and none is
	// started; an *OSPlanRefusal says why none was.
	StartSystemOSPlan() (SystemOSPlanStatus, error)
	// GetSystemOSPlan says how the plan is going, or how it went, and whether one can be made: it runs no apt
	// and waits for no lock.
	GetSystemOSPlan() SystemOSPlanStatus
	// GetSystemOSRunbook is the text with which to do the hop by hand, written from the plan that is there; an
	// *OSPlanRefusal says why there is none.
	GetSystemOSRunbook() (*osplan.Runbook, error)
	// StopSystemOSPlan cancels the plan that is being made, and waits (no longer than wait) for it to have
	// removed its directory: the core's end.
	StopSystemOSPlan(wait time.Duration)
	// SetSystemOSUpgradeBeta turns the upgrade of the operating system on or off for what begins something (the flag file); an
	// *OSUpgradeRefusal run_open says that it is not turned off while a run is open.
	SetSystemOSUpgradeBeta(enabled bool) error
	// GetSystemOSUpgradeStatus is how the upgrade is going, for the page that polls it: it runs no apt, takes no lock of a run, and
	// answers whatever the state of the machine.
	GetSystemOSUpgradeStatus() OSUpgradeStatus
	// StartSystemOSUpgrade starts the upgrade (or its rehearsal) for the plan that the owner read; the owner's buttons below act on
	// the run that it makes. Each of them answers the status that follows it, and an *OSUpgradeRefusal says why it did not act.
	StartSystemOSUpgrade(req OSUpgradeStartRequest) (OSUpgradeStatus, error)
	// StartSystemOSUpgradeRefresh brings the release that the machine runs to its last state, before a plan is made: a run of its own kind, with no point of no return.
	StartSystemOSUpgradeRefresh(req OSUpgradeRefreshRequest) (OSUpgradeStatus, error)
	RebootSystemOSUpgrade() (OSUpgradeStatus, error)
	ResumeSystemOSUpgrade() (OSUpgradeStatus, error)
	AbortSystemOSUpgrade() (OSUpgradeStatus, error)
	AckSystemOSUpgrade(req OSUpgradeAckRequest) (OSUpgradeStatus, error)
	// GetSystemOSUpgradeRunbook is the owner's own text of the run that is open, for the owner who takes it over.
	GetSystemOSUpgradeRunbook() (*OSUpgradeRunbook, error)
	// StartSystemOSUpgradeSettle starts the loop that looks at the upgrade that is open, once, when the core is ready to serve;
	// StopSystemOSUpgradeSettle ends it and waits, for no longer than wait, for the look that is being made.
	StartSystemOSUpgradeSettle(ctx context.Context)
	StopSystemOSUpgradeSettle(wait time.Duration)
	// OSUpgradeMuted says whether the alerts about the apps' containers are to be kept quiet: an upgrade holds the machine, or let
	// go of it less than ten minutes ago. SetOSUpgradeNotifier sets what tells the owner of what became of an upgrade.
	OSUpgradeMuted() bool
	SetOSUpgradeNotifier(notify func(key, sentence string))
	// The modules (system_module_provider.go): the module assistant's, or the system image's ([server] ImageManaged).
	ModuleProvider
	// RefreshSystemModuleCatalogue fetches the online catalogue of the modules once, when [modules] OnlineCatalogue is on and the host is not image-managed, and logs what became of it.
	RefreshSystemModuleCatalogue(ctx context.Context)
	// SetSystemModuleAutoUpdate lets the night update an installed module written outside ReCasaOS, or no longer (system_module.go).
	SetSystemModuleAutoUpdate(name string, enabled bool) error
	GetSystemConfigDebug() []string
	GetCasaOSLogs(lineNumber int) string
	UpdateAssist()
	GetTimeZone() string
	UpAppOrderFile(str, id string)
	GetAppOrderFile(id string) []byte
	GetNet(physics bool) []string
	GetNetInfo() []net.IOCountersStat
	GetCpuCoreNum() int
	GetCpuPercent() float64
	GetMemInfo() map[string]interface{}
	GetCpuInfo() []cpu.InfoStat
	GetDirPath(path string) ([]model.Path, error)
	GetDirPathOne(path string) (m model.Path)
	GetNetState(name string) string
	GetDiskInfo() *disk.UsageStat
	GetSysInfo() host.InfoStat
	GetDeviceTree() string
	GetDeviceInfo() model.DeviceInfo
	CreateFile(path string) (int, error)
	RenameFile(oldF, newF string) (int, error)
	MkdirAll(path string) (int, error)
	GetCPUTemperature() int
	GetCPUPower() map[string]string
	GetCPUFrequency() int
	GetMacAddress() (string, error)
	SystemReboot() error
	SystemShutdown() error
	GetSystemEntry() string
	GenreateSystemEntry()
}
type systemService struct {
	packageUpdates *systemPackageUpdater
	// imageModules is the module provider of an image-built host, made on first use
	imageModules     *imageModules
	imageModulesOnce sync.Once
	// osPlan is the job that makes the plan of the upgrade of the operating system.
	osPlan osPlanJob
	// osUpgrade is what the core keeps in memory of the upgrade that it runs: its loop, and nothing that the upgrade is.
	osUpgrade osUpgradeEngine
}

func (c *systemService) GetDeviceInfo() model.DeviceInfo {
	m := model.DeviceInfo{}
	m.OS_Version = common.VERSION
	err, portStr := MyService.Gateway().GetPort()
	if err != nil {
		m.Port = 80
	} else {
		port := gjson.Get(portStr, "data")
		if len(port.Raw) == 0 {
			m.Port = 80
		} else {
			p, err := strconv.Atoi(port.String()) // the gateway answers "80", a string
			if err != nil {
				m.Port = 80
			} else {
				m.Port = p
			}
		}
	}
	allIpv4 := ip_helper.GetDeviceAllIPv4()
	ip := []string{}
	nets := MyService.System().GetNet(true)
	for _, n := range nets {
		if v, ok := allIpv4[n]; ok {
			{
				ip = append(ip, v)
			}
		}
	}

	m.LanIpv4 = ip
	h, err := host.Info() /*  */
	if err == nil {
		m.DeviceName = h.Hostname
	}
	mb := model.BaseInfo{}

	err = json.Unmarshal(file.ReadFullFile(config.AppInfo.DBPath+"/baseinfo.conf"), &mb)
	if err == nil {
		m.Hash = mb.Hash
	}

	osRelease, _ := file.ReadOSRelease()
	m.DeviceModel = osRelease["MODEL"]
	m.DeviceSN = osRelease["SN"]
	res := httper.Get("http://127.0.0.1:"+strconv.Itoa(m.Port)+"/v1/users/status", nil)
	init := gjson.Get(res, "data.initialized")
	m.Initialized, _ = strconv.ParseBool(init.Raw)

	return m
}

func (c *systemService) GenreateSystemEntry() {
	modelsPath := "/var/lib/casaos/www/modules"
	entryFileName := "entry.json"
	entryFilePath := filepath.Join(config.AppInfo.DBPath, "db", entryFileName)
	file.IsNotExistCreateFile(entryFilePath)

	dir, err := os.ReadDir(modelsPath)
	if err != nil {
		logger.Error("read dir error", zap.Error(err))
		return
	}
	json := "["
	for _, v := range dir {
		data, err := os.ReadFile(filepath.Join(modelsPath, v.Name(), entryFileName))
		if err != nil {
			logger.Error("read entry file error", zap.Error(err))
			continue
		}
		json += string(data) + ","
	}
	json = strings.TrimRight(json, ",")
	json += "]"
	err = os.WriteFile(entryFilePath, []byte(json), 0o666)
	if err != nil {
		logger.Error("write entry file error", zap.Error(err))
		return
	}
}

func (c *systemService) GetSystemEntry() string {
	modelsPath := "/var/lib/casaos/www/modules"
	entryFileName := "entry.json"
	dir, err := os.ReadDir(modelsPath)
	if err != nil {
		logger.Error("read dir error", zap.Error(err))
		return ""
	}
	json := "["
	for _, v := range dir {
		data, err := os.ReadFile(filepath.Join(modelsPath, v.Name(), entryFileName))
		if err != nil {
			logger.Error("read entry file error", zap.Error(err))
			continue
		}
		json += string(data) + ","
	}
	json = strings.TrimRight(json, ",")
	json += "]"
	if err != nil {
		logger.Error("write entry file error", zap.Error(err))
		return ""
	}
	return json
}

func (c *systemService) GetMacAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	nets := MyService.System().GetNet(true)
	for _, v := range interfaces {
		for _, n := range nets {
			if v.Name == n {
				return v.HardwareAddr, nil
			}
		}
	}
	return "", errors.New("not found")
}

func (c *systemService) MkdirAll(path string) (int, error) {
	_, err := os.Stat(path)
	if err == nil {
		return common_err.DIR_ALREADY_EXISTS, nil
	} else {
		if os.IsNotExist(err) {
			os.MkdirAll(path, os.ModePerm)
			return common_err.SUCCESS, nil
		} else if strings.Contains(err.Error(), ": not a directory") {
			return common_err.FILE_OR_DIR_EXISTS, err
		}
	}
	return common_err.SERVICE_ERROR, err
}

func (c *systemService) RenameFile(oldF, newF string) (int, error) {
	_, err := os.Stat(newF)
	if err == nil {
		return common_err.DIR_ALREADY_EXISTS, nil
	} else {
		if os.IsNotExist(err) {
			err := os.Rename(oldF, newF)
			if err != nil {
				return common_err.SERVICE_ERROR, err
			}
			return common_err.SUCCESS, nil
		}
	}
	return common_err.SERVICE_ERROR, err
}

func (c *systemService) CreateFile(path string) (int, error) {
	_, err := os.Stat(path)
	if err == nil {
		return common_err.FILE_OR_DIR_EXISTS, nil
	} else {
		if os.IsNotExist(err) {
			file.CreateFile(path)
			return common_err.SUCCESS, nil
		}
	}
	return common_err.SERVICE_ERROR, err
}

func (c *systemService) GetDeviceTree() string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetDeviceTree"); err != nil {
		return ""
	} else {
		return output
	}
}

func (c *systemService) GetSysInfo() host.InfoStat {
	info, _ := host.Info()
	return *info
}

func (c *systemService) GetDiskInfo() *disk.UsageStat {
	path := "/"
	if runtime.GOOS == "windows" {
		path = "C:"
	}
	diskInfo, _ := disk.Usage(path)
	diskInfo.UsedPercent, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", diskInfo.UsedPercent), 64)
	diskInfo.InodesUsedPercent, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", diskInfo.InodesUsedPercent), 64)
	return diskInfo
}

func (c *systemService) GetNetState(name string) string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;CatNetCardState " + name); err != nil {
		return ""
	} else {
		return output
	}
}

func (c *systemService) GetDirPathOne(path string) (m model.Path) {
	f, err := os.Stat(path)
	if err != nil {
		return
	}
	m.IsDir = f.IsDir()
	m.Name = f.Name()
	m.Path = path
	m.Size = f.Size()
	m.Date = f.ModTime()
	return
}

func (c *systemService) GetDirPath(path string) ([]model.Path, error) {
	if path == "/DATA" {
		sysType := runtime.GOOS
		if sysType == "windows" {
			path = "C:\\CasaOS\\DATA"
		}
		if sysType == "darwin" {
			path = "./CasaOS/DATA"
		}

	}

	ls, err := os.ReadDir(path)
	if err != nil {
		logger.Error("when read dir", zap.Error(err))
		return []model.Path{}, err
	}
	dirs := []model.Path{}
	if len(path) > 0 {
		for _, l := range ls {
			filePath := filepath.Join(path, l.Name())
			link, err := filepath.EvalSymlinks(filePath)
			if err != nil {
				link = filePath
			}
			tempFile, err := l.Info()
			if err != nil {
				logger.Error("when read dir", zap.Error(err))
				return []model.Path{}, err
			}
			temp := model.Path{Name: l.Name(), Path: filePath, IsDir: l.IsDir(), Date: tempFile.ModTime(), Size: tempFile.Size()}
			if filePath != link {
				// a link changed since it was resolved: shown as the entry it is
				if file, err := os.Stat(link); err == nil {
					temp.IsDir = file.IsDir()
				}
			}
			dirs = append(dirs, temp)
		}
	} else {
		dirs = append(dirs, model.Path{Name: "DATA", Path: "/DATA/", IsDir: true, Date: time.Now()})
	}
	return dirs, nil
}

func (c *systemService) GetCpuInfo() []cpu.InfoStat {
	info, _ := cpu.Info()
	return info
}

func (c *systemService) GetMemInfo() map[string]interface{} {
	memInfo, _ := mem.VirtualMemory()
	memInfo.UsedPercent, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", memInfo.UsedPercent), 64)
	memData := make(map[string]interface{})
	memData["total"] = memInfo.Total
	memData["available"] = memInfo.Available
	memData["used"] = memInfo.Used
	memData["free"] = memInfo.Free
	memData["usedPercent"] = memInfo.UsedPercent
	return memData
}

func (c *systemService) GetCpuPercent() float64 {
	percent, _ := cpu.Percent(0, false)
	value, _ := strconv.ParseFloat(fmt.Sprintf("%.1f", percent[0]), 64)
	return value
}

func (c *systemService) GetCpuCoreNum() int {
	count, _ := cpu.Counts(false)
	return count
}

func (c *systemService) GetNetInfo() []net.IOCountersStat {
	parts, _ := net.IOCounters(true)
	return parts
}

func (c *systemService) GetNet(physics bool) []string {
	t := "1"
	if physics {
		t = "2"
	}

	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetNetCard " + t); err != nil {
		return []string{}
	} else {
		return strings.Split(output, "\n")
	}
}

// The update runs a release's installer only once it is checked: signed by one of
// installersig.Keys, announcing that release, and no older than the release installed. The
// seams below are package variables for the tests.
var (
	// installerReleaseBase is where a release's files are, by tag.
	installerReleaseBase = common.FORK_RELEASE_BASE
	installerClient      = &http.Client{Timeout: 2 * time.Minute}
	// installerDir holds the checked installers, root's alone (the core runs as root).
	installerDir = "/var/lib/casaos/update"
	// installedReleaseFile is the release installed: an update is never older.
	installedReleaseFile = common.FORK_RELEASE_FILE
	runSystemdRun        = func(args ...string) ([]byte, error) {
		return osExec.Command("systemd-run", args...).CombinedOutput()
	}
	// latestReleaseTag is the release the version announcement names (version.json, through its cache).
	latestReleaseTag = func() string { return (&casaService{}).GetCasaosVersion().Version }
)

// installerTestKeyEnv is one more public key (base64 of 32 bytes) for the install check, which
// signs its own installers: it counts only while installerURLEnv names the check's installer.
const installerTestKeyEnv = "CASAOS_INSTALLER_TEST_PUBKEY"

// installerMaxSize bounds what is read of an installer or its signature.
const installerMaxSize = 5 << 20

// installerUnreachable is an installer, its signature or its release that could not be had: the
// update is not started and the automatic update tries again without counting a failure.
type installerUnreachable struct{ err error }

func (e installerUnreachable) Error() string {
	return "the installer could not be downloaded: " + e.err.Error()
}
func (e installerUnreachable) Unwrap() []error { return []error{autoupdate.ErrBusy, e.err} }

// installerRefused is an installer that failed its check: a failed attempt, told as such.
type installerRefused struct{ tag string }

func (e installerRefused) Error() string {
	return "The update to " + e.tag + " was not installed: its installer is not signed by ReCasaOS."
}
func (e installerRefused) Unwrap() error { return autoupdate.ErrRefused }

// fetchVerifiedInstaller downloads the installer of the release tagged tag and its signature from
// one address, checks both, and writes the installer to a file of installerDir that only root
// can read, write or run: its path. Nothing is written for an installer that fails its check.
func fetchVerifiedInstaller(ctx context.Context, tag string) (string, error) {
	if !releaseTag.MatchString(tag) {
		return "", installerUnreachable{fmt.Errorf("%q is not a release tag", tag)}
	}
	url := resolveReleaseInstallerURL(tag)
	script, err := downloadInstallerFile(ctx, url)
	if err != nil {
		return "", installerUnreachable{err}
	}
	sig, err := downloadInstallerFile(ctx, url+".sig")
	var status installerStatusError
	reason := ""
	switch {
	case errors.As(err, &status) && int(status) == http.StatusNotFound:
		// the script was there: its release has no signature, which is no network problem
		reason = "it has no signature"
	case err != nil:
		return "", installerUnreachable{err}
	default:
		reason = checkInstaller(script, sig, tag)
	}
	if reason != "" {
		logger.Info("update: the installer is refused", zap.String("tag", tag), zap.String("url", url), zap.String("reason", reason))
		return "", installerRefused{tag}
	}
	return writeInstaller(script, tag)
}

// installerStatusError is an HTTP status other than 200.
type installerStatusError int

func (e installerStatusError) Error() string {
	return fmt.Sprintf("answered %d %s", int(e), http.StatusText(int(e)))
}

// downloadInstallerFile is the body of url: a 200 of at most installerMaxSize bytes.
func downloadInstallerFile(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := installerClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %w", url, installerStatusError(response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, installerMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > installerMaxSize {
		return nil, fmt.Errorf("%s is over %d bytes", url, installerMaxSize)
	}
	return body, nil
}

// checkInstaller says why script is refused as the installer of tag, "" when it is not.
func checkInstaller(script, sig []byte, tag string) string {
	if err := installersig.Verify(script, sig, installerKeys()); err != nil {
		return err.Error()
	}
	announced, err := installersig.ReleaseTag(script)
	if err != nil {
		return err.Error()
	}
	if announced != tag {
		return "it announces " + announced
	}
	// A missing or unreadable file, or one whose content is not a vX.Y.Z tag (Newer is false
	// then), is no lower bound: the box does not know what is installed, only root writes that
	// file, and the signature and the tag asked still hold.
	if data, err := os.ReadFile(installedReleaseFile); err == nil {
		installed := "v" + strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
		if installersig.Newer(installed, tag) {
			return "it is older than the installed " + installed
		}
	}
	return ""
}

// installerKeys is installersig.Keys, and the install check's test key while the install check
// names its own installer.
func installerKeys() []ed25519.PublicKey {
	keys := append([]ed25519.PublicKey(nil), installersig.Keys...)
	if installCheckURL(installerURLEnv) == "" {
		return keys
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv(installerTestKeyEnv)))
	if err == nil && len(key) == ed25519.PublicKeySize {
		keys = append(keys, key)
	}
	return keys
}

// writeInstaller writes script to installerDir/install-<tag>.sh, folder and file 0700: through a
// new file renamed over the old, so a script that runs is never rewritten under it.
func writeInstaller(script []byte, tag string) (string, error) {
	// the folder must be a real folder of root's: a link or anything else in its place goes
	if info, err := os.Lstat(installerDir); err == nil && !ownFolder(info) {
		if err := os.RemoveAll(installerDir); err != nil {
			return "", fmt.Errorf("write the installer: %w", err)
		}
	}
	if err := os.MkdirAll(installerDir, 0o700); err != nil {
		return "", fmt.Errorf("write the installer: %w", err)
	}
	// MkdirAll leaves a folder that exists as it is
	if err := os.Chmod(installerDir, 0o700); err != nil {
		return "", fmt.Errorf("write the installer: %w", err)
	}
	// only the last checked installer is kept, and no temporary file of an earlier write; one
	// that runs keeps its file open, removed or not
	old, _ := filepath.Glob(filepath.Join(installerDir, "install-*.sh"))
	temps, _ := filepath.Glob(filepath.Join(installerDir, ".install-*"))
	for _, name := range append(old, temps...) {
		_ = os.Remove(name)
	}
	tmp, err := os.CreateTemp(installerDir, ".install-*")
	if err != nil {
		return "", fmt.Errorf("write the installer: %w", err)
	}
	defer os.Remove(tmp.Name()) // nothing left once renamed
	_, err = tmp.Write(script)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o700)
	}
	path := filepath.Join(installerDir, "install-"+tag+".sh")
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return "", fmt.Errorf("write the installer: %w", err)
	}
	return path, nil
}

// ownFolder says whether info is a directory (not a link) owned by the user the core runs as.
func ownFolder(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	// root on a box
	return info.IsDir() && ok && int(st.Uid) == os.Geteuid()
}

// UpdateSystemVersion installs the release the version announcement names, whatever the
// argument says: its installer and signature are both taken from that release.
func (s *systemService) UpdateSystemVersion(string) error {
	return s.updateSystem("")
}

func (s *systemService) UpdateSystemRelease(tag string) error {
	return s.updateSystem(tag)
}

// updateSystemLock is held by the update being prepared and started.
var updateSystemLock sync.Mutex

// releaseTag is a release's tag as the fork cuts them; it goes into a URL and a file name.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// resolveReleaseInstallerURL is the install.sh the release tagged tag carries (a releaseTag),
// unless config or the install check names an installer of their own. Its signature is that
// address + ".sig".
func resolveReleaseInstallerURL(tag string) string {
	if url := resolveUpdateInstallerURL(); url != common.FORK_UPDATE_URL {
		return url
	}
	return installerReleaseBase + tag + "/install.sh"
}

// updateSystem installs the release tagged tag, or with "" the one the version announcement
// names, once its installer is checked.
func (s *systemService) updateSystem(tag string) error {
	// one update at a time, the button's or the night's: the download takes minutes
	if !updateSystemLock.TryLock() {
		return fmt.Errorf("%w: another update of ReCasaOS is being prepared", ErrSystemMaintenanceBusy)
	}
	defer updateSystemLock.Unlock()
	if reason, busy := s.systemPackageUpdater().maintenanceBusy(context.Background(), common.UPDATE_UNIT+".service"); busy {
		return fmt.Errorf("%w: %s", ErrSystemMaintenanceBusy, reason)
	}
	if tag == "" {
		tag = latestReleaseTag()
	}
	// a feed may announce a release without its "v": the tag is the release's
	tag = "v" + strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if !releaseTag.MatchString(tag) {
		// no release to aim at is no failure of one: nothing is counted, the next check tries again
		return installerUnreachable{fmt.Errorf("no release tag to install (%q)", tag)}
	}
	installer, err := fetchVerifiedInstaller(context.Background(), tag)
	if err != nil {
		return err
	}
	// the download took time: whatever started meanwhile keeps the update away (the file stays
	// until the next checked installer replaces it)
	if reason, busy := s.systemPackageUpdater().maintenanceBusy(context.Background(), common.UPDATE_UNIT+".service"); busy {
		return fmt.Errorf("%w: %s", ErrSystemMaintenanceBusy, reason)
	}
	keyName := "casa_version:" + resolveUpdateVersionURL()
	Cache.Delete(keyName)

	logPath := filepath.Join(config.AppInfo.LogPath, "upgrade.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		return fmt.Errorf("prepare update log: %w", err)
	}

	return startDetachedUpdate(installer, logPath, tag)
}

func startDetachedUpdate(installer, logPath, version string) error {
	output, err := runSystemdRun(detachedUpdateArgs(installer, logPath, version)...)
	if err != nil {
		return fmt.Errorf("start detached update: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func detachedUpdateArgs(installer, logPath, version string) []string {
	return []string{
		"--quiet",
		"--collect",
		"--unit=" + common.UPDATE_UNIT,
		"--property=Type=exec",
		"--setenv=CASAOS_INSTALLER_DETACHED=1",
		"--description=CasaOS update " + version,
		"/bin/bash",
		"-c",
		detachedUpdateCommand(installer, logPath),
	}
}

// detachedUpdateCommand runs the checked installer file, its output appended to the log.
func detachedUpdateCommand(installer, logPath string) string {
	return "exec >> " + shellQuote(logPath) + " 2>&1; exec /bin/bash " + shellQuote(installer)
}

func resolveUpdateInstallerURL() string {
	if value := installCheckURL(installerURLEnv); value != "" {
		return value
	}
	value := strings.TrimSpace(config.ServerInfo.UpdateUrl)
	if validHTTPSURL(value) {
		return value
	}
	return common.FORK_UPDATE_URL
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (s *systemService) UpdateAssist() {
	command.ExecResultStrArray("source " + config.AppInfo.ShellPath + "/assist.sh")
}

func (s *systemService) GetTimeZone() string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetTimeZone"); err != nil {
		return ""
	} else {
		return output
	}
}

func (s *systemService) GetSystemConfigDebug() []string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetSysInfo"); err != nil {
		return []string{}
	} else {
		return strings.Split(output, "\n")
	}
}

func (s *systemService) UpAppOrderFile(str, id string) {
	file.WriteToPath([]byte(str), config.AppInfo.DBPath+"/"+id, "app_order.json")
}

func (s *systemService) GetAppOrderFile(id string) []byte {
	return file.ReadFullFile(config.AppInfo.UserDataPath + "/" + id + "/app_order.json")
}

func (s *systemService) GetCasaOSLogs(lineNumber int) string {
	file, err := os.Open(filepath.Join(config.AppInfo.LogPath, fmt.Sprintf("%s.%s",
		config.AppInfo.LogSaveName,
		config.AppInfo.LogFileExt,
	)))
	if err != nil {
		return err.Error()
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return err.Error()
	}

	return string(content)
}

func GetDeviceAllIP() []string {
	var address []string
	addrs, err := net2.InterfaceAddrs()
	if err != nil {
		return address
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net2.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To16() != nil {
				address = append(address, ipNet.IP.String())
			}
		}
	}
	return address
}

// find thermal_zone of cpu.
// assertions:
//   - thermal_zone "type" and "temp" are required fields
//     (https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-class-thermal)
func GetCPUThermalZone() string {
	keyName := "cpu_thermal_zone"

	var path string
	if result, ok := Cache.Get(keyName); ok {
		path, ok = result.(string)
		if ok {
			return path
		}
	}

	var name string
	cpu_types := []string{"x86_pkg_temp", "cpu", "CPU", "soc"}
	stub := "/sys/devices/virtual/thermal/thermal_zone"
	for i := 0; i < 100; i++ {
		path = stub + strconv.Itoa(i)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			name = strings.TrimSuffix(string(file.ReadFullFile(path+"/type")), "\n")
			for _, s := range cpu_types {
				if strings.HasPrefix(name, s) {
					//logger.Info(fmt.Sprintf("CPU thermal zone found: %s, path: %s.", name, path))
					Cache.SetDefault(keyName, path)
					return path
				}
			}
		} else {
			if len(name) > 0 { // proves at least one zone
				path = stub + "0"
			} else {
				path = ""
			}
			break
		}
	}

	Cache.SetDefault(keyName, path)
	return path
}

func (s *systemService) GetCPUTemperature() int {
	outPut := ""
	// the processor's own chip first, found by its name: hwmon0 is the charger on a laptop, and a box with no thermal zone has nothing else
	if raw, ok := hwmon.CPUTemperature("/sys/class/hwmon"); ok {
		outPut = strconv.Itoa(raw)
	} else if path := GetCPUThermalZone(); len(path) > 0 {
		outPut = string(file.ReadFullFile(path + "/temp"))
	} else {
		outPut = string(file.ReadFullFile("/sys/class/hwmon/hwmon0/temp1_input"))
		if len(outPut) == 0 {
			outPut = "0"
		}
	}

	celsius, _ := strconv.Atoi(strings.TrimSpace(outPut))

	if celsius > 1000 {
		celsius = celsius / 1000
	}
	return celsius
}

// GetCPUFrequency is the mean current frequency of the cores in MHz, 0 when the box reports none.
func (s *systemService) GetCPUFrequency() int {
	mhz, _ := cpufreq.Average("/sys/devices/system/cpu")
	return mhz
}

func (s *systemService) GetCPUPower() map[string]string {
	data := make(map[string]string, 2)
	data["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
	if file.Exists("/sys/class/powercap/intel-rapl/intel-rapl:0/energy_uj") {
		data["value"] = strings.TrimSpace(string(file.ReadFullFile("/sys/class/powercap/intel-rapl/intel-rapl:0/energy_uj")))
	} else {
		data["value"] = "0"
	}
	return data
}

func (s *systemService) SystemReboot() error {
	return s.systemPower("reboot")
}

func (s *systemService) SystemShutdown() error {
	return s.systemPower("poweroff")
}

func runSystemctlPowerAction(action string) error {
	cmd := exec2.Command("systemctl", "--no-block", action)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if message := strings.TrimSpace(string(output)); message != "" {
			return fmt.Errorf("systemctl %s: %w: %s", action, err, message)
		}
		return fmt.Errorf("systemctl %s: %w", action, err)
	}
	return nil
}

func NewSystemService() SystemService {
	return &systemService{packageUpdates: newSystemPackageUpdater()}
}
