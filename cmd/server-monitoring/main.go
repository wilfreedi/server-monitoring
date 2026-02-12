package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	APIURL          string
	APIToken        string
	ChatID          string
	MessageThreadID string
	CPUThreshold    float64
	MemThreshold    float64
	DiskThreshold   float64
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	alerts := make([]string, 0, 8)

	cpuUsage, err := readCPUUsage(300 * time.Millisecond)
	if err != nil {
		alerts = append(alerts, fmt.Sprintf("CPU: ошибка чтения (%v)", err))
	} else if cpuUsage >= cfg.CPUThreshold {
		alerts = append(alerts, fmt.Sprintf("CPU: %.1f%% (порог %.0f%%)", cpuUsage, cfg.CPUThreshold))
	}

	memUsage, err := readMemUsage()
	if err != nil {
		alerts = append(alerts, fmt.Sprintf("RAM: ошибка чтения (%v)", err))
	} else if memUsage >= cfg.MemThreshold {
		alerts = append(alerts, fmt.Sprintf("RAM: %.1f%% (порог %.0f%%)", memUsage, cfg.MemThreshold))
	}

	diskUsages, err := readDiskUsages(defaultDiskFSTypeExclude())
	if err != nil {
		alerts = append(alerts, fmt.Sprintf("Диск: ошибка чтения (%v)", err))
	} else {
		for _, du := range diskUsages {
			if du.UsedPercent >= cfg.DiskThreshold {
				alerts = append(alerts, fmt.Sprintf(
					"Диск %s: %.1f%% (использовано %s из %s, порог %.0f%%)",
					du.MountPoint,
					du.UsedPercent,
					formatBytes(du.UsedBytes),
					formatBytes(du.TotalBytes),
					cfg.DiskThreshold,
				))
			}
		}
	}

	if len(alerts) == 0 {
		return
	}

	message := buildMessage(alerts)
	if err := sendAlert(ctx, cfg, message); err != nil {
		log.Fatal(err)
	}
}

func loadConfig() (Config, error) {
	cfg := Config{}

	cfg.APIURL = strings.TrimSpace(getenvDefault("API_URL", "https://acmen.ru/api/v1/telegram/"))
	cfg.APIToken = strings.TrimSpace(os.Getenv("API_TOKEN"))
	cfg.ChatID = strings.TrimSpace(os.Getenv("CHAT_ID"))
	cfg.MessageThreadID = strings.TrimSpace(os.Getenv("MESSAGE_THREAD_ID"))

	cfg.CPUThreshold = getenvFloat("CPU_THRESHOLD", 80)
	cfg.MemThreshold = getenvFloat("RAM_THRESHOLD", 80)
	cfg.DiskThreshold = getenvFloat("DISK_THRESHOLD", 80)

	if cfg.APIToken == "" {
		return Config{}, errors.New("API_TOKEN is required")
	}
	if cfg.ChatID == "" {
		return Config{}, errors.New("CHAT_ID is required")
	}
	return cfg, nil
}

func getenvDefault(key, def string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	return val
}

func getenvFloat(key string, def float64) float64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return def
	}
	return parsed
}

type DiskUsage struct {
	MountPoint  string
	UsedPercent float64
	UsedBytes   uint64
	TotalBytes  uint64
}

