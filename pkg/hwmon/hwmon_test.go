package hwmon

import (
	"os"
	"path/filepath"
	"testing"
)

// chip lays out one hwmon directory the way the kernel does: its name, and its files with their values.
func chip(t *testing.T, root, dir, name string, files map[string]string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "name"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for f, v := range files {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheProcessorIsFoundByItsNameAndNotByItsNumber(t *testing.T) {
	for _, c := range []struct {
		name  string
		boxes func(t *testing.T, root string)
		want  int
		ok    bool
	}{
		{"the laptop of the report: the charger and the battery come first, coretemp lists only its cores", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "ACAD", nil)
			chip(t, root, "hwmon1", "BAT1", nil)
			chip(t, root, "hwmon2", "coretemp", map[string]string{"temp2_input": "43000\n", "temp3_input": "45000\n"})
		}, 45000, true},
		{"coretemp with its package: the package, even when a core is hotter", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "nvme", map[string]string{"temp1_input": "38850\n"})
			chip(t, root, "hwmon1", "coretemp", map[string]string{"temp1_input": "61000\n", "temp2_input": "63000\n"})
		}, 61000, true},
		{"an AMD processor behind a disk: k10temp, not the disk", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "nvme", map[string]string{"temp1_input": "38850\n"})
			chip(t, root, "hwmon1", "amdgpu", map[string]string{"temp1_input": "52000\n"})
			chip(t, root, "hwmon2", "k10temp", map[string]string{"temp1_input": "47250\n"})
		}, 47250, true},
		{"two sockets: the hottest", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "coretemp", map[string]string{"temp1_input": "55000\n"})
			chip(t, root, "hwmon1", "coretemp", map[string]string{"temp1_input": "58000\n"})
		}, 58000, true},
		{"a reading that is not a number is skipped for the next one", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "coretemp", map[string]string{"temp1_input": "N/A\n", "temp2_input": "41000\n"})
		}, 41000, true},
		{"a sensor that says 0 is not a reading", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "k10temp", map[string]string{"temp1_input": "0\n"})
		}, 0, false},
		{"a box with no processor chip: nothing, and not the first chip it has", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "nvme", map[string]string{"temp1_input": "38850\n"})
			chip(t, root, "hwmon1", "ACAD", nil)
		}, 0, false},
		{"coretemp is preferred to k10temp when both are there", func(t *testing.T, root string) {
			chip(t, root, "hwmon0", "k10temp", map[string]string{"temp1_input": "70000\n"})
			chip(t, root, "hwmon1", "coretemp", map[string]string{"temp1_input": "50000\n"})
		}, 50000, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			c.boxes(t, root)
			if got, ok := CPUTemperature(root); got != c.want || ok != c.ok {
				t.Errorf("%d %v, not %d %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestABoxWithoutHwmonHasNoTemperature(t *testing.T) {
	if got, ok := CPUTemperature(filepath.Join(t.TempDir(), "absent")); got != 0 || ok {
		t.Errorf("%d %v", got, ok)
	}
}
