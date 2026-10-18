package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	net2 "net"
	"os"
	osExec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/command"
	exec2 "github.com/ReCasaOS/CasaOS-Common/utils/exec"

	"github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
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
	UpdateSystemVersion(version string) error
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
	// ListSystemModules is the modules of this release with what the box says of each; StartSystemModuleInstall and StartSystemModuleRemove start the unit that installs or removes one
	// (an *ModuleRefusal says why not), PlanSystemModule says what installing it would do and GetSystemModuleStatus how the job of the module is going.
	ListSystemModules() (modulecmd.ListResult, error)
	PlanSystemModule(name string) (osmodule.PlanResult, error)
	StartSystemModuleInstall(name, planID string) (SystemModuleStatus, error)
	StartSystemModuleRemove(name string) (SystemModuleStatus, error)
	GetSystemModuleStatus(name string) (SystemModuleStatus, error)
	GetSystemConfigDebug() []string
	GetCasaOSLogs(lineNumber int) string
	UpdateAssist()
	UpSystemPort(port string)
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
	GetMacAddress() (string, error)
	SystemReboot() error
	SystemShutdown() error
	GetSystemEntry() string
	GenreateSystemEntry()
}
type systemService struct {
	packageUpdates *systemPackageUpdater
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
				file, _ := os.Stat(link)
				temp.IsDir = file.IsDir()
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

func (s *systemService) UpdateSystemVersion(version string) error {
	if reason, busy := s.systemPackageUpdater().maintenanceBusy(context.Background(), common.UPDATE_UNIT+".service"); busy {
		return fmt.Errorf("%w: %s", ErrSystemMaintenanceBusy, reason)
	}
	keyName := "casa_version:" + resolveUpdateVersionURL()
	Cache.Delete(keyName)

	logPath := filepath.Join(config.AppInfo.LogPath, "upgrade.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		return fmt.Errorf("prepare update log: %w", err)
	}

	return startDetachedUpdate(resolveUpdateInstallerURL(), logPath, version)
}

func startDetachedUpdate(installerURL, logPath, version string) error {
	args := detachedUpdateArgs(installerURL, logPath, version)
	output, err := osExec.Command("systemd-run", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("start detached update: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func detachedUpdateArgs(installerURL, logPath, version string) []string {
	return []string{
		"--quiet",
		"--collect",
		"--unit=" + common.UPDATE_UNIT,
		"--property=Type=exec",
		"--setenv=CASAOS_INSTALLER_DETACHED=1",
		"--description=CasaOS update " + version,
		"/bin/bash",
		"-c",
		detachedUpdateCommand(installerURL, logPath),
	}
}

func detachedUpdateCommand(installerURL, logPath string) string {
	return "set -o pipefail; exec >> " + shellQuote(logPath) + " 2>&1; curl -fsSL " + shellQuote(installerURL) + " | /bin/bash"
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

func (s *systemService) UpSystemPort(port string) {
	if len(port) > 0 && port != config.ServerInfo.HttpPort {
		config.Cfg.Section("server").Key("HttpPort").SetValue(port)
		config.ServerInfo.HttpPort = port
	}
	config.Cfg.SaveTo(config.SystemConfigInfo.ConfigPath)
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
	path := GetCPUThermalZone()
	if len(path) > 0 {
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
