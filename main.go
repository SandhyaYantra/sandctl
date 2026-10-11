package main

import (
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
)

type screenState int

const (
	mainMenuScreen screenState = iota
	wifiScreen
	bluetoothScreen
	monitorScreen
)

type wifiAction int

const (
	wifiConnect wifiAction = iota
	wifiDisconnect
	wifiShowPassword
)

type WiFiNetwork struct {
	InUseMark string
	BSSID     string
	SSID      string
	Mode      string
	Band      string
	Channel   string
	Rate      string
	Signal    int
	Bars      string
	Security  string
	InUse     bool
}

type WifiNetwork = WiFiNetwork

type BluetoothDevice struct {
	MACAddress string
	Name       string
	Connected  bool
}

type networkCounters struct {
	RX uint64
	TX uint64
}

type trafficRates struct {
	RXBytesPerSecond float64
	TXBytesPerSecond float64
	TopRX            float64
	TopTX            float64
	TotalRX          uint64
	TotalTX          uint64
}

type diskSnapshot struct {
	Mountpoint string
	Filesystem string
	Total      uint64
	Used       uint64
	Free       uint64
}

type networkDeviceStatus struct {
	Device     string
	Type       string
	State      string
	Connection string
}

type systemSnapshot struct {
	CPUPercent      []float64
	RAMTotal        uint64
	RAMUsed         uint64
	RAMAvailable    uint64
	RAMCached       uint64
	RAMFree         uint64
	RootDiskTotal   uint64
	RootDiskUsed    uint64
	RootDiskFree    uint64
	Disks           []diskSnapshot
	Load1           float64
	Load5           float64
	Load15          float64
	BatteryPercent  int
	BatteryOK       bool
	TemperatureC    float64
	TemperatureOK   bool
	GPUUtilPercent  int
	GPUUtilOK       bool
	GPUUtilEstimate bool
	GPUFrequencyMHz int
	GPUFrequencyOK  bool
	GPUPowerWatts   float64
	GPUPowerOK      bool
	Interface       string
	LocalIP         string
	NetworkDevices  []networkDeviceStatus
	Traffic         trafficRates
	NetworkOK       bool
	SampledAt       time.Time
}

type Model struct {
	menu   []string
	cursor int
	screen screenState
	width  int
	height int

	volumePercent      int
	volumeAvailable    bool
	speakerMuted       bool
	micMuted           bool
	audioStateOK       bool
	brightness         int
	brightnessOK       bool
	powerProfile       string
	powerProfileOK     bool
	powerProfileCursor int
	lastError          string

	wifiNetworks []WiFiNetwork
	wifiCursor   int
	wifiOffset   int
	wifiLoading  bool
	wifiError    string
	wifiDialog   bool
	wifiAction   int
	wifiBusy     bool
	wifiMessage  string
	wifiSelected string

	bluetoothDevices []BluetoothDevice
	bluetoothCursor  int
	bluetoothLoading bool
	bluetoothMessage string

	snapshot      systemSnapshot
	hasSnapshot   bool
	previousIO    networkCounters
	previousIOAt  time.Time
	hasPreviousIO bool
	ramHistory    []int
	rxHistory     []float64
	txHistory     []float64
}

type volumeMsg struct {
	percent int
	err     error
}

type audioStateMsg struct {
	volume      int
	speakerMute bool
	micMute     bool
	err         error
}

type brightnessMsg struct {
	percent int
	err     error
}

type powerProfileMsg struct {
	profile string
	err     error
}

type systemMsg struct {
	snapshot systemSnapshot
	err      error
}

type wifiScanMsg struct {
	networks []WiFiNetwork
	err      error
}

type wifiActionMsg struct {
	action wifiAction
	ssid   string
	output string
	err    error
}

type bluetoothScanMsg struct {
	devices []BluetoothDevice
	err     error
}

type bluetoothConnectMsg struct {
	device    BluetoothDevice
	connected bool
	err       error
}

type tickMsg struct{}

var powerProfiles = []string{"balanced", "performance", "power-saver"}

func profileIndex(profile string) int {
	for index, candidate := range powerProfiles {
		if candidate == profile {
			return index
		}
	}
	return 0
}

