package worker

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/private-compute/network/internal/store"
)

// nvidiaSnapshot — всё, что удалось узнать у nvidia-smi за один проход.
type nvidiaSnapshot struct {
	GPUs           []store.GPU
	CUDAVersion    string
	DriverVersion  string
	VRAMFreeBytes  *int64
	TemperatureC   *float32
	PowerWatts     *float32
	UtilizationPct *float32
}

var cudaVersionRe = regexp.MustCompile(`CUDA(?: UMD)? Version:\s*([0-9]+(?:\.[0-9]+)*)`)

const mib = 1024 * 1024

// VRAMFree возвращает свободную видеопамять в байтах.
//
// Нужна перед стартом шард-группы: видеопамять делят все процессы машины, и
// Ollama, держащая модель, оставляет новой группе меньше, чем ей нужно.
// Отдельная функция, потому что полный Collect запускает PowerShell и стоит
// сотни миллисекунд, а здесь нужен один опрос nvidia-smi.
func VRAMFree(ctx context.Context) (int64, bool) {
	snap := queryNvidia(ctx)
	if snap.VRAMFreeBytes == nil {
		return 0, false
	}
	return *snap.VRAMFreeBytes, true
}

// queryNvidia опрашивает nvidia-smi. Отсутствие утилиты или GPU — не ошибка:
// агент просто сообщит пустой список GPU.
func queryNvidia(ctx context.Context) nvidiaSnapshot {
	var snap nvidiaSnapshot

	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := quiet(exec.CommandContext(queryCtx, "nvidia-smi",
		"--query-gpu=name,memory.total,memory.used,memory.free,driver_version,temperature.gpu,power.draw,utilization.gpu",
		"--format=csv,noheader,nounits")).Output()
	if err != nil {
		return snap
	}

	var (
		driverVer string
		tempMax   float32
		powerSum  float32
		utilMax   float32
		haveTemp  bool
		havePower bool
		haveUtil  bool
		vramFree  int64
	)

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 8 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}

		gpu := store.GPU{
			Name:      fields[0],
			VRAMBytes: parseMiB(fields[1]),
			DriverVer: fields[4],
		}
		if gpu.DriverVer != "" {
			driverVer = gpu.DriverVer
		}
		snap.GPUs = append(snap.GPUs, gpu)

		if free := parseMiB(fields[3]); free > 0 {
			vramFree += free
		}
		if temp, ok := parseFloat(fields[5]); ok {
			if temp > tempMax || !haveTemp {
				tempMax = temp
			}
			haveTemp = true
		}
		if power, ok := parseFloat(fields[6]); ok {
			powerSum += power
			havePower = true
		}
		if util, ok := parseFloat(fields[7]); ok {
			if util > utilMax || !haveUtil {
				utilMax = util
			}
			haveUtil = true
		}
	}

	snap.DriverVersion = driverVer
	snap.CUDAVersion = queryCUDAVersion(ctx)
	attachComputeCapability(ctx, snap.GPUs)

	if vramFree > 0 {
		snap.VRAMFreeBytes = Ptr(vramFree)
	}
	if haveTemp {
		snap.TemperatureC = Ptr(tempMax)
	}
	if havePower {
		snap.PowerWatts = Ptr(powerSum)
	}
	if haveUtil {
		snap.UtilizationPct = Ptr(utilMax)
	}
	return snap
}

// attachComputeCapability дозапрашивает compute capability, если драйвер это умеет.
func attachComputeCapability(ctx context.Context, gpus []store.GPU) {
	if len(gpus) == 0 {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := quiet(exec.CommandContext(checkCtx, "nvidia-smi",
		"--query-gpu=compute_cap", "--format=csv,noheader")).Output()
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != len(gpus) {
		return
	}
	for i := range gpus {
		value := strings.TrimSpace(lines[i])
		if value == "" || strings.Contains(value, "N/A") || strings.Contains(value, "Field") {
			continue
		}
		gpus[i].ComputeCap = value
	}
}

// queryCUDAVersion вытаскивает версию CUDA из шапки nvidia-smi.
func queryCUDAVersion(ctx context.Context) string {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := quiet(exec.CommandContext(checkCtx, "nvidia-smi")).Output()
	if err != nil {
		return ""
	}
	match := cudaVersionRe.FindStringSubmatch(string(out))
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// parseMiB переводит значение в MiB в байты, игнорируя «[N/A]».
func parseMiB(raw string) int64 {
	value, ok := parseFloat(raw)
	if !ok || value <= 0 {
		return 0
	}
	return int64(value) * mib
}

// parseFloat разбирает число, возвращая false для «[N/A]» и пустых строк.
func parseFloat(raw string) (float32, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "N/A") || strings.Contains(raw, "Not Supported") {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 32)
	if err != nil {
		return 0, false
	}
	return float32(value), true
}
