package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/private-compute/network/internal/download"
)

// Release — описание сборки, которую платформа отдаёт участникам.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

// NewerVersion сообщает, что remote новее local.
// Сравниваются три числа. Непонятная версия на сервере обновлением не считается.
func NewerVersion(remote, local string) bool {
	got, ok := parseVersion(remote)
	if !ok {
		return false
	}
	have, ok := parseVersion(local)
	if !ok {
		return true
	}
	for i := 0; i < 3; i++ {
		if got[i] != have[i] {
			return got[i] > have[i]
		}
	}
	return false
}

func parseVersion(raw string) ([3]int, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "v"))
	if raw == "" {
		return [3]int{}, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// CheckRelease читает release.json с платформы.
// Второй результат — true, когда опубликованная сборка новее запущенной.
func CheckRelease(ctx context.Context, controlURL string) (Release, bool, error) {
	control := normalizeControl(controlURL)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, control+"/node/release.json", nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("User-Agent", "compute-network-node/"+AgentVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("список обновлений: %s", resp.Status)
	}
	var rel Release
	if err := json.Unmarshal(raw, &rel); err != nil {
		return Release{}, false, fmt.Errorf("список обновлений повреждён")
	}
	rel.Version = strings.TrimSpace(rel.Version)
	rel.SHA256 = strings.TrimSpace(rel.SHA256)
	rel.URL = strings.TrimSpace(rel.URL)
	if rel.URL == "" {
		rel.URL = control + "/node/compute-node.exe"
	}
	if err := validateRelease(control, rel); err != nil {
		return Release{}, false, err
	}
	return rel, NewerVersion(rel.Version, AgentVersion), nil
}

// InstallRelease скачивает сборку, сверяет хеш и заменяет запущенную программу.
// Описание читается заново прямо перед скачиванием: кнопка могла запомнить
// хеш предыдущего файла, пока на сервере его уже сменили.
func InstallRelease(ctx context.Context, _ Release, progress func(done, total int64)) error {
	control := normalizeControl("")
	rel, newer, err := CheckRelease(ctx, control)
	if err != nil {
		return err
	}
	if !newer {
		return fmt.Errorf("обновление уже установлено")
	}
	if err := validateRelease(control, rel); err != nil {
		return err
	}
	exe, err := currentExecutable()
	if err != nil {
		return err
	}
	staged := exe + ".new"
	if _, err := download.Fetch(ctx, download.Options{
		URL:         rel.URL,
		DestPath:    staged,
		ExpectedSHA: rel.SHA256,
		Retries:     3,
		Streams:     4,
		Progress:    progress,
	}); err != nil {
		return fmt.Errorf("обновление не скачалось: %w", err)
	}
	return swapExecutable(exe, staged)
}

func normalizeControl(controlURL string) string {
	control := strings.TrimRight(strings.TrimSpace(controlURL), "/")
	if control == "" {
		control = DefaultControlURL
	}
	if !strings.HasPrefix(control, "http") {
		control = "https://" + control
	}
	return control
}

func validateRelease(control string, rel Release) error {
	if _, ok := parseVersion(rel.Version); !ok {
		return fmt.Errorf("у обновления нет версии")
	}
	if len(rel.SHA256) != 64 {
		return fmt.Errorf("у обновления нет хеша")
	}
	if !releaseURLAllowed(control, rel.URL) {
		return fmt.Errorf("адрес обновления не с этой платформы")
	}
	return nil
}

func releaseURLAllowed(control, raw string) bool {
	base, err := url.Parse(control)
	if err != nil || base.Hostname() == "" {
		return false
	}
	got, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if got.Scheme != "https" || got.User != nil || got.Hostname() == "" {
		return false
	}
	return strings.EqualFold(got.Hostname(), base.Hostname())
}