func initialModel() Model {
	return Model{
		width:  80,
		height: 24,
		menu:   []string{"Audio Volume", "Brightness", "Wi-Fi", "Bluetooth", "Microphone", "Power Profile", "System Monitor"},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(readAudioStateCmd(), readBrightnessCmd(), readPowerProfileCmd())
}

func readAudioStateCmd() tea.Cmd {
	return func() tea.Msg {
		volumeOutput, err := exec.Command("wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@").CombinedOutput()
		if err != nil {
			return audioStateMsg{err: fmt.Errorf("wpctl get-volume (speaker): %w: %s", err, strings.TrimSpace(string(volumeOutput)))}
		}
		volume, err := parseVolume(string(volumeOutput))
		if err != nil {
			return audioStateMsg{err: err}
		}
		micOutput, err := exec.Command("wpctl", "get-volume", "@DEFAULT_AUDIO_SOURCE@").CombinedOutput()
		if err != nil {
			return audioStateMsg{err: fmt.Errorf("wpctl get-volume (microphone): %w: %s", err, strings.TrimSpace(string(micOutput)))}
		}
		_, micErr := parseVolume(string(micOutput))
		if micErr != nil {
			return audioStateMsg{err: micErr}
		}
		return audioStateMsg{
			volume: volume, speakerMute: strings.Contains(string(volumeOutput), "[MUTED]"),
			micMute: strings.Contains(string(micOutput), "[MUTED]"),
		}
	}
}

func toggleMuteCmd(target string) tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("wpctl", "set-mute", target, "toggle").CombinedOutput()
		if err != nil {
			return audioStateMsg{err: fmt.Errorf("wpctl set-mute: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return readAudioStateCmd()()
	}
}

func readVolumeCmd() tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@").CombinedOutput()
		if err != nil {
			return volumeMsg{err: fmt.Errorf("wpctl get-volume: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		percent, err := parseVolume(string(output))
		return volumeMsg{percent: percent, err: err}
	}
}

func parseVolume(output string) (int, error) {
	fields := strings.Fields(output)
	for index, field := range fields {
		if strings.HasPrefix(field, "Volume:") {
			valueText := strings.TrimPrefix(field, "Volume:")
			if valueText == "" && index+1 < len(fields) {
				valueText = fields[index+1]
			}
			value, err := strconv.ParseFloat(valueText, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return 0, fmt.Errorf("invalid wpctl output %q", output)
			}
			return int(math.Round(value * 100)), nil
		}
	}
	return 0, fmt.Errorf("volume missing from wpctl output %q", output)
}

func setVolumeCmd(percent int) tea.Cmd {
	return func() tea.Msg {
		percent = min(100, max(0, percent))
		output, err := exec.Command("wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%d%%", percent), "--limit", "1.0").CombinedOutput()
		if err != nil {
			return volumeMsg{err: fmt.Errorf("wpctl set-volume: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return readVolumeCmd()()
	}
}

func readPowerProfileCmd() tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("powerprofilesctl", "get").CombinedOutput()
		if err != nil {
			return powerProfileMsg{err: fmt.Errorf("powerprofilesctl get: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return powerProfileMsg{profile: strings.TrimSpace(string(output))}
	}
}

func setPowerProfileCmd(profile string) tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("powerprofilesctl", "set", profile).CombinedOutput()
		if err != nil {
			return powerProfileMsg{err: fmt.Errorf("powerprofilesctl set: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return readPowerProfileCmd()()
	}
}

func readBrightnessCmd() tea.Cmd {
	return func() tea.Msg {
		current, err := exec.Command("brightnessctl", "g").Output()
		if err != nil {
			return brightnessMsg{err: fmt.Errorf("brightnessctl g: %w", err)}
		}
		maximum, err := exec.Command("brightnessctl", "m").Output()
		if err != nil {
			return brightnessMsg{err: fmt.Errorf("brightnessctl m: %w", err)}
		}
		percent, err := parseBrightness(string(current), string(maximum))
		return brightnessMsg{percent: percent, err: err}
	}
}

func parseBrightness(current, maximum string) (int, error) {
	currentValue, currentErr := strconv.ParseUint(strings.TrimSpace(current), 10, 64)
	maximumValue, maximumErr := strconv.ParseUint(strings.TrimSpace(maximum), 10, 64)
	if currentErr != nil || maximumErr != nil || maximumValue == 0 || currentValue > maximumValue {
		return 0, fmt.Errorf("invalid brightness values current=%q max=%q", current, maximum)
	}
	return int(math.Round(float64(currentValue) / float64(maximumValue) * 100)), nil
}

func setBrightnessCmd(change string) tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("brightnessctl", "set", "5%"+change).CombinedOutput()
		if err != nil {
			return brightnessMsg{err: fmt.Errorf("brightnessctl set: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return readBrightnessCmd()()
	}
}

func readSystemCmd() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := collectSystemSnapshot()
		return systemMsg{snapshot: snapshot, err: err}
	}
}

func collectSystemSnapshot() (systemSnapshot, error) {
	var snapshot systemSnapshot
	snapshot.SampledAt = time.Now()

	perCore, cpuErr := cpu.Percent(0, true)
	if cpuErr == nil {
		snapshot.CPUPercent = perCore
	}
	memory, memoryErr := mem.VirtualMemory()
	if memoryErr == nil && memory != nil {
		snapshot.RAMTotal = memory.Total
		snapshot.RAMUsed = memory.Used
		snapshot.RAMAvailable = memory.Available
		snapshot.RAMFree = memory.Free
		snapshot.RAMCached = memory.Cached
	}
	partitions, partitionErr := disk.Partitions(false)
	var diskErrors []string
	if partitionErr != nil {
		diskErrors = append(diskErrors, partitionErr.Error())
	} else {
		for _, partition := range partitions {
			if !isPhysicalDiskPartition(partition) {
				continue
			}
			usage, err := disk.Usage(partition.Mountpoint)
			if err != nil || usage == nil || usage.Total == 0 {
				if err != nil {
					diskErrors = append(diskErrors, fmt.Sprintf("%s: %v", partition.Mountpoint, err))
				}
				continue
			}
			entry := diskSnapshot{Mountpoint: partition.Mountpoint, Filesystem: partition.Fstype, Total: usage.Total, Used: usage.Used, Free: usage.Free}
			snapshot.Disks = append(snapshot.Disks, entry)
			if partition.Mountpoint == "/" {
				snapshot.RootDiskTotal, snapshot.RootDiskUsed, snapshot.RootDiskFree = usage.Total, usage.Used, usage.Free
			}
		}
	}
	if averages, err := load.Avg(); err == nil && averages != nil {
		snapshot.Load1, snapshot.Load5, snapshot.Load15 = averages.Load1, averages.Load5, averages.Load15
	}
	if interfaces, err := gnet.IOCounters(true); err == nil {
		if active := selectNetworkInterface(interfaces); active != nil {
			snapshot.Interface = active.Name
			snapshot.Traffic.TotalRX = active.BytesRecv
			snapshot.Traffic.TotalTX = active.BytesSent
			snapshot.NetworkOK = true
			snapshot.LocalIP = localIPForInterface(active.Name)
		}
	}
	if output, err := exec.Command("nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device").Output(); err == nil {
		snapshot.NetworkDevices, _ = parseNetworkDeviceStatuses(string(output))
	}
	snapshot.BatteryPercent, snapshot.BatteryOK = readBattery()
	snapshot.TemperatureC, snapshot.TemperatureOK = readTemperature()
	snapshot.GPUUtilPercent, snapshot.GPUUtilOK, snapshot.GPUUtilEstimate, snapshot.GPUFrequencyMHz, snapshot.GPUFrequencyOK, snapshot.GPUPowerWatts, snapshot.GPUPowerOK = readGPU()

	if cpuErr != nil && memoryErr != nil && partitionErr != nil {
		return snapshot, fmt.Errorf("system metrics unavailable: CPU: %v; memory: %v; disks: %s", cpuErr, memoryErr, strings.Join(diskErrors, "; "))
	}
	return snapshot, nil
}

func parseNetworkDeviceStatuses(output string) ([]networkDeviceStatus, error) {
	var devices []networkDeviceStatus
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := parseColonFields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("invalid nmcli device row %q", line)
		}
		devices = append(devices, networkDeviceStatus{
			Device: fields[0], Type: fields[1], State: fields[2], Connection: fields[3],
		})
	}
	return devices, nil
}

func isPhysicalDiskPartition(partition disk.PartitionStat) bool {
	filesystem := strings.ToLower(partition.Fstype)
	switch filesystem {
	case "", "proc", "sysfs", "devtmpfs", "tmpfs", "squashfs", "overlay", "autofs", "tracefs", "cgroup", "cgroup2", "fusectl", "nsfs", "ramfs", "configfs", "debugfs", "pstore", "mqueue", "securityfs", "efivarfs", "bpf", "binfmt_misc":
		return false
	}
	device := strings.ToLower(filepath.Base(partition.Device))
	if strings.HasPrefix(device, "loop") || strings.HasPrefix(device, "ram") || strings.HasPrefix(device, "zram") {
		return false
	}
	return partition.Mountpoint != ""
}

func selectNetworkInterface(interfaces []gnet.IOCountersStat) *gnet.IOCountersStat {
	for index := range interfaces {
		candidate := &interfaces[index]
		if candidate.Name == "lo" || candidate.BytesRecv == 0 && candidate.BytesSent == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/class/net", candidate.Name, "wireless")); err == nil {
			return candidate
		}
	}
	for index := range interfaces {
		candidate := &interfaces[index]
		if candidate.Name != "lo" && (candidate.BytesRecv > 0 || candidate.BytesSent > 0) {
			return candidate
		}
	}
	return nil
}

func localIPForInterface(name string) string {
	interfaceInfo, err := net.InterfaceByName(name)
	if err != nil {
		return "N/A"
	}
	addresses, err := interfaceInfo.Addrs()
	if err != nil {
		return "N/A"
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
			return ip.String()
		}
	}
	return "N/A"
}

func readBattery() (int, bool) {
	for _, path := range []string{"/sys/class/power_supply/BAT0/capacity", "/sys/class/power_supply/BAT1/capacity"} {
		if data, err := os.ReadFile(path); err == nil {
			value, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && value >= 0 && value <= 100 {
				return value, true
			}
		}
	}
	return 0, false
}

func readTemperature() (float64, bool) {
	paths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	var total float64
	var count int
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		if value > 1000 {
			value /= 1000
		}
		if value >= -20 && value <= 150 {
			total += value
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return total / float64(count), true
}

func readGPU() (utilization int, utilOK, estimated bool, frequency int, frequencyOK bool, power float64, powerOK bool) {
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]")
	for _, card := range cards {
		frequency, frequencyOK = readInteger(filepath.Join(card, "gt_cur_freq_mhz"))
		maximum, maximumOK := readInteger(filepath.Join(card, "gt_max_freq_mhz"))
		if frequencyOK && maximumOK && maximum > 0 {
			utilization = min(100, int(math.Round(float64(frequency)/float64(maximum)*100)))
			utilOK, estimated = true, true
		}
		for _, path := range mustGlob(filepath.Join(card, "device/hwmon/hwmon*/power*_average")) {
			if data, err := os.ReadFile(path); err == nil {
				value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
				if err == nil && value >= 0 {
					power, powerOK = value/1_000_000, true
					break
				}
			}
		}
		if utilOK || frequencyOK || powerOK {
			break
		}
	}
	return
}

func bytesToGiB(value uint64) float64 {
	return float64(value) / (1024 * 1024 * 1024)
}

func readInteger(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return value, err == nil && value >= 0
}

func mustGlob(pattern string) []string {
	paths, _ := filepath.Glob(pattern)
	return paths
}

func parseWiFiNetworks(output string) ([]WiFiNetwork, error) {
	var networks []WiFiNetwork
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := parseColonFields(line)
		if len(fields) == 4 {
			signal, err := strconv.Atoi(fields[3])
			if err != nil {
				return nil, fmt.Errorf("invalid Wi-Fi signal in %q", line)
			}
			networks = append(networks, WiFiNetwork{
				InUseMark: fields[0], SSID: fields[1], Security: fields[2], Signal: signal, InUse: fields[0] == "*",
			})
			continue
		}
		if len(fields) != 10 {
			return nil, fmt.Errorf("invalid nmcli row %q", line)
		}
		signal, err := strconv.Atoi(fields[7])
		if err != nil {
			return nil, fmt.Errorf("invalid Wi-Fi signal in %q", line)
		}
		networks = append(networks, WiFiNetwork{
			InUseMark: fields[0], BSSID: fields[1], SSID: fields[2], Mode: fields[3], Band: fields[4],
			Channel: fields[5], Rate: fields[6], Signal: signal, Bars: fields[8], Security: fields[9], InUse: fields[0] == "*",
		})
	}
	return networks, nil
}

func parseColonFields(line string) []string {
	fields := make([]string, 0, 4)
	var field strings.Builder
	escaped := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		if escaped {
			field.WriteByte(character)
			escaped = false
		} else if character == '\\' {
			escaped = true
		} else if character == ':' {
			fields = append(fields, field.String())
			field.Reset()
		} else {
			field.WriteByte(character)
		}
	}
	if escaped {
		field.WriteByte('\\')
	}
	return append(fields, field.String())
}

