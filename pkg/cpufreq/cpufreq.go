// Package cpufreq reads the current frequency of the processor from the cpufreq files the kernel lists under /sys/devices/system/cpu.
package cpufreq

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Average is the mean current frequency of the cores under root (/sys/devices/system/cpu), in MHz, and false when no core
// reports one (a virtual machine, or a kernel without cpufreq). The kernel gives kHz in scaling_cur_freq; a core that cannot be read is left out.
func Average(root string) (int, bool) {
	files, _ := filepath.Glob(filepath.Join(root, "cpu[0-9]*", "cpufreq", "scaling_cur_freq"))
	sum, n := 0, 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		khz, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || khz <= 0 {
			continue
		}
		sum += khz
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / n / 1000, true
}
