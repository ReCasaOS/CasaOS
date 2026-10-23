/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-05-13 18:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-21 15:27:53
 * @FilePath: /CasaOS/pkg/utils/version/version.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package version

import (
	"os"
	"strings"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// IsVersionNewer says whether latest is a later release than current, as osmodule.CompareVersions compares them (a pre-release before its release); a string that is not a version is
// newer than nothing.
func IsVersionNewer(latest string, current string) bool {
	c, ok := osmodule.CompareVersions(latest, current)
	return ok && c > 0
}

func CurrentVersion() string {
	return currentVersionFromFile(common.FORK_RELEASE_FILE)
}

func currentVersionFromFile(path string) string {
	// FORK_RELEASE_FILE is the truth: the installer writes the distribution's tag
	// there on every install and every upgrade, so on any host that has run a
	// recent installer this is what answers.
	//
	// FORK_RELEASE_VERSION is the fallback, and it is a FLOOR rather than a second
	// copy of the same value: the distribution this binary was BUILT for. The two
	// may differ, because a distribution release need not ship this component --
	// and wherever they differ, the marker exists and wins. A host with no marker
	// has not run a recent installer, so it is running the binary that shipped with
	// the distribution the constant names, and the constant is the honest answer
	// for it. Reading them as equal is what made every distribution release drag a
	// release of this component along for one string.
	//
	// Both hold the tag with its "v". The API answers a bare version - the dashboard
	// adds the "v" itself, so the tag's own prefix showed up as "vv0.4.40".
	version := common.FORK_RELEASE_VERSION
	if data, err := os.ReadFile(path); err == nil {
		if installedVersion := strings.TrimSpace(string(data)); installedVersion != "" {
			version = installedVersion
		}
	}
	return strings.TrimPrefix(version, "v")
}

func IsNeedUpdate(version model.Version) (bool, model.Version) {
	return IsVersionNewer(version.Version, CurrentVersion()), version
}