func scanWiFiCmd() tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("nmcli", "-t", "-f", "IN-USE,BSSID,SSID,MODE,BAND,CHAN,RATE,SIGNAL,BARS,SECURITY", "dev", "wifi").CombinedOutput()
		if err != nil {
			return wifiScanMsg{err: fmt.Errorf("nmcli Wi-Fi scan: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		networks, err := parseWiFiNetworks(string(output))
		return wifiScanMsg{networks: networks, err: err}
	}
}

func wifiActionCmd(action wifiAction, ssid string) tea.Cmd {
	return func() tea.Msg {
		var command *exec.Cmd
		switch action {
		case wifiConnect:
			command = exec.Command("nmcli", "dev", "wifi", "connect", ssid)
		case wifiDisconnect:
			command = exec.Command("nmcli", "connection", "down", "id", ssid)
		case wifiShowPassword:
			command = exec.Command("nmcli", "dev", "wifi", "show-password")
		default:
			return wifiActionMsg{action: action, ssid: ssid, err: fmt.Errorf("unknown Wi-Fi action")}
		}
		output, err := command.CombinedOutput()
		if err != nil {
			if action == wifiShowPassword {
				err = fmt.Errorf("saved password unavailable: %s", strings.TrimSpace(string(output)))
			} else {
				err = fmt.Errorf("nmcli: %w: %s", err, strings.TrimSpace(string(output)))
			}
			return wifiActionMsg{action: action, ssid: ssid, err: err}
		}
		if action == wifiShowPassword && strings.TrimSpace(string(output)) == "" {
			return wifiActionMsg{action: action, ssid: ssid, err: fmt.Errorf("no saved password was returned")}
		}
		return wifiActionMsg{action: action, ssid: ssid, output: strings.TrimSpace(string(output))}
	}
}

func scanBluetoothCmd() tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("bluetoothctl", "devices", "Paired").CombinedOutput()
		if err != nil {
			return bluetoothScanMsg{err: fmt.Errorf("bluetoothctl devices: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		var devices []BluetoothDevice
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "Device" {
				device := BluetoothDevice{MACAddress: fields[1], Name: strings.Join(fields[2:], " ")}
				info, infoErr := exec.Command("bluetoothctl", "info", device.MACAddress).Output()
				device.Connected = infoErr == nil && strings.Contains(string(info), "Connected: yes")
				devices = append(devices, device)
			}
		}
		return bluetoothScanMsg{devices: devices}
	}
}

func connectBluetoothCmd(device BluetoothDevice) tea.Cmd {
	return func() tea.Msg {
		verb := "connect"
		if device.Connected {
			verb = "disconnect"
		}
		output, err := exec.Command("bluetoothctl", verb, device.MACAddress).CombinedOutput()
		if err != nil {
			return bluetoothConnectMsg{device: device, err: fmt.Errorf("bluetoothctl %s: %w: %s", verb, err, strings.TrimSpace(string(output)))}
		}
		device.Connected = !device.Connected
		return bluetoothConnectMsg{device: device, connected: device.Connected}
	}
}

func systemTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case tea.KeyMsg:
		if message.String() == "ctrl+c" || m.screen == mainMenuScreen && message.String() == "q" {
			return m, tea.Quit
		}
		switch m.screen {
		case wifiScreen:
			return m.updateWiFi(message)
		case bluetoothScreen:
			return m.updateBluetooth(message)
		case monitorScreen:
			if message.String() == "esc" {
				m.screen = mainMenuScreen
			}
			return m, nil
		default:
			return m.updateMainMenu(message)
		}
	case volumeMsg:
		if message.err == nil {
			m.volumePercent, m.volumeAvailable = min(100, max(0, message.percent)), true
		} else {
			m.lastError = message.err.Error()
		}
	case audioStateMsg:
		if message.err == nil {
			m.volumePercent, m.volumeAvailable = min(100, max(0, message.volume)), true
			m.speakerMuted, m.micMuted, m.audioStateOK = message.speakerMute, message.micMute, true
		} else {
			m.lastError = message.err.Error()
		}
	case brightnessMsg:
		if message.err == nil {
			m.brightness, m.brightnessOK = message.percent, true
		} else {
			m.lastError = message.err.Error()
		}
	case powerProfileMsg:
		if message.err == nil {
			m.powerProfile, m.powerProfileOK = message.profile, true
			m.powerProfileCursor = profileIndex(message.profile)
		} else {
			m.lastError = message.err.Error()
		}
	case systemMsg:
		if message.err == nil {
			m.applySnapshot(message.snapshot)
		} else {
			m.lastError = message.err.Error()
		}
		if m.screen == monitorScreen {
			return m, systemTickCmd()
		}
	case tickMsg:
		if m.screen == monitorScreen {
			return m, readSystemCmd()
		}
	case wifiScanMsg:
		m.wifiLoading = false
		if message.err != nil {
			m.wifiError = message.err.Error()
		} else {
			m.wifiError = ""
			m.wifiNetworks = message.networks
			m.wifiCursor = min(m.wifiCursor, max(0, len(m.wifiNetworks)-1))
			m.wifiOffset = 0
		}
	case wifiActionMsg:
		m.wifiBusy = false
		if message.err != nil {
			m.wifiMessage = message.err.Error()
			return m, nil
		}
		if message.action == wifiShowPassword {
			m.wifiMessage = message.output
			return m, nil
		}
		m.wifiDialog = false
		m.wifiMessage = strings.TrimSpace(message.output)
		return m, scanWiFiCmd()
	case bluetoothScanMsg:
		m.bluetoothLoading = false
		if message.err != nil {
			m.bluetoothMessage = message.err.Error()
		} else {
			m.bluetoothDevices = message.devices
			m.bluetoothCursor = min(m.bluetoothCursor, max(0, len(m.bluetoothDevices)-1))
		}
	case bluetoothConnectMsg:
		if message.err != nil {
			m.bluetoothMessage = message.err.Error()
		} else {
			for index := range m.bluetoothDevices {
				if m.bluetoothDevices[index].MACAddress == message.device.MACAddress {
					m.bluetoothDevices[index].Connected = message.connected
				}
			}
			state := "Connected to "
			if !message.connected {
				state = "Disconnected from "
			}
			m.bluetoothMessage = state + message.device.Name
		}
	}
	return m, nil
}

