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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	APIURL        string
	APIToken      string
	VKPeerID      int64
	CPUThreshold  float64
	MemThreshold  float64
	DiskThreshold float64
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
		top, err := topCPUProcesses(5, 200*time.Millisecond)
		if err != nil {
			alerts = append(alerts, fmt.Sprintf("CPU топ: ошибка чтения (%v)", err))
		} else {
			for i, p := range top {
				alerts = append(alerts, fmt.Sprintf("CPU топ %d: PID %d %s %.1f%%", i+1, p.PID, p.Command, p.CPUPercent))
			}
		}
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

	cfg.APIURL = strings.TrimSpace(getenvDefault("API_URL", "https://acmen.ru/api/v1/vk/sendMessage"))
	cfg.APIToken = strings.TrimSpace(os.Getenv("API_TOKEN"))
	peerIDRaw := strings.TrimSpace(getenvDefault("VK_PEER_ID", "2000000008"))
	peerID, err := strconv.ParseInt(peerIDRaw, 10, 64)
	if err != nil || peerID <= 0 {
		return Config{}, errors.New("VK_PEER_ID must be a positive integer")
	}
	cfg.VKPeerID = peerID

	cfg.CPUThreshold = getenvFloat("CPU_THRESHOLD", 80)
	cfg.MemThreshold = getenvFloat("RAM_THRESHOLD", 80)
	cfg.DiskThreshold = getenvFloat("DISK_THRESHOLD", 80)

	if cfg.APIToken == "" {
		return Config{}, errors.New("API_TOKEN is required")
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

type procSample struct {
	pid     int
	command string
	total   uint64
}

type procUsage struct {
	PID        int
	Command    string
	CPUPercent float64
}

func topCPUProcesses(limit int, sampleInterval time.Duration) ([]procUsage, error) {
	if limit <= 0 {
		return nil, nil
	}
	firstTotal, err := readCPUSnapshot()
	if err != nil {
		return nil, err
	}
	first, err := readProcSamples()
	if err != nil {
		return nil, err
	}

	if sampleInterval <= 0 {
		sampleInterval = 200 * time.Millisecond
	}
	time.Sleep(sampleInterval)

	secondTotal, err := readCPUSnapshot()
	if err != nil {
		return nil, err
	}
	second, err := readProcSamples()
	if err != nil {
		return nil, err
	}

	deltaTotal := secondTotal.total - firstTotal.total
	if deltaTotal == 0 {
		return nil, errors.New("invalid cpu delta")
	}

	usages := make([]procUsage, 0, limit)
	for pid, s2 := range second {
		s1, ok := first[pid]
		if !ok {
			continue
		}
		if s2.total <= s1.total {
			continue
		}
		delta := s2.total - s1.total
		cpuPercent := (float64(delta) / float64(deltaTotal)) * 100
		if cpuPercent <= 0 {
			continue
		}
		usages = append(usages, procUsage{
			PID:        pid,
			Command:    s2.command,
			CPUPercent: cpuPercent,
		})
	}

	sort.Slice(usages, func(i, j int) bool {
		return usages[i].CPUPercent > usages[j].CPUPercent
	})
	if len(usages) > limit {
		usages = usages[:limit]
	}
	return usages, nil
}

func readProcSamples() (map[int]procSample, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	samples := make(map[int]procSample, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		statPath := "/proc/" + entry.Name() + "/stat"
		data, err := os.ReadFile(statPath)
		if err != nil {
			continue
		}
		sample, ok := parseProcStat(string(data))
		if !ok {
			continue
		}
		if cmdline := readCmdline(pid); cmdline != "" {
			sample.command = cmdline
		}
		sample.pid = pid
		samples[pid] = sample
	}
	return samples, nil
}

func parseProcStat(line string) (procSample, bool) {
	start := strings.Index(line, "(")
	end := strings.LastIndex(line, ")")
	if start == -1 || end == -1 || end <= start {
		return procSample{}, false
	}
	command := strings.TrimSpace(line[start+1 : end])
	rest := strings.Fields(line[end+1:])
	if len(rest) < 13 {
		return procSample{}, false
	}
	utime, err := strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return procSample{}, false
	}
	stime, err := strconv.ParseUint(rest[12], 10, 64)
	if err != nil {
		return procSample{}, false
	}
	return procSample{
		command: command,
		total:   utime + stime,
	}, true
}

func readCmdline(pid int) string {
	path := fmt.Sprintf("/proc/%d/cmdline", pid)
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	for i := range data {
		if data[i] == 0 {
			data[i] = ' '
		}
	}
	cmd := strings.TrimSpace(string(data))
	if cmd == "" {
		return ""
	}
	const maxLen = 160
	if len(cmd) > maxLen {
		return cmd[:maxLen] + "..."
	}
	return cmd
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
	payload := map[string]any{
		"peer_id": cfg.VKPeerID,
		"message": message,
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var apiResp struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if len(body) > 0 && json.Unmarshal(body, &apiResp) == nil {
		if apiResp.Success != nil && !*apiResp.Success {
			if apiResp.Message != "" {
				return errors.New(apiResp.Message)
			}
			return errors.New("api returned success=false")
		}
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
