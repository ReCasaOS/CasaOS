// Package hwmon reads the temperature of the processor from the hwmon chips the kernel lists in /sys/class/hwmon.
package hwmon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cpuChips are the hwmon chips that are the processor's own sensor on x86: Intel's coretemp, AMD's k10temp and zenpower. They come in the order they are preferred.
var cpuChips = []string{"coretemp", "k10temp", "zenpower"}

// CPUTemperature is the temperature of the processor in millidegrees, as the chips under root (/sys/class/hwmon) report it: the hottest chip of the first name in cpuChips the box has
// (coretemp has one per socket), and false when it has none or none that can be read. A chip's number is not fixed (the battery and the charger of a laptop take the first ones), which is
// why the chip is found by its name and never by hwmon0.
func CPUTemperature(root string) (int, bool) {
	dirs, _ := filepath.Glob(filepath.Join(root, "hwmon*"))
	for _, chip := range cpuChips {
		hottest := 0
		for _, dir := range dirs {
			if name, err := os.ReadFile(filepath.Join(dir, "name")); err != nil || strings.TrimSpace(string(name)) != chip {
				continue
			}
			hottest = max(hottest, chipTemperature(dir))
		}
		if hottest > 0 {
			return hottest, true
		}
	}
	return 0, false
}

// chipTemperature is temp1_input when the chip has one (the package on coretemp, the control temperature on k10temp), and otherwise the hottest of its temp*_input files: a laptop whose
// coretemp lists only its cores has no temp1.
func chipTemperature(dir string) int {
	if t := milliDegrees(filepath.Join(dir, "temp1_input")); t > 0 {
		return t
	}
	inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
	hottest := 0
	for _, input := range inputs {
		hottest = max(hottest, milliDegrees(input))
	}
	return hottest
}

// milliDegrees is the number in the file, and 0 for one that is missing, cannot be read (a sensor that is not ready answers an error) or is not a number.
func milliDegrees(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return n
}