func (m *Model) applySnapshot(snapshot systemSnapshot) {
	if snapshot.NetworkOK && m.hasPreviousIO {
		elapsed := snapshot.SampledAt.Sub(m.previousIOAt).Seconds()
		if elapsed > 0 && snapshot.Interface == m.snapshot.Interface {
			if snapshot.Traffic.TotalRX >= m.previousIO.RX {
				snapshot.Traffic.RXBytesPerSecond = float64(snapshot.Traffic.TotalRX-m.previousIO.RX) / elapsed
			}
			if snapshot.Traffic.TotalTX >= m.previousIO.TX {
				snapshot.Traffic.TXBytesPerSecond = float64(snapshot.Traffic.TotalTX-m.previousIO.TX) / elapsed
			}
		}
		snapshot.Traffic.TopRX = math.Max(m.snapshot.Traffic.TopRX, snapshot.Traffic.RXBytesPerSecond)
		snapshot.Traffic.TopTX = math.Max(m.snapshot.Traffic.TopTX, snapshot.Traffic.TXBytesPerSecond)
	}
	if snapshot.NetworkOK {
		m.previousIO = networkCounters{RX: snapshot.Traffic.TotalRX, TX: snapshot.Traffic.TotalTX}
		m.previousIOAt = snapshot.SampledAt
		m.hasPreviousIO = true
		m.rxHistory = append(m.rxHistory, snapshot.Traffic.RXBytesPerSecond)
		m.txHistory = append(m.txHistory, snapshot.Traffic.TXBytesPerSecond)
		if len(m.rxHistory) > 32 {
			m.rxHistory = m.rxHistory[len(m.rxHistory)-32:]
			m.txHistory = m.txHistory[len(m.txHistory)-32:]
		}
	}
	if snapshot.RAMTotal > 0 {
		usedPercent := int(math.Round(float64(snapshot.RAMUsed) / float64(snapshot.RAMTotal) * 100))
		m.ramHistory = append(m.ramHistory, usedPercent)
		if len(m.ramHistory) > 40 {
			m.ramHistory = m.ramHistory[len(m.ramHistory)-40:]
		}
	}
	m.snapshot = snapshot
	m.hasSnapshot = true
}

func (m Model) updateMainMenu(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(m.menu)-1, m.cursor+1)
	case "right", "+":
		if m.cursor == 0 {
			m.volumePercent = min(100, m.volumePercent+5)
			m.volumeAvailable = true
			return m, setVolumeCmd(m.volumePercent)
		}
		if m.cursor == 1 {
			m.brightness = min(100, m.brightness+5)
			m.brightnessOK = true
			return m, setBrightnessCmd("+")
		}
		if m.cursor == 5 {
			m.powerProfileCursor = (m.powerProfileCursor + 1) % len(powerProfiles)
		}
	case "left", "-":
		if m.cursor == 0 {
			m.volumePercent = max(0, m.volumePercent-5)
			m.volumeAvailable = true
			return m, setVolumeCmd(m.volumePercent)
		}
		if m.cursor == 1 {
			m.brightness = max(0, m.brightness-5)
			m.brightnessOK = true
			return m, setBrightnessCmd("-")
		}
		if m.cursor == 5 {
			m.powerProfileCursor = (m.powerProfileCursor + len(powerProfiles) - 1) % len(powerProfiles)
		}
	case "enter":
		if m.cursor == 0 {
			return m, toggleMuteCmd("@DEFAULT_AUDIO_SINK@")
		}
		if m.cursor == 2 {
			m.screen, m.wifiLoading, m.wifiError = wifiScreen, true, ""
			return m, scanWiFiCmd()
		}
		if m.cursor == 3 {
			m.screen, m.bluetoothLoading = bluetoothScreen, true
			return m, scanBluetoothCmd()
		}
		if m.cursor == 4 {
			return m, toggleMuteCmd("@DEFAULT_AUDIO_SOURCE@")
		}
		if m.cursor == 5 {
			return m, setPowerProfileCmd(powerProfiles[m.powerProfileCursor])
		}
		if m.cursor == 6 {
			m.screen = monitorScreen
			return m, readSystemCmd()
		}
	}
	return m, nil
}

