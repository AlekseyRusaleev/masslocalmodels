// Package worker — Worker Agent: обнаружение железа, enrollment, heartbeat.
//
// Агент инициирует все соединения сам (исходящий HTTPS), не слушает порты
// и не принимает входящих подключений. Секрет устройства хранится локально
// и никогда не пишется в журнал.
package worker

import (
	"time"

	"github.com/private-compute/network/internal/store"
)

// SystemInfo — обнаруженные характеристики машины.
type SystemInfo struct {
	CPUModel  string
	CPUCores  int
	RAMBytes  int64
	OS        string
	Hostname  string
	Disks     []store.Disk
	GPUs      []store.GPU
	CUDAVer   string
	DriverVer string
}

// Telemetry — текущее состояние машины для heartbeat.
type Telemetry struct {
	CPUPercent    *float32
	RAMFreeBytes  *int64
	VRAMFreeBytes *int64
	DiskFreeBytes *int64
	TemperatureC  *float32
	PowerWatts    *float32
	ActiveJobs    *int
	Meta          map[string]any
}

// EnrollRequest — тело запроса регистрации узла.
type EnrollRequest struct {
	Token        string             `json:"token"`
	Name         string             `json:"name"`
	Hostname     string             `json:"hostname"`
	OS           string             `json:"os"`
	AgentVersion string             `json:"agent_version"`
	Hardware     store.Capabilities `json:"hardware"`
}

// EnrollResponse — ответ Control Plane на регистрацию.
type EnrollResponse struct {
	WorkerID    string    `json:"worker_id"`
	Name        string    `json:"name"`
	State       string    `json:"state"`
	TrustClass  string    `json:"trust_class"`
	EnrolledAt  time.Time `json:"enrolled_at"`
	DeviceToken string    `json:"device_token"`
	Warning     string    `json:"warning"`
}

// HeartbeatRequest — тело запроса телеметрии.
type HeartbeatRequest struct {
	State         string         `json:"state"`
	CPUPercent    *float32       `json:"cpu_percent,omitempty"`
	RAMFreeBytes  *int64         `json:"ram_free_bytes,omitempty"`
	VRAMFreeBytes *int64         `json:"vram_free_bytes,omitempty"`
	DiskFreeBytes *int64         `json:"disk_free_bytes,omitempty"`
	TemperatureC  *float32       `json:"temperature_c,omitempty"`
	PowerWatts    *float32       `json:"power_watts,omitempty"`
	ActiveJobs    *int           `json:"active_jobs,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}

// HeartbeatResponse — ответ Control Plane на heartbeat.
type HeartbeatResponse struct {
	OK           bool        `json:"ok"`
	WorkerID     string      `json:"worker_id"`
	DesiredState string      `json:"desired_state"`
	ServerTime   string      `json:"server_time"`
	IntervalSecs int         `json:"heartbeat_interval_seconds"`
	Weights      *WeightPlan `json:"weights,omitempty"`
}

// Capabilities собирает пакет возможностей из обнаруженной системы.
func (s SystemInfo) Capabilities() store.Capabilities {
	network := map[string]any{}
	if s.Hostname != "" {
		network["hostname"] = s.Hostname
	}
	return store.Capabilities{
		CPUModel:      s.CPUModel,
		CPUCores:      s.CPUCores,
		RAMBytes:      s.RAMBytes,
		GPUs:          s.GPUs,
		Disks:         s.Disks,
		CUDAVersion:   s.CUDAVer,
		DriverVersion: s.DriverVer,
		Network:       network,
	}
}

// Ptr возвращает указатель на значение (помощник для необязательных полей).
func Ptr[T any](v T) *T { return &v }
