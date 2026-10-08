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
)

type wifiAction int

const (
	wifiConnect wifiAction = iota
	wifiDisconnect
	wifiShowPassword
)

type WiFiNetwork struct {
	SSID     string
	Security string
	Signal   int
	InUse    bool
}

type BluetoothDevice struct {
	MACAddress string
	Name       string
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

	volumePercent   int
	volumeAvailable bool
	brightness      int
	brightnessOK    bool
	lastError       string

	wifiNetworks []WiFiNetwork
	wifiCursor   int
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

type brightnessMsg struct {
	percent int
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
	device BluetoothDevice
	err    error
}

type tickMsg struct{}

func initialModel() Model {
	return Model{
		width:  80,
		height: 24,
		menu:   []string{"Audio", "Brightness", "Wi-Fi", "Bluetooth"},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(readVolumeCmd(), readBrightnessCmd(), readSystemCmd())
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

func setVolumeCmd(change string) tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%"+change).CombinedOutput()
		if err != nil {
			return volumeMsg{err: fmt.Errorf("wpctl set-volume: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return readVolumeCmd()()
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
	root, diskErr := disk.Usage("/")
	if diskErr == nil && root != nil {
		snapshot.RootDiskTotal = root.Total
		snapshot.RootDiskUsed = root.Used
		snapshot.RootDiskFree = root.Free
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
	snapshot.BatteryPercent, snapshot.BatteryOK = readBattery()
	snapshot.TemperatureC, snapshot.TemperatureOK = readTemperature()
	snapshot.GPUUtilPercent, snapshot.GPUUtilOK, snapshot.GPUUtilEstimate, snapshot.GPUFrequencyMHz, snapshot.GPUFrequencyOK, snapshot.GPUPowerWatts, snapshot.GPUPowerOK = readGPU()

	if cpuErr != nil && memoryErr != nil && diskErr != nil {
		return snapshot, fmt.Errorf("system metrics unavailable: CPU: %v; memory: %v; disk: %v", cpuErr, memoryErr, diskErr)
	}
	return snapshot, nil
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
		if len(fields) != 4 {
			return nil, fmt.Errorf("invalid nmcli row %q", line)
		}
		if fields[1] == "" {
			continue
		}
		signal, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("invalid Wi-Fi signal in %q", line)
		}
		networks = append(networks, WiFiNetwork{InUse: fields[0] == "*", SSID: fields[1], Security: fields[2], Signal: signal})
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
		output, err := exec.Command("nmcli", "-t", "-f", "IN-USE,SSID,SECURITY,SIGNAL", "dev", "wifi").CombinedOutput()
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
		output, err := exec.Command("bluetoothctl", "devices").CombinedOutput()
		if err != nil {
			return bluetoothScanMsg{err: fmt.Errorf("bluetoothctl devices: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		var devices []BluetoothDevice
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "Device" {
				devices = append(devices, BluetoothDevice{MACAddress: fields[1], Name: strings.Join(fields[2:], " ")})
			}
		}
		return bluetoothScanMsg{devices: devices}
	}
}

func connectBluetoothCmd(device BluetoothDevice) tea.Cmd {
	return func() tea.Msg {
		output, err := exec.Command("bluetoothctl", "connect", device.MACAddress).CombinedOutput()
		if err != nil {
			return bluetoothConnectMsg{device: device, err: fmt.Errorf("bluetoothctl connect: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return bluetoothConnectMsg{device: device}
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
		default:
			return m.updateMainMenu(message)
		}
	case volumeMsg:
		if message.err == nil {
			m.volumePercent, m.volumeAvailable = message.percent, true
		} else {
			m.lastError = message.err.Error()
		}
	case brightnessMsg:
		if message.err == nil {
			m.brightness, m.brightnessOK = message.percent, true
		} else {
			m.lastError = message.err.Error()
		}
	case systemMsg:
		if message.err == nil {
			m.applySnapshot(message.snapshot)
		} else {
			m.lastError = message.err.Error()
		}
		return m, systemTickCmd()
	case tickMsg:
		return m, readSystemCmd()
	case wifiScanMsg:
		m.wifiLoading = false
		if message.err != nil {
			m.wifiError = message.err.Error()
		} else {
			m.wifiError = ""
			m.wifiNetworks = message.networks
			m.wifiCursor = min(m.wifiCursor, max(0, len(m.wifiNetworks)-1))
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
			m.bluetoothMessage = "Connected to " + message.device.Name
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
			m.volumePercent = min(150, m.volumePercent+5)
			m.volumeAvailable = true
			return m, setVolumeCmd("+")
		}
		if m.cursor == 1 {
			m.brightness = min(100, m.brightness+5)
			m.brightnessOK = true
			return m, setBrightnessCmd("+")
		}
	case "left", "-":
		if m.cursor == 0 {
			m.volumePercent = max(0, m.volumePercent-5)
			m.volumeAvailable = true
			return m, setVolumeCmd("-")
		}
		if m.cursor == 1 {
			m.brightness = max(0, m.brightness-5)
			m.brightnessOK = true
			return m, setBrightnessCmd("-")
		}
	case "enter":
		if m.cursor == 2 {
			m.screen, m.wifiLoading, m.wifiError = wifiScreen, true, ""
			return m, scanWiFiCmd()
		}
		if m.cursor == 3 {
			m.screen, m.bluetoothLoading = bluetoothScreen, true
			return m, scanBluetoothCmd()
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
				m.wifiSelected = m.wifiNetworks[m.wifiCursor].SSID
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
	case "down", "j":
		m.wifiCursor = min(len(m.wifiNetworks)-1, m.wifiCursor+1)
	case "enter":
		if len(m.wifiNetworks) > 0 && !m.wifiLoading {
			m.wifiDialog, m.wifiAction, m.wifiMessage = true, int(wifiConnect), ""
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
	height := max(8, m.height)
	left := borderedPanel(leftWidth, height, m.menuView(activeStyle, headerStyle))
	rightTitle, rightContent := "SYSTEM DASHBOARD", m.dashboardView(headerStyle)
	switch m.screen {
	case wifiScreen:
		rightTitle, rightContent = "WI-FI", m.wifiView(activeStyle)
	case bluetoothScreen:
		rightTitle, rightContent = "BLUETOOTH", m.bluetoothView(activeStyle)
	}
	right := borderedPanel(rightWidth, height, headerStyle.Render(rightTitle)+"\n\n"+rightContent)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func panelWidths(width int) (int, int) {
	width = max(30, width)
	left := int(math.Round(float64(width) * .4))
	left = min(width-16, max(14, left))
	return left, width - left
}

func borderedPanel(width, height int, content string) string {
	return lipgloss.NewStyle().Width(max(1, width-4)).Height(max(1, height-2)).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Render(content)
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
		case 1:
			if m.brightnessOK {
				label += fmt.Sprintf(" [%d%%]", m.brightness)
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
	view += "\n\nUp/Down or j/k\nLeft/Right or +/-\nEnter: Open  q: Exit"
	if m.lastError != "" {
		view += "\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(m.lastError)
	}
	return view
}

func (m Model) dashboardView(headerStyle lipgloss.Style) string {
	view := digitalClock(time.Now(), headerStyle) + "\n\n"
	if !m.hasSnapshot {
		return view + "Collecting system metrics..."
	}
	snapshot := m.snapshot
	view += "CPU PER CORE\n"
	for start := 0; start < len(snapshot.CPUPercent); start += 2 {
		left := coreTile(start, snapshot.CPUPercent[start])
		right := ""
		if start+1 < len(snapshot.CPUPercent) {
			right = coreTile(start+1, snapshot.CPUPercent[start+1])
		}
		view += lipgloss.JoinHorizontal(lipgloss.Top, left, "   ", right) + "\n"
	}
	view += fmt.Sprintf("Load Avg  1m %.2f   5m %.2f   15m %.2f\n\n", snapshot.Load1, snapshot.Load5, snapshot.Load15)
	view += memoryPanel(snapshot)
	view += diskPanel(snapshot)
	view += gpuPanel(snapshot)
	view += fmt.Sprintf("Battery  %s    Temperature  %s\n", displayPercent(snapshot.BatteryPercent, snapshot.BatteryOK), displayTemperature(snapshot.TemperatureC, snapshot.TemperatureOK))
	view += networkPanel(snapshot, m.rxHistory, m.txHistory)
	return view
}

func coreTile(core int, load float64) string {
	percent := min(100, max(0, int(math.Round(load))))
	return fmt.Sprintf("C%-2d %3d%% %s", core, percent, smallBar(percent, 8, "2"))
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
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(strings.Repeat("█", snapshot.GPUUtilPercent/5))
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
	view := "Available networks\n\n"
	if m.wifiLoading {
		return view + "Scanning..."
	}
	if m.wifiError != "" {
		return view + lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(m.wifiError)
	}
	if len(m.wifiNetworks) == 0 {
		view += "No networks found.\n"
	}
	connected := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	for index, network := range m.wifiNetworks {
		name := network.SSID
		if name == "" {
			name = "<hidden>"
		}
		line := fmt.Sprintf("%s  %d%%", name, network.Signal)
		if network.InUse {
			line = name + " " + connected.Render("[CONNECTED]") + fmt.Sprintf("  %d%%", network.Signal)
		}
		if index == m.wifiCursor {
			view += activeStyle.Render("> "+line) + "\n"
		} else {
			view += "  " + line + "\n"
		}
	}
	if m.wifiDialog {
		return wifiDialogView(m, activeStyle)
	}
	if m.wifiMessage != "" {
		view += "\n" + m.wifiMessage + "\n"
	}
	return view + "\nEsc: Main Menu"
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
	box := lipgloss.NewStyle().Width(min(48, max(12, rightWidth-10))).Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Render(content)
	return lipgloss.Place(max(1, rightWidth-4), max(1, m.height-6), lipgloss.Center, lipgloss.Center, box)
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
		line := fmt.Sprintf("%s  %s", device.Name, device.MACAddress)
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