func (m Model) updateWiFi(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.wifiDialog {
		switch message.String() {
		case "esc":
			if !m.wifiBusy {
				m.wifiDialog = false
				m.wifiMessage = ""
			}
		case "up", "k":
			if !m.wifiBusy {
				m.wifiAction = max(0, m.wifiAction-1)
			}
		case "down", "j":
			if !m.wifiBusy {
				m.wifiAction = min(2, m.wifiAction+1)
			}
		case "enter":
			if !m.wifiBusy && len(m.wifiNetworks) > 0 {
				network := m.wifiNetworks[m.wifiCursor]
				m.wifiSelected = network.SSID
				if wifiAction(m.wifiAction) == wifiConnect && (m.wifiSelected == "" || m.wifiSelected == "--") {
					m.wifiSelected = network.BSSID
				}
				m.wifiBusy = true
				return m, wifiActionCmd(wifiAction(m.wifiAction), m.wifiSelected)
			}
		}
		return m, nil
	}
	switch message.String() {
	case "esc":
		m.screen = mainMenuScreen
	case "up", "k":
		m.wifiCursor = max(0, m.wifiCursor-1)
		if m.wifiCursor < m.wifiOffset {
			m.wifiOffset = m.wifiCursor
		}
	case "down", "j":
		m.wifiCursor = min(len(m.wifiNetworks)-1, m.wifiCursor+1)
		visibleRows := max(1, m.height-6)
		if m.wifiCursor >= m.wifiOffset+visibleRows {
			m.wifiOffset = m.wifiCursor - visibleRows + 1
		}
	case "enter":
		if len(m.wifiNetworks) > 0 && !m.wifiLoading {
			m.wifiDialog, m.wifiAction, m.wifiMessage = true, int(wifiConnect), ""
			m.wifiSelected = m.wifiNetworks[m.wifiCursor].SSID
		}
	}
	return m, nil
}

func (m Model) updateBluetooth(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.screen = mainMenuScreen
	case "up", "k":
		m.bluetoothCursor = max(0, m.bluetoothCursor-1)
	case "down", "j":
		m.bluetoothCursor = min(len(m.bluetoothDevices)-1, m.bluetoothCursor+1)
	case "enter":
		if !m.bluetoothLoading && len(m.bluetoothDevices) > 0 {
			return m, connectBluetoothCmd(m.bluetoothDevices[m.bluetoothCursor])
		}
	}
	return m, nil
}

