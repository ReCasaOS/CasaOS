package cpufreq

import (
	"os"
	"path/filepath"
	"testing"
)

func core(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name, "cpufreq")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scaling_cur_freq"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAverageIsTheMeanOfTheCoresInMHz(t *testing.T) {
	root := t.TempDir()
	core(t, root, "cpu0", "3000000\n")
	core(t, root, "cpu1", "1000000\n")
	core(t, root, "cpu2", "not a number\n")

	if mhz, ok := Average(root); !ok || mhz != 2000 {
		t.Fatalf("got %d, %v; want 2000, true", mhz, ok)
	}
}

func TestAverageSaysNothingWithoutCpufreq(t *testing.T) {
	if mhz, ok := Average(t.TempDir()); ok || mhz != 0 {
		t.Fatalf("got %d, %v; want 0, false", mhz, ok)
	}
}
