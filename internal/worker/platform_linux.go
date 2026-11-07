//go:build linux

package worker

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/private-compute/network/internal/store"
)

// Discover собирает характеристики машины на Linux.
func Discover(ctx context.Context) (SystemInfo, error) {
	info := SystemInfo{}

	hostname, _ := os.Hostname()
	info.Hostname = hostname
	info.CPUCores = runtime.NumCPU()
	info.OS = readOSPrettyName()
	info.CPUModel = readCPUModel()
	info.RAMBytes = readMemTotal()

	if root, ok := statfsDisk("/"); ok {
		info.Disks = append(info.Disks, root)
	}
	// Отдельный раздел под кэш моделей, если он есть и отличается.
	if data, ok := statfsDisk("/var/lib"); ok && data.Mount != "/" {
		info.Disks = append(info.Disks, data)
	}

	_ = ctx
	snap := queryNvidia(context.Background())
	info.GPUs = snap.GPUs
	info.CUDAVer = snap.CUDAVersion
	info.DriverVer = snap.DriverVersion

	return info, nil
}

// Collect собирает телеметрию на Linux.
func Collect(ctx context.Context, disks []store.Disk) (Telemetry, error) {
	tel := Telemetry{}

	if free, ok := readMemAvailable(); ok {
		tel.RAMFreeBytes = Ptr(free)
	}
	if percent, ok := readLoadPercent(runtime.NumCPU()); ok {
		tel.CPUPercent = Ptr(percent)
	}
	if root, ok := statfsDisk("/"); ok {
		tel.DiskFreeBytes = Ptr(root.FreeBytes)
	}

	snap := queryNvidia(ctx)
	tel.VRAMFreeBytes = snap.VRAMFreeBytes
	tel.TemperatureC = snap.TemperatureC
	tel.PowerWatts = snap.PowerWatts
	if snap.UtilizationPct != nil {
		tel.Meta = map[string]any{"gpu_utilization_percent": *snap.UtilizationPct}
	}
	return tel, nil
}

// readOSPrettyName берёт PRETTY_NAME из /etc/os-release.
func readOSPrettyName() string {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return "linux"
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "linux"
}

// readCPUModel читает первую строку model name из /proc/cpuinfo.
func readCPUModel() string {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			if _, value, ok := strings.Cut(line, ":"); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// readMemTotal возвращает MemTotal в байтах.
func readMemTotal() int64 {
	values := readMeminfo()
	return values["MemTotal"] * 1024
}

// readMemAvailable возвращает MemAvailable в байтах.
func readMemAvailable() (int64, bool) {
	values := readMeminfo()
	available, ok := values["MemAvailable"]
	if !ok || available == 0 {
		return 0, false
	}
	return available * 1024, true
}

// readMeminfo разбирает /proc/meminfo в килобайтах.
func readMeminfo() map[string]int64 {
	values := map[string]int64{}
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return values
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[key] = value
	}
	return values
}

// readLoadPercent оценивает загрузку CPU по loadavg (приблизительно).
func readLoadPercent(cores int) (float32, bool) {
	if cores < 1 {
		return 0, false
	}
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	load, err := strconv.ParseFloat(fields[0], 32)
	if err != nil {
		return 0, false
	}
	percent := float32(load) / float32(cores) * 100
	if percent > 100 {
		percent = 100
	}
	return percent, true
}

// statfsDisk возвращает свободное место для точки монтирования.
func statfsDisk(mount string) (store.Disk, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mount, &stat); err != nil {
		return store.Disk{}, false
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	total := int64(stat.Blocks) * int64(stat.Bsize)
	if total == 0 {
		return store.Disk{}, false
	}
	return store.Disk{Mount: mount, FreeBytes: free, TotalBytes: total}, true
}