func (m Model) View() string {
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	leftWidth, rightWidth := panelWidths(m.width)
	height := max(1, m.height)
	left := borderedPanel(leftWidth, height, m.menuView(activeStyle, headerStyle))
	rightTitle, rightContent := m.mainDetail(activeStyle, headerStyle)
	var right string
	switch m.screen {
	case wifiScreen:
		rightTitle, rightContent = "WI-FI", m.wifiView(activeStyle)
	case bluetoothScreen:
		rightTitle, rightContent = "BLUETOOTH", m.bluetoothView(activeStyle)
	case monitorScreen:
		right = borderedPanel(rightWidth, height, m.dashboardView(headerStyle))
	}
	if right == "" {
		right = borderedPanel(rightWidth, height, headerStyle.Render(rightTitle)+"\n\n"+rightContent)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func panelWidths(width int) (int, int) {
	width = max(2, width)
	if width < 30 {
		left := min(width-1, max(1, int(math.Round(float64(width)*.4))))
		return left, width - left
	}
	left := int(math.Round(float64(width) * .4))
	left = min(width-16, max(14, left))
	return left, width - left
}

func borderedPanel(width, height int, content string) string {
	width, height = max(1, width), max(1, height)
	return lipgloss.NewStyle().Width(width).Height(height).MaxWidth(width).MaxHeight(height).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Render(content)
}

func (m Model) menuView(activeStyle, headerStyle lipgloss.Style) string {
	view := headerStyle.Render("CONTROL & MENU") + "\n\n"
	for index, label := range m.menu {
		switch index {
		case 0:
			if m.volumeAvailable {
				label += fmt.Sprintf(" [%d%%]", m.volumePercent)
			} else {
				label += " [--]"
			}
			if m.audioStateOK && m.speakerMuted {
				label += " [MUTED]"
			}
		case 1:
			if m.brightnessOK {
				label += fmt.Sprintf(" [%d%%]", m.brightness)
			} else {
				label += " [--]"
			}
		case 4:
			if m.audioStateOK && m.micMuted {
				label += " [MUTED]"
			} else if m.audioStateOK {
				label += " [ON]"
			}
		case 5:
			if m.powerProfileOK {
				label += " [" + m.powerProfile + "]"
			} else {
				label += " [--]"
			}
		}
		if index == m.cursor {
			view += activeStyle.Render("> "+label) + "\n"
		} else {
			view += "  " + label + "\n"
		}
	}
	view += "\n\nUp/Down or j/k\nLeft/Right or +/-\nEnter: Select  q: Exit"
	if m.lastError != "" {
		view += "\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(m.lastError)
	}
	return view
}

func (m Model) mainDetail(activeStyle, headerStyle lipgloss.Style) (string, string) {
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	goodStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	switch m.cursor {
	case 0:
		volume := "Unavailable"
		if m.volumeAvailable {
			volume = fmt.Sprintf("%d%%", m.volumePercent)
		}
		muteState := goodStyle.Render("UNMUTED")
		if m.speakerMuted {
			muteState = mutedStyle.Render("MUTED")
		}
		return "AUDIO", headerStyle.Render("Speaker output") + "\n\nVolume  " + volume + "\nMute    " + muteState + "\n\n←/→ or -/+ adjusts by 5%\nEnter toggles mute"
	case 1:
		brightness := "Unavailable"
		if m.brightnessOK {
			brightness = fmt.Sprintf("%d%%", m.brightness)
		}
		return "BRIGHTNESS", headerStyle.Render("Display backlight") + "\n\nBrightness  " + brightness + "\n\n←/→ or -/+ adjusts by 5%"
	case 2:
		content := headerStyle.Render("Available networks") + "\n\nPress Enter to scan for Wi-Fi networks."
		if len(m.wifiNetworks) > 0 {
			content = m.wifiView(activeStyle)
		}
		return "WI-FI", content
	case 3:
		content := headerStyle.Render("Paired devices") + "\n\nPress Enter to load paired Bluetooth devices."
		if len(m.bluetoothDevices) > 0 {
			content = m.bluetoothView(activeStyle)
		}
		return "BLUETOOTH", content
	case 4:
		state := "Unavailable"
		if m.audioStateOK {
			state = goodStyle.Render("UNMUTED")
			if m.micMuted {
				state = mutedStyle.Render("MUTED")
			}
		}
		return "MICROPHONE", headerStyle.Render("Default input") + "\n\nMicrophone  " + state + "\n\nEnter toggles microphone mute"
	case 5:
		selected := powerProfiles[m.powerProfileCursor]
		current := "Unavailable"
		if m.powerProfileOK {
			current = m.powerProfile
		}
		var options strings.Builder
		for index, profile := range powerProfiles {
			line := "  " + profile
			if index == m.powerProfileCursor {
				line = activeStyle.Render("> " + profile)
			}
			options.WriteString(line + "\n")
		}
		return "POWER PROFILE", headerStyle.Render("Power mode") + "\n\nCurrent  " + current + "\nSelected " + selected + "\n\n" + options.String() + "\n←/→ chooses, Enter applies"
	default:
		return "SYSTEM MONITOR", headerStyle.Render("Live system dashboard") + "\n\nCPU per-core usage, detailed memory and disk usage, and network traffic are sampled only while this submenu is open.\n\nEnter opens the monitor"
	}
}

func (m Model) dashboardView(headerStyle lipgloss.Style) string {
	_, rightWidth := panelWidths(m.width)
	boxWidth := max(1, rightWidth-4)
	boxHeight := max(1, m.height-2)
	if boxWidth < 12 || boxHeight < 26 {
		return lipgloss.NewStyle().Width(boxWidth).Height(boxHeight).MaxWidth(boxWidth).MaxHeight(boxHeight).Align(lipgloss.Center, lipgloss.Center).Render("Increase terminal size to view the System Monitor")
	}

	clockHeight := max(9, boxHeight/5)
	networkHeight := max(10, boxHeight/4)
	if clockHeight+networkHeight > boxHeight-5 {
		clockHeight, networkHeight = 9, 5
	}
	middleHeight := boxHeight - clockHeight - networkHeight
	columnGap := 1
	leftColumnWidth := (boxWidth - columnGap) / 2
	rightColumnWidth := boxWidth - leftColumnWidth - columnGap
	clockStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	now := time.Now()
	clockContent := lipgloss.NewStyle().Width(max(1, boxWidth-4)).Align(lipgloss.Center).Render(now.Format("Monday, 02 January 2006") + "\n" + digitalClock(now, clockStyle))
	clock := monitorBox(boxWidth, clockHeight, "CLOCK", clockContent)
	middleLeft := monitorBox(leftColumnWidth, middleHeight, "CPU & GPU", monitorCPUGPUContent(m.snapshot, m.hasSnapshot))
	stackGap := 1
	stackHeight := middleHeight - stackGap
	memoryHeight := stackHeight / 2
	disksHeight := stackHeight - memoryHeight
	middleRight := lipgloss.JoinVertical(lipgloss.Left,
		monitorBox(rightColumnWidth, memoryHeight, monitorMemoryTitle(m.snapshot, m.hasSnapshot), monitorMemoryContent(m.snapshot, m.hasSnapshot)),
		"",
		monitorBox(rightColumnWidth, disksHeight, "DISKS /", monitorDiskContent(m.snapshot, m.hasSnapshot)),
	)
	middle := lipgloss.JoinHorizontal(lipgloss.Top, middleLeft, " ", middleRight)
	networkContentHeight := max(1, networkHeight-4)
	networkContent := monitorNetworkContent(m.snapshot, m.rxHistory, m.txHistory, m.hasSnapshot, max(1, boxWidth-4), networkContentHeight)
	network := monitorBox(boxWidth, networkHeight, "NETWORK", networkContent)
	return lipgloss.JoinVertical(lipgloss.Left, clock, middle, network)
}

func monitorBox(width, height int, title, content string) string {
	innerWidth := max(1, width-4)
	innerHeight := max(1, height-2)
	content = lipgloss.NewStyle().MaxWidth(innerWidth).MaxHeight(max(1, innerHeight-1)).Render(content)
	title = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render(title)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1).Width(width).Height(height).MaxWidth(width).MaxHeight(height).Render(title + "\n" + content)
}

func monitorCPUGPUContent(snapshot systemSnapshot, available bool) string {
	if !available {
		return "Collecting system metrics..."
	}
	var lines []string
	if len(snapshot.CPUPercent) == 0 {
		lines = append(lines, "CPU unavailable")
	} else {
		lines = append(lines, "CPU PER CORE")
		for core, percent := range snapshot.CPUPercent {
			lines = append(lines, coreTile(core, percent))
		}
	}
	lines = append(lines, fmt.Sprintf("Load Avg  1m %.2f  5m %.2f  15m %.2f", snapshot.Load1, snapshot.Load5, snapshot.Load15))
	lines = append(lines, gpuPanel(snapshot))
	lines = append(lines, "", lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render("POWER / THERMAL"))
	lines = append(lines, fmt.Sprintf("Battery %s", displayPercent(snapshot.BatteryPercent, snapshot.BatteryOK)))
	lines = append(lines, fmt.Sprintf("Temperature %s", displayTemperature(snapshot.TemperatureC, snapshot.TemperatureOK)))
	return strings.Join(lines, "\n")
}

func monitorMemoryContent(snapshot systemSnapshot, available bool) string {
	if !available || snapshot.RAMTotal == 0 {
		return "Memory unavailable"
	}
	used := int(math.Round(float64(snapshot.RAMUsed) / float64(snapshot.RAMTotal) * 100))
	availablePercent := int(math.Round(float64(snapshot.RAMAvailable) / float64(snapshot.RAMTotal) * 100))
	cached := int(math.Round(float64(snapshot.RAMCached) / float64(snapshot.RAMTotal) * 100))
	free := int(math.Round(float64(snapshot.RAMFree) / float64(snapshot.RAMTotal) * 100))
	return fmt.Sprintf("Used      %3d%% %s\nAvailable %3d%% %s\n\nCached    %3d%% %s\nFree      %3d%% %s",
		used, progressBar(used, 5), availablePercent, progressBar(availablePercent, 5),
		cached, progressBar(cached, 5), free, progressBar(free, 5))
}

func monitorMemoryTitle(snapshot systemSnapshot, available bool) string {
	if !available || snapshot.RAMTotal == 0 {
		return "MEMORY"
	}
	return fmt.Sprintf("MEMORY %.1f GiB", bytesToGiB(snapshot.RAMTotal))
}

func monitorDiskContent(snapshot systemSnapshot, available bool) string {
	if !available {
		return "Disk unavailable"
	}
	if len(snapshot.Disks) == 0 && snapshot.RootDiskTotal > 0 {
		snapshot.Disks = []diskSnapshot{{Mountpoint: "/", Total: snapshot.RootDiskTotal, Used: snapshot.RootDiskUsed, Free: snapshot.RootDiskFree}}
	}
	if len(snapshot.Disks) == 0 {
		return "No physical disks found"
	}
	lines := make([]string, 0, len(snapshot.Disks)*3)
	for _, mounted := range snapshot.Disks {
		if mounted.Total == 0 {
			continue
		}
		percent := min(100, max(0, int(math.Round(float64(mounted.Used)/float64(mounted.Total)*100))))
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render(mounted.Mountpoint), fmt.Sprintf("%d%% used  %s", percent, progressBar(percent, 8)), "")
	}
	if len(lines) == 0 {
		return "No disk usage available"
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func progressBar(percent, width int) string {
	percent = min(100, max(0, percent))
	width = max(1, width)
	filled := int(math.Round(float64(percent) * float64(width) / 100))
	color := "2"
	if percent > 80 {
		color = "1"
	} else if percent > 50 {
		color = "3"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("░", width-filled))
}

func monitorNetworkContent(snapshot systemSnapshot, rxHistory, txHistory []float64, available bool, width, height int) string {
	if !available {
		return "Collecting system metrics..."
	}
	statusHeight := max(2, height-2)
	statusTable := networkDeviceStatusTable(snapshot.NetworkDevices, width, statusHeight)
	traffic := lipgloss.JoinVertical(lipgloss.Left,
		fmt.Sprintf("Download RX %-12s  Upload TX %-12s", formatByteRate(snapshot.Traffic.RXBytesPerSecond), formatByteRate(snapshot.Traffic.TXBytesPerSecond)),
		fmt.Sprintf("RX %s  TX %s", trafficGraph(rxHistory), trafficGraph(txHistory)),
	)
	return lipgloss.JoinVertical(lipgloss.Left, statusTable, traffic)
}

func networkDeviceStatusTable(devices []networkDeviceStatus, width, maxRows int) string {
	columns := []string{"DEVICE", "TYPE", "STATE", "CONNECTION"}
	widths := []int{max(6, width/5), max(6, width/6), max(12, width/4), max(1, width-width/5-width/6-width/4-3)}
	lines := []string{networkDeviceStatusRow(columns, widths, lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true))}
	for index, device := range devices {
		if index >= max(0, maxRows-1) {
			break
		}
		rowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(networkDeviceStateColor(device.State)))
		lines = append(lines, networkDeviceStatusRow([]string{device.Device, device.Type, device.State, device.Connection}, widths, rowStyle))
	}
	if len(devices) == 0 && maxRows > 1 {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("No NetworkManager devices"))
	}
	return strings.Join(lines, "\n")
}

