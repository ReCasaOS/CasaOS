package service

import (
	"context"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// ModuleProvider is what the routes of the modules (/v1/sys/modules*) ask, and the one place where it is decided who answers:
//   - the module assistant (`casaos module`, casaosModules): archives from the catalogue of the release, apt, a transient unit per job; the default
//   - the system image (imageModules, [server] ImageManaged): the modules the image has, their units started and stopped
//
// Both give the same results, so the routes and the Modules page are the same for either.
type ModuleProvider interface {
	// ListSystemModules is the modules of the catalogue with what the box says of each; PlanSystemModule says what installing one would do, StartSystemModuleInstall and
	// StartSystemModuleRemove start the job that installs or removes it (an *ModuleRefusal says why not) and GetSystemModuleStatus how the job of the module is going.
	ListSystemModules() (modulecmd.ListResult, error)
	PlanSystemModule(name string) (osmodule.PlanResult, error)
	StartSystemModuleInstall(ctx context.Context, name, planID string) (SystemModuleStatus, error)
	StartSystemModuleRemove(ctx context.Context, name string) (SystemModuleStatus, error)
	// PlanSystemModuleUpdate and StartSystemModuleUpdate are the plan and the start for the update of a module that is installed, to the tag the catalogue names.
	PlanSystemModuleUpdate(name string) (osmodule.PlanResult, error)
	StartSystemModuleUpdate(ctx context.Context, name, planID string) (SystemModuleStatus, error)
	GetSystemModuleStatus(name string) (SystemModuleStatus, error)
}

// modules is the provider of this core: the system image's on an image-managed host, the module assistant's otherwise.
func (s *systemService) modules() ModuleProvider {
	if config.ServerInfo.ImageManaged {
		s.imageModulesOnce.Do(func() { s.imageModules = newImageModules(config.ServerInfo.ModuleCatalogue) })
		return s.imageModules
	}
	return casaosModules{s.systemPackageUpdater()}
}

func (s *systemService) ListSystemModules() (modulecmd.ListResult, error) {
	return s.modules().ListSystemModules()
}

func (s *systemService) PlanSystemModule(name string) (osmodule.PlanResult, error) {
	return s.modules().PlanSystemModule(name)
}

func (s *systemService) StartSystemModuleInstall(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return s.modules().StartSystemModuleInstall(ctx, name, planID)
}

func (s *systemService) PlanSystemModuleUpdate(name string) (osmodule.PlanResult, error) {
	return s.modules().PlanSystemModuleUpdate(name)
}

func (s *systemService) StartSystemModuleUpdate(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return s.modules().StartSystemModuleUpdate(ctx, name, planID)
}

func (s *systemService) StartSystemModuleRemove(ctx context.Context, name string) (SystemModuleStatus, error) {
	return s.modules().StartSystemModuleRemove(ctx, name)
}

func (s *systemService) GetSystemModuleStatus(name string) (SystemModuleStatus, error) {
	return s.modules().GetSystemModuleStatus(name)
}

// casaosModules is the module assistant's provider: the code of system_module.go, as it was.
type casaosModules struct{ u *systemPackageUpdater }

func (m casaosModules) ListSystemModules() (modulecmd.ListResult, error) { return m.u.listModules() }

func (m casaosModules) PlanSystemModule(name string) (osmodule.PlanResult, error) {
	return m.u.planModule(name)
}

func (m casaosModules) StartSystemModuleInstall(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return m.u.startModuleInstall(ctx, name, planID)
}

func (m casaosModules) PlanSystemModuleUpdate(name string) (osmodule.PlanResult, error) {
	return m.u.planModuleMode(name, moduleVerbUpdate)
}

func (m casaosModules) StartSystemModuleUpdate(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return m.u.startModulePlanned(ctx, moduleVerbUpdate, name, planID)
}

func (m casaosModules) StartSystemModuleRemove(ctx context.Context, name string) (SystemModuleStatus, error) {
	return m.u.startModuleRemove(ctx, name)
}

func (m casaosModules) GetSystemModuleStatus(name string) (SystemModuleStatus, error) {
	return m.u.moduleStatus(name)
}