func readDiskUsages(fstypeExclude map[string]struct{}) ([]DiskUsage, error) {
	file, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	seen := make(map[string]struct{})
	usages := make([]DiskUsage, 0, 16)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		mountPoint := fields[1]
		fsType := fields[2]

		if _, ok := fstypeExclude[fsType]; ok {
			continue
		}
		if _, ok := seen[mountPoint]; ok {
			continue
		}
		seen[mountPoint] = struct{}{}

		var stat syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &stat); err != nil {
			continue
		}
		if stat.Blocks == 0 {
			continue
		}

		total := stat.Blocks * uint64(stat.Bsize)
		avail := stat.Bavail * uint64(stat.Bsize)
		used := total - avail
		usedPercent := (float64(used) / float64(total)) * 100

		usages = append(usages, DiskUsage{
			MountPoint:  mountPoint,
			UsedPercent: usedPercent,
			UsedBytes:   used,
			TotalBytes:  total,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return usages, nil
}

func defaultDiskFSTypeExclude() map[string]struct{} {
	return map[string]struct{}{
		"proc":        {},
		"sysfs":       {},
		"devtmpfs":    {},
		"tmpfs":       {},
		"devpts":      {},
		"cgroup":      {},
		"cgroup2":     {},
		"mqueue":      {},
		"hugetlbfs":   {},
		"debugfs":     {},
		"tracefs":     {},
		"pstore":      {},
		"securityfs":  {},
		"rpc_pipefs":  {},
		"configfs":    {},
		"fusectl":     {},
		"overlay":     {},
		"squashfs":    {},
		"autofs":      {},
		"binfmt_misc": {},
		"nsfs":        {},
		"ramfs":       {},
		"efivarfs":    {},
		"bpf":         {},
		"selinuxfs":   {},
		"cgroupfs":    {},
	}
}

func readCPUUsage(sampleInterval time.Duration) (float64, error) {
	first, err := readCPUSnapshot()
	if err != nil {
		return 0, err
	}
	if sampleInterval <= 0 {
		sampleInterval = 200 * time.Millisecond
	}
	time.Sleep(sampleInterval)
	second, err := readCPUSnapshot()
	if err != nil {
		return 0, err
	}

	deltaTotal := second.total - first.total
	deltaIdle := second.idle - first.idle
	if deltaTotal <= 0 {
		return 0, errors.New("invalid cpu delta")
	}
	usage := (float64(deltaTotal-deltaIdle) / float64(deltaTotal)) * 100
	if usage < 0 {
		usage = 0
	}
	if usage > 100 {
		usage = 100
	}
	return usage, nil
}

type cpuSnapshot struct {
	idle  uint64
	total uint64
}

func readCPUSnapshot() (cpuSnapshot, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return cpuSnapshot{}, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return cpuSnapshot{}, err
	}
	if !strings.HasPrefix(line, "cpu ") {
		return cpuSnapshot{}, errors.New("cpu line not found")
	}
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return cpuSnapshot{}, errors.New("invalid cpu fields")
	}

	var values []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuSnapshot{}, err
		}
		values = append(values, v)
	}

	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	var total uint64
	for _, v := range values {
		total += v
	}
	return cpuSnapshot{idle: idle, total: total}, nil
}

func readMemUsage() (float64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var total, available uint64
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseMeminfoValue(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			available = parseMeminfoValue(line)
		}
		if total > 0 && available > 0 {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if total == 0 || available == 0 {
		return 0, errors.New("meminfo values not found")
	}
	used := total - available
	usage := (float64(used) / float64(total)) * 100
	return usage, nil
}

func parseMeminfoValue(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return v * 1024
}

func buildMessage(alerts []string) string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	lines := make([]string, 0, len(alerts)+3)
	lines = append(lines, fmt.Sprintf("Мониторинг сервера: %s", hostname))
	lines = append(lines, fmt.Sprintf("Время: %s", time.Now().Format("2006-01-02 15:04:05 MST")))
	lines = append(lines, "Проблемы:")
	for _, a := range alerts {
		lines = append(lines, "- "+a)
	}
	return strings.Join(lines, "\n")
}

func sendAlert(ctx context.Context, cfg Config, message string) error {
	payload := map[string]string{
		"chat_id": cfg.ChatID,
		"message": message,
	}
	if cfg.MessageThreadID != "" {
		payload["message_thread_id"] = cfg.MessageThreadID
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	bearer := fmt.Sprintf("Bearer %s", cfg.APIToken)
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func formatBytes(v uint64) string {
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := uint64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	value := float64(v) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	if exp >= len(suffix) {
		exp = len(suffix) - 1
	}
	return fmt.Sprintf("%.1f %s", value, suffix[exp])
}