func networkDeviceStateColor(state string) string {
	state = strings.ToLower(state)
	if state == "connected" || strings.HasPrefix(state, "connected ") {
		return "2"
	}
	if strings.Contains(state, "disconnected") {
		return "1"
	}
	return "8"
}

func networkDeviceStatusRow(fields []string, widths []int, style lipgloss.Style) string {
	cells := make([]string, len(widths))
	for index, width := range widths {
		value := ""
		if index < len(fields) {
			value = ansi.TruncateWc(fields[index], width, "")
		}
		cells[index] = style.Width(width).Render(value)
	}
	return strings.Join(cells, " ")
}

func coreTile(core int, load float64) string {
	percent := min(100, max(0, int(math.Round(load))))
	return fmt.Sprintf("C%-2d %3d%% %s", core, percent, progressBar(percent, 8))
}

func smallBar(percent, width int, color string) string {
	filled := int(math.Round(float64(min(100, max(0, percent))) * float64(width) / 100))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.Repeat("█", filled)) + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("░", width-filled))
}

func memoryPanel(snapshot systemSnapshot) string {
	view := "MEMORY\n"
	if snapshot.RAMTotal == 0 {
		return view + "Memory unavailable\n"
	}
	for _, item := range []struct {
		name  string
		value uint64
	}{{"Total", snapshot.RAMTotal}, {"Used", snapshot.RAMUsed}, {"Available", snapshot.RAMAvailable}, {"Cached", snapshot.RAMCached}, {"Free", snapshot.RAMFree}} {
		percent := int(math.Round(float64(item.value) / float64(snapshot.RAMTotal) * 100))
		view += fmt.Sprintf("%-10s %6.2f GiB  %3d%%\n", item.name, bytesToGiB(item.value), percent)
	}
	return view + "\n"
}

func diskPanel(snapshot systemSnapshot) string {
	view := "DISKS /\n"
	if snapshot.RootDiskTotal == 0 {
		return view + "Disk unavailable\n"
	}
	percent := int(math.Round(float64(snapshot.RootDiskUsed) / float64(snapshot.RootDiskTotal) * 100))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(strings.Repeat("█", percent/5))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(strings.Repeat("█", (100-percent)/5))
	return view + fmt.Sprintf("Used %.1f GiB / %.1f GiB  Free %.1f GiB\n[%s%s]\n\n", bytesToGiB(snapshot.RootDiskUsed), bytesToGiB(snapshot.RootDiskTotal), bytesToGiB(snapshot.RootDiskFree), red, green)
}

func gpuPanel(snapshot systemSnapshot) string {
	view := "INTEL GPU  "
	if !snapshot.GPUUtilOK {
		return view + "N/A\n"
	}
	bar := progressBar(snapshot.GPUUtilPercent, 8)
	percent := fmt.Sprintf("%d%%", snapshot.GPUUtilPercent)
	if snapshot.GPUUtilEstimate {
		percent = "~" + percent
	}
	view += fmt.Sprintf("[%s] %s", bar, percent)
	if snapshot.GPUFrequencyOK {
		view += fmt.Sprintf("  %d MHz", snapshot.GPUFrequencyMHz)
	}
	if snapshot.GPUPowerOK {
		view += fmt.Sprintf("  %.1f W", snapshot.GPUPowerWatts)
	}
	return view + "\n"
}

func networkPanel(snapshot systemSnapshot, rxHistory, txHistory []float64) string {
	view := "NETWORK\n"
	if !snapshot.NetworkOK {
		return view + "No active interface\n"
	}
	return view + fmt.Sprintf("%s  IP %s\nDownload %-12s Upload %-12s\nTotal RX %-10s TX %-10s\nRX %s\nTX %s\n",
		snapshot.Interface, snapshot.LocalIP,
		formatByteRate(snapshot.Traffic.RXBytesPerSecond), formatByteRate(snapshot.Traffic.TXBytesPerSecond),
		formatDataSize(snapshot.Traffic.TotalRX), formatDataSize(snapshot.Traffic.TotalTX),
		trafficGraph(rxHistory), trafficGraph(txHistory))
}

func trafficGraph(history []float64) string {
	const maxSamples = 32
	if len(history) > maxSamples {
		history = history[len(history)-maxSamples:]
	}
	var peak float64
	for _, value := range history {
		peak = math.Max(peak, value)
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	var output strings.Builder
	for _, value := range history {
		if peak == 0 || value == 0 {
			output.WriteRune('·')
		} else {
			index := min(len(levels)-1, max(0, int(math.Ceil(value/peak*float64(len(levels))))-1))
			output.WriteRune(levels[index])
		}
	}
	return output.String()
}

func formatByteRate(value float64) string {
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s"}
	index := 0
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func formatDataSize(value uint64) string {
	return formatByteRate(float64(value))
}

func displayPercent(value int, available bool) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%d%%", value)
}

func displayTemperature(value float64, available bool) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%.1f C", value)
}

func digitalClock(now time.Time, style lipgloss.Style) string {
	glyphs := map[rune][]string{
		'0': {"███", "█ █", "█ █", "█ █", "███"}, '1': {" █ ", "██ ", " █ ", " █ ", "███"},
		'2': {"███", "  █", "███", "█  ", "███"}, '3': {"███", "  █", " ██", "  █", "███"},
		'4': {"█ █", "█ █", "███", "  █", "  █"}, '5': {"███", "█  ", "███", "  █", "███"},
		'6': {"███", "█  ", "███", "█ █", "███"}, '7': {"███", "  █", "  █", "  █", "  █"},
		'8': {"███", "█ █", "███", "█ █", "███"}, '9': {"███", "█ █", "███", "  █", "███"},
		':': {" ", "█", " ", "█", " "},
	}
	var rows [5]strings.Builder
	for _, digit := range now.Format("15:04:05") {
		glyph := glyphs[digit]
		for row := range rows {
			rows[row].WriteString(glyph[row])
			rows[row].WriteByte(' ')
		}
	}
	result := make([]string, len(rows))
	for row := range rows {
		result[row] = style.Render(strings.TrimRight(rows[row].String(), " "))
	}
	return strings.Join(result, "\n")
}

