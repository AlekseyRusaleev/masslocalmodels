//go:build windows

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/private-compute/network/internal/store"
)

type winSystem struct {
	CPU      string `json:"cpu"`
	Cores    int    `json:"cores"`
	RAMTotal int64  `json:"ram_total"`
	OS       string `json:"os"`
	Hostname string `json:"hostname"`
}

type winDisk struct {
	DeviceID   string `json:"DeviceID"`
	DriveType  int    `json:"DriveType"`
	FileSystem string `json:"FileSystem"`
	VolumeName string `json:"VolumeName"`
	FreeSpace  int64  `json:"FreeSpace"`
	Size       int64  `json:"Size"`
}

type winTelemetry struct {
	CPUPercent *float32 `json:"cpu_percent"`
	RAMFree    int64    `json:"ram_free"`
	DiskFree   int64    `json:"disk_free"`
}

// runPowerShell выполняет скрипт и возвращает stdout.
func runPowerShell(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	return quiet(cmd).Output()
}

// Discover собирает характеристики машины: CPU, память, ОС, диски, GPU.
func Discover(ctx context.Context) (SystemInfo, error) {
	info := SystemInfo{}

	discoverCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	script := `$c = Get-CimInstance Win32_Processor | Select-Object -First 1;` +
		`$o = Get-CimInstance Win32_OperatingSystem;` +
		`[pscustomobject]@{cpu=$c.Name;cores=$c.NumberOfLogicalProcessors;` +
		`ram_total=[int64]$o.TotalVisibleMemorySize*1024;` +
		`os=($o.Caption + ' build ' + $o.BuildNumber);hostname=$env:COMPUTERNAME} | ConvertTo-Json -Compress`

	out, err := runPowerShell(discoverCtx, script)
	if err != nil {
		return info, fmt.Errorf("discover system: %w", err)
	}
	var sys winSystem
	if err := json.Unmarshal(out, &sys); err != nil {
		return info, fmt.Errorf("parse system json: %w", err)
	}

	info.CPUModel = strings.TrimSpace(sys.CPU)
	info.CPUCores = sys.Cores
	info.RAMBytes = sys.RAMTotal
	info.OS = strings.TrimSpace(sys.OS)
	info.Hostname = sys.Hostname
	if info.Hostname == "" {
		info.Hostname, _ = os.Hostname()
	}

	diskScript := `Get-CimInstance Win32_LogicalDisk | ` +
		`Select-Object DeviceID,DriveType,FileSystem,VolumeName,FreeSpace,Size | ConvertTo-Json -Compress`
	if diskOut, err := runPowerShell(discoverCtx, diskScript); err == nil {
		info.Disks = parseWindowsDisks(diskOut)
	}

	snap := queryNvidia(discoverCtx)
	info.GPUs = snap.GPUs
	info.CUDAVer = snap.CUDAVersion
	info.DriverVer = snap.DriverVersion

	return info, nil
}

// parseWindowsDisks принимает и массив, и одиночный объект JSON.
// Облачные и сетевые диски отбрасываются: кэш моделей на них размещать нельзя.
func parseWindowsDisks(data []byte) []store.Disk {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}

	var items []winDisk
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(data, &items); err != nil {
			return nil
		}
	} else {
		var single winDisk
		if err := json.Unmarshal(data, &single); err != nil {
			return nil
		}
		items = append(items, single)
	}

	disks := make([]store.Disk, 0, len(items))
	for _, item := range items {
		if !isLocalWindowsDisk(item) {
			continue
		}
		kind := "local"
		if item.DriveType == 2 {
			kind = "removable"
		}
		disks = append(disks, store.Disk{
			Mount:      item.DeviceID + `\`,
			FreeBytes:  item.FreeSpace,
			TotalBytes: item.Size,
			Kind:       kind,
		})
	}
	return disks
}

// localFileSystems — файловые системы реальных локальных томов.
var localFileSystems = map[string]bool{
	"NTFS":  true,
	"ReFS":  true,
	"exFAT": true,
	"FAT32": true,
	"FAT":   true,
}

// cloudMarkers — признаки облачных/виртуальных дисков в метке тома.
var cloudMarkers = []string{
	"yandex", "google", "dropbox", "onedrive", "airlive", "cloud", "icloud", "mega",
}

// isLocalWindowsDisk отсекает сетевые, облачные и оптические диски.
func isLocalWindowsDisk(item winDisk) bool {
	if item.DeviceID == "" || item.Size == 0 {
		return false
	}
	switch item.DriveType {
	case 2, 3: // removable, fixed
	default:
		return false // 1=no root, 4=network, 5=cdrom, 6=ram
	}
	if !localFileSystems[strings.ToUpper(strings.TrimSpace(item.FileSystem))] {
		return false
	}
	label := strings.ToLower(item.VolumeName)
	for _, marker := range cloudMarkers {
		if strings.Contains(label, marker) {
			return false
		}
	}
	return true
}

// Collect собирает текущую телеметрию для heartbeat.
func Collect(ctx context.Context, disks []store.Disk) (Telemetry, error) {
	collectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	script := `$c = Get-CimInstance Win32_Processor | Select-Object -First 1;` +
		`$o = Get-CimInstance Win32_OperatingSystem;` +
		`[pscustomobject]@{cpu_percent=$c.LoadPercentage;ram_free=[int64]$o.FreePhysicalMemory*1024} | ConvertTo-Json -Compress`

	out, err := runPowerShell(collectCtx, script)
	if err != nil {
		return Telemetry{}, fmt.Errorf("collect telemetry: %w", err)
	}
	var raw winTelemetry
	if err := json.Unmarshal(out, &raw); err != nil {
		return Telemetry{}, fmt.Errorf("parse telemetry json: %w", err)
	}

	tel := Telemetry{}
	if raw.CPUPercent != nil {
		tel.CPUPercent = raw.CPUPercent
	}
	if raw.RAMFree > 0 {
		tel.RAMFreeBytes = Ptr(raw.RAMFree)
	}

	// Свободное место считаем только по локальным томам: облачные диски
	// (Yandex.Disk через AirLiveDrive и подобные) в отчёт не попадают.
	diskScript := `Get-CimInstance Win32_LogicalDisk | ` +
		`Select-Object DeviceID,DriveType,FileSystem,VolumeName,FreeSpace,Size | ConvertTo-Json -Compress`
	if diskOut, diskErr := runPowerShell(collectCtx, diskScript); diskErr == nil {
		var maxFree int64
		for _, disk := range parseWindowsDisks(diskOut) {
			if disk.FreeBytes > maxFree {
				maxFree = disk.FreeBytes
			}
		}
		if maxFree > 0 {
			tel.DiskFreeBytes = Ptr(maxFree)
		}
	}
	if raw.DiskFree > 0 {
		tel.DiskFreeBytes = Ptr(raw.DiskFree)
	}

	snap := queryNvidia(collectCtx)
	tel.VRAMFreeBytes = snap.VRAMFreeBytes
	tel.TemperatureC = snap.TemperatureC
	tel.PowerWatts = snap.PowerWatts

	meta := map[string]any{}
	if snap.UtilizationPct != nil {
		meta["gpu_utilization_percent"] = *snap.UtilizationPct
	}
	if len(disks) > 0 {
		meta["disks_reported"] = len(disks)
	}
	if len(meta) > 0 {
		tel.Meta = meta
	}
	return tel, nil
}