func (m Model) wifiView(activeStyle lipgloss.Style) string {
	_, rightWidth := panelWidths(m.width)
	viewWidth := max(1, rightWidth-4)
	viewHeight := max(1, m.height-4)
	if m.wifiLoading {
		return "Available networks\n\nScanning..."
	}
	if m.wifiError != "" {
		return "Available networks\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(m.wifiError)
	}
	if len(m.wifiNetworks) == 0 {
		return "Available networks\n\nNo networks found."
	}
	view := wifiTableView(m.wifiNetworks, m.wifiCursor, m.wifiOffset, viewWidth, viewHeight)
	if m.wifiDialog {
		return overlayWiFiDialog(view, wifiDialogView(m, activeStyle), viewWidth, viewHeight)
	}
	if m.wifiMessage != "" {
		view += "\n" + m.wifiMessage + "\n"
	}
	return view
}

func wifiTableView(networks []WiFiNetwork, cursor, offset, width, height int) string {
	titles := []string{"IN-USE", "BSSID", "SSID", "MODE", "BAND", "CHAN", "RATE", "SIGNAL", "BARS", "SECURITY"}
	rows := make([][]string, len(networks))
	for index, network := range networks {
		inUse := network.InUseMark
		if inUse == "" && network.InUse {
			inUse = "*"
		}
		rows[index] = []string{inUse, network.BSSID, network.SSID, network.Mode, network.Band, network.Channel, network.Rate, strconv.Itoa(network.Signal), network.Bars, network.Security}
	}
	widths := wifiColumnWidths(titles, rows, max(1, width))
	visibleRows := max(1, height-2)
	offset = min(max(0, offset), max(0, len(networks)-1))
	if cursor >= offset+visibleRows {
		offset = cursor - visibleRows + 1
	}
	if cursor < offset {
		offset = cursor
	}
	lines := []string{"Available networks", wifiTableRow(titles, widths, -1, cursor)}
	end := min(len(networks), offset+visibleRows)
	for index := offset; index < end; index++ {
		lines = append(lines, wifiTableRow(rows[index], widths, index, cursor))
	}
	return strings.Join(lines, "\n")
}

func wifiColumnWidths(titles []string, rows [][]string, width int) []int {
	widths := make([]int, len(titles))
	preferred := make([]int, len(titles))
	caps := []int{6, 17, 20, 5, 6, 4, 12, 6, 4, 10}
	for index, title := range titles {
		preferred[index] = lipgloss.Width(title)
		for _, row := range rows {
			if index < len(row) {
				preferred[index] = max(preferred[index], lipgloss.Width(row[index]))
			}
		}
		preferred[index] = min(preferred[index], caps[index])
	}
	remaining := max(0, width-len(titles)+1)
	for index := range widths {
		widths[index] = 1
		remaining--
	}
	for remaining > 0 {
		changed := false
		for index := range widths {
			if widths[index] < preferred[index] && remaining > 0 {
				widths[index]++
				remaining--
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return widths
}

func wifiTableRow(fields []string, widths []int, rowIndex, cursor int) string {
	cells := make([]string, len(widths))
	for index, width := range widths {
		value := ""
		if index < len(fields) {
			value = ansi.TruncateWc(fields[index], width, "")
		}
		style := lipgloss.NewStyle().Width(width)
		if rowIndex == -1 {
			style = style.Foreground(lipgloss.Color("6")).Bold(true)
		} else if rowIndex == cursor {
			style = style.Background(lipgloss.Color("8")).Foreground(lipgloss.Color("0"))
		}
		if rowIndex != -1 && index == 0 && value == "*" {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Width(width)
			if rowIndex == cursor {
				style = style.Background(lipgloss.Color("8"))
			}
		}
		if rowIndex != -1 && index == 8 {
			signal := 0
			if len(fields) > 7 {
				signal, _ = strconv.Atoi(fields[7])
			}
			color := "6"
			if signal > 80 {
				color = "2"
			} else if signal > 60 {
				color = "3"
			} else if signal > 40 {
				color = "5"
			}
			style = style.Foreground(lipgloss.Color(color))
			if rowIndex == cursor {
				style = style.Background(lipgloss.Color("8"))
			}
		}
		cells[index] = style.Render(value)
	}
	separator := " "
	if rowIndex == cursor {
		separator = lipgloss.NewStyle().Background(lipgloss.Color("8")).Render(separator)
	}
	return strings.Join(cells, separator)
}

func overlayWiFiDialog(background, dialog string, width, height int) string {
	baseLines := strings.Split(background, "\n")
	dialogLines := strings.Split(dialog, "\n")
	dialogWidth := 0
	for _, line := range dialogLines {
		dialogWidth = max(dialogWidth, ansi.StringWidth(line))
	}
	left := max(0, (width-dialogWidth)/2)
	top := max(1, (height-len(dialogLines))/2)
	for index, line := range dialogLines {
		row := top + index
		if row >= height {
			break
		}
		for len(baseLines) <= row {
			baseLines = append(baseLines, "")
		}
		base := ansi.TruncateWc(baseLines[row], width, "")
		baseWidth := ansi.StringWidth(base)
		if baseWidth < width {
			base += strings.Repeat(" ", width-baseWidth)
		}
		dialogLine := ansi.TruncateWc(line, max(0, width-left), "")
		dialogLineWidth := ansi.StringWidth(dialogLine)
		suffixStart := min(width, left+dialogWidth)
		suffix := ansi.CutWc(base, suffixStart, width)
		baseLines[row] = ansi.CutWc(base, 0, left) + dialogLine + strings.Repeat(" ", max(0, dialogWidth-dialogLineWidth)) + suffix
	}
	if len(baseLines) > height {
		baseLines = baseLines[:height]
	}
	return strings.Join(baseLines, "\n")
}

func wifiDialogView(m Model, activeStyle lipgloss.Style) string {
	options := []string{"Connect", "Disconnect", "Show Password"}
	content := "Network: " + m.wifiSelected + "\n\n"
	if m.wifiBusy {
		content += "Working...\n"
	} else {
		for index, option := range options {
			if index == m.wifiAction {
				content += activeStyle.Render("> "+option) + "\n"
			} else {
				content += "  " + option + "\n"
			}
		}
	}
	if m.wifiMessage != "" {
		content += "\n" + m.wifiMessage + "\n"
	}
	content += "\nEsc: Close"
	_, rightWidth := panelWidths(m.width)
	innerWidth := max(1, min(40, rightWidth-8))
	return lipgloss.NewStyle().Width(innerWidth).MaxWidth(innerWidth).Padding(1, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Background(lipgloss.Color("0")).Render(content)
}

func (m Model) bluetoothView(activeStyle lipgloss.Style) string {
	view := "Paired devices\n\n"
	if m.bluetoothLoading {
		return view + "Loading..."
	}
	if len(m.bluetoothDevices) == 0 {
		view += "No devices found.\n"
	}
	for index, device := range m.bluetoothDevices {
		state := "[DISCONNECTED]"
		if device.Connected {
			state = "[CONNECTED]"
		}
		line := fmt.Sprintf("%s  %s  %s", device.Name, device.MACAddress, state)
		if index == m.bluetoothCursor {
			view += activeStyle.Render("> "+line) + "\n"
		} else {
			view += "  " + line + "\n"
		}
	}
	if m.bluetoothMessage != "" {
		view += "\n" + m.bluetoothMessage + "\n"
	}
	return view + "\nEsc: Main Menu"
}

func main() {
	program := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error running sandctl: %v\n", err)
		os.Exit(1)
	}
}
