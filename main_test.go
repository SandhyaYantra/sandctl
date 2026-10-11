package main

import (
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shirou/gopsutil/v3/disk"
)

func TestParseVolumeOutput(t *testing.T) {
	for _, test := range []struct {
		output string
		want   int
	}{
		{output: "Volume: 0.65", want: 65},
		{output: "Volume:0.42 [MUTED]", want: 42},
	} {
		got, err := parseVolume(test.output)
		if err != nil || got != test.want {
			t.Errorf("parseVolume(%q) = (%d, %v), want (%d, nil)", test.output, got, err, test.want)
		}
	}
}

func TestParseBrightnessValues(t *testing.T) {
	got, err := parseBrightness("500\n", "1000\n")
	if err != nil || got != 50 {
		t.Fatalf("parseBrightness() = (%d, %v), want (50, nil)", got, err)
	}
	if _, err := parseBrightness("10", "0"); err == nil {
		t.Fatal("parseBrightness accepted zero maximum")
	}
}

func TestParseWiFiNetworksAndConnectedState(t *testing.T) {
	output := "*:Home Network:WPA2:55\n:Cafe\\:Floor:WPA3:67\n:Guest::42\n"
	networks, err := parseWiFiNetworks(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 3 || !networks[0].InUse || networks[0].SSID != "Home Network" || networks[0].Signal != 55 || networks[1].SSID != "Cafe:Floor" {
		t.Fatalf("unexpected parsed networks: %+v", networks)
	}
}

func TestParseWiFiTerseTenFieldsAndHiddenSSID(t *testing.T) {
	output := "*:AA\\:BB\\:CC\\:DD\\:EE\\:FF:Home\\: Network:Infra:5 GHz:36:540 Mbit/s:87:▂▄▆█:WPA2\n:11\\:22\\:33\\:44\\:55\\:66:--:Infra:2.4 GHz:1:130 Mbit/s:38:▂▄:WPA3"
	networks, err := parseWiFiNetworks(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 2 {
		t.Fatalf("parsed %d networks, want 2: %+v", len(networks), networks)
	}
	connected := networks[0]
	if !connected.InUse || connected.InUseMark != "*" || connected.BSSID != "AA:BB:CC:DD:EE:FF" || connected.SSID != "Home: Network" || connected.Mode != "Infra" || connected.Band != "5 GHz" || connected.Channel != "36" || connected.Rate != "540 Mbit/s" || connected.Signal != 87 || connected.Bars != "▂▄▆█" || connected.Security != "WPA2" {
		t.Fatalf("ten-field Wi-Fi row not retained: %+v", connected)
	}
	if networks[1].SSID != "--" || networks[1].BSSID != "11:22:33:44:55:66" {
		t.Fatalf("hidden SSID row was changed or dropped: %+v", networks[1])
	}
}

func TestParseNetworkDeviceStatuses(t *testing.T) {
	devices, err := parseNetworkDeviceStatuses("wlan0:wifi:connected:Home Wi-Fi\nenp2s0:ethernet:disconnected:\nlo:loopback:unavailable:")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 3 || devices[0].Device != "wlan0" || devices[0].Connection != "Home Wi-Fi" || devices[1].State != "disconnected" || devices[2].State != "unavailable" {
		t.Fatalf("unexpected NetworkManager device statuses: %+v", devices)
	}
}

func TestNetworkDeviceStatusColors(t *testing.T) {
	for _, test := range []struct {
		state string
		color string
	}{{"connected", "2"}, {"connected (externally)", "2"}, {"disconnected", "1"}, {"unavailable", "8"}, {"unmanaged", "8"}} {
		if got := networkDeviceStateColor(test.state); got != test.color {
			t.Errorf("networkDeviceStateColor(%q) = %q, want %q", test.state, got, test.color)
		}
	}
}

func TestWiFiTableContainsAllColumnsAndDialogOverlay(t *testing.T) {
	networks := []WiFiNetwork{{InUse: true, InUseMark: "*", BSSID: "AA:BB:CC:DD:EE:FF", SSID: "Home", Mode: "Infra", Band: "5 GHz", Channel: "36", Rate: "540 Mbit/s", Signal: 87, Bars: "▂▄▆█", Security: "WPA2"}}
	table := wifiTableView(networks, 0, 0, 120, 20)
	for _, field := range []string{"IN-USE", "BSSID", "SSID", "MODE", "BAND", "CHAN", "RATE", "SIGNAL", "BARS", "SECURITY", "AA:BB:CC:DD:EE:FF", "Home", "540 Mbit/s"} {
		if !strings.Contains(table, field) {
			t.Errorf("Wi-Fi table missing %q", field)
		}
	}
	lines := strings.Split(table, "\n")
	if lipgloss.Width(lines[1]) != lipgloss.Width(lines[2]) {
		t.Errorf("Wi-Fi header width %d differs from data row width %d", lipgloss.Width(lines[1]), lipgloss.Width(lines[2]))
	}
	overlay := overlayWiFiDialog(table, "╭──────╮\n│MENU  │\n╰──────╯", 120, 20)
	if !strings.Contains(overlay, "BSSID") || !strings.Contains(overlay, "MENU") {
		t.Fatalf("dialog overlay did not retain table and dialog content: %q", overlay)
	}
}

func TestWiFiEnterOpensDialogAndActionIsAsync(t *testing.T) {
	model := initialModel()
	model.cursor = 2
	model.wifiNetworks = []WiFiNetwork{{SSID: "Home", Signal: 80}}
	updated, scan := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if scan == nil || model.screen != wifiScreen || !model.wifiLoading {
		t.Fatal("Enter on Wi-Fi did not open scanning submenu")
	}
	updated, _ = model.Update(wifiScanMsg{networks: model.wifiNetworks})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil || !model.wifiDialog {
		t.Fatal("SSID Enter should open a dialog without immediately running a command")
	}
	for _, option := range []string{"Connect", "Disconnect", "Show Password"} {
		if !strings.Contains(model.wifiView(lipgloss.NewStyle()), option) {
			t.Errorf("Wi-Fi dialog missing %q", option)
		}
	}
}

func TestWiFiActionSelectionDispatchesCommand(t *testing.T) {
	model := initialModel()
	model.screen = wifiScreen
	model.wifiNetworks = []WiFiNetwork{{SSID: "Home"}}
	model.wifiDialog = true
	model.wifiAction = int(wifiShowPassword)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil || !model.wifiBusy || model.wifiSelected != "Home" {
		t.Fatal("Show Password selection did not dispatch asynchronously")
	}
	updated, _ = model.Update(wifiActionMsg{action: wifiShowPassword, output: "Password: stored"})
	model = updated.(Model)
	if !strings.Contains(model.wifiMessage, "stored") || !model.wifiDialog {
		t.Fatal("password result was not retained in the dialog")
	}
}

func TestPanelWidthsAndFullscreenProgramSetup(t *testing.T) {
	left, right := panelWidths(100)
	if left != 40 || right != 60 {
		t.Fatalf("panelWidths(100) = (%d, %d), want (40, 60)", left, right)
	}
	model := initialModel()
	if model.Init() == nil {
		t.Fatal("Init should start background monitoring commands")
	}
}

func TestApplySnapshotTracksNetworkRatesTotalsAndHistory(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	model := initialModel()
	model.snapshot.Interface = "wlan0"
	model.previousIO = networkCounters{RX: 1000, TX: 2000}
	model.previousIOAt = start
	model.hasPreviousIO = true
	model.applySnapshot(systemSnapshot{
		Interface: "wlan0", NetworkOK: true, SampledAt: start.Add(2 * time.Second),
		Traffic: trafficRates{TotalRX: 3500, TotalTX: 2600},
	})
	if model.snapshot.Traffic.RXBytesPerSecond != 1250 || model.snapshot.Traffic.TXBytesPerSecond != 300 {
		t.Fatalf("rates = %.1f/%.1f, want 1250/300", model.snapshot.Traffic.RXBytesPerSecond, model.snapshot.Traffic.TXBytesPerSecond)
	}
	if model.snapshot.Traffic.TopRX != 1250 || model.snapshot.Traffic.TopTX != 300 || model.snapshot.Traffic.TotalRX != 3500 {
		t.Fatalf("traffic stats not retained: %+v", model.snapshot.Traffic)
	}
	if len(model.rxHistory) != 1 || len(model.txHistory) != 1 {
		t.Fatalf("traffic history lengths = %d/%d, want 1/1", len(model.rxHistory), len(model.txHistory))
	}
}

func TestCollectSystemSnapshotUsesGopsutilMetrics(t *testing.T) {
	snapshot, err := collectSystemSnapshot()
	if err != nil {
		t.Fatalf("collectSystemSnapshot() error = %v", err)
	}
	if len(snapshot.CPUPercent) == 0 {
		t.Fatal("gopsutil returned no per-core CPU samples")
	}
	if snapshot.RAMTotal == 0 || snapshot.RootDiskTotal == 0 {
		t.Fatalf("gopsutil returned incomplete memory/disk snapshot: %+v", snapshot)
	}
}

func TestDashboardRendersDetailedSections(t *testing.T) {
	model := initialModel()
	model.width = 100
	model.height = 42
	model.hasSnapshot = true
	model.snapshot = systemSnapshot{
		CPUPercent: []float64{10, 20, 30, 40},
		RAMTotal:   8 << 30, RAMUsed: 4 << 30, RAMAvailable: 4 << 30, RAMCached: 1 << 30, RAMFree: 512 << 20,
		RootDiskTotal: 10 << 30, RootDiskUsed: 8 << 30, RootDiskFree: 2 << 30,
		Disks: []diskSnapshot{{Mountpoint: "/", Total: 10 << 30, Used: 8 << 30, Free: 2 << 30}, {Mountpoint: "/run/media/user/usb", Total: 4 << 30, Used: 1 << 30, Free: 3 << 30}},
		Load1: 1.25, Load5: 0.75, Load15: 0.5,
		Interface: "wlan0", LocalIP: "192.168.1.5", NetworkOK: true,
		BatteryPercent: 80, BatteryOK: true, TemperatureC: 50, TemperatureOK: true,
		NetworkDevices: []networkDeviceStatus{{Device: "wlan0", Type: "wifi", State: "connected", Connection: "Home Wi-Fi"}, {Device: "eth0", Type: "ethernet", State: "disconnected"}},
		Traffic:        trafficRates{RXBytesPerSecond: 1024, TXBytesPerSecond: 512, TotalRX: 1 << 20, TotalTX: 512 << 10},
	}
	view := model.dashboardView(lipgloss.NewStyle())
	_, rightWidth := panelWidths(model.width)
	if got, want := lipgloss.Width(view), rightWidth-4; got != want {
		t.Errorf("dashboard width = %d, want panel interior width %d", got, want)
	}
	if got, want := lipgloss.Height(view), model.height-2; got != want {
		t.Errorf("dashboard height = %d, want panel interior height %d", got, want)
	}
	for _, expected := range []string{"CPU PER CORE", "C0", "10%", "Load Avg", "Battery 80%", "Temperature 50.0 C", "MEMORY", "Available", "Cached", "DISKS /", "8.0 GiB", "/run/media/user/usb", "NETWORK", "DEVICE", "wlan0", "Home Wi-Fi", "disconnected", "Download", "Upload", "RX", "TX"} {
		if !strings.Contains(view, expected) {
			t.Errorf("dashboard missing %q", expected)
		}
	}
	if strings.Index(view, "DEVICE") > strings.Index(view, "Download") {
		t.Error("Network device status table should appear above traffic metrics")
	}
	if strings.Index(view, "Battery 80%") > strings.Index(view, "NETWORK") {
		t.Error("battery/temperature summary should remain in CPU & GPU above Network")
	}
}

func TestMonitorSectionsKeepVisualSpacing(t *testing.T) {
	snapshot := systemSnapshot{
		CPUPercent: []float64{20}, RAMTotal: 8 << 30, RAMUsed: 4 << 30, RAMAvailable: 4 << 30,
		RAMCached: 1 << 30, RAMFree: 512 << 20, RootDiskTotal: 10 << 30, RootDiskUsed: 8 << 30, RootDiskFree: 2 << 30,
		Disks:          []diskSnapshot{{Mountpoint: "/", Total: 10 << 30, Used: 8 << 30, Free: 2 << 30}, {Mountpoint: "/media/usb", Total: 4 << 30, Used: 1 << 30, Free: 3 << 30}},
		BatteryPercent: 80, BatteryOK: true, TemperatureC: 50, TemperatureOK: true,
	}
	if !strings.Contains(monitorMemoryContent(snapshot, true), "\n\n") {
		t.Error("Memory metrics should have a blank line between metric groups")
	}
	if !strings.Contains(monitorDiskContent(snapshot, true), "\n\n") {
		t.Error("Disk entries should have a blank line between mount points")
	}
	cpuView := monitorCPUGPUContent(snapshot, true)
	if !strings.Contains(cpuView, "\n\n") || !strings.Contains(cpuView, "POWER / THERMAL") {
		t.Error("CPU & GPU should separate the battery/temperature section")
	}
}

func TestWiFiSubmenuHasNoDeviceStatusTable(t *testing.T) {
	model := initialModel()
	model.width, model.height, model.screen = 120, 30, wifiScreen
	model.wifiNetworks = []WiFiNetwork{{BSSID: "AA:BB:CC:DD:EE:FF", SSID: "Home", Mode: "Infra", Band: "5 GHz", Channel: "36", Rate: "540 Mbit/s", Signal: 90, Bars: "▂▄▆█", Security: "WPA2"}}
	view := model.wifiView(lipgloss.NewStyle())
	if !strings.Contains(view, "BSSID") || !strings.Contains(view, "Home") {
		t.Fatalf("Wi-Fi SSID table not rendered: %q", view)
	}
	if strings.Contains(view, "CONNECTION") || strings.Contains(view, "DEVICE") {
		t.Fatalf("device status table leaked into Wi-Fi submenu: %q", view)
	}
}

func TestPhysicalDiskPartitionFilter(t *testing.T) {
	for _, partition := range []struct {
		device, mountpoint, filesystem string
		want                           bool
	}{
		{device: "/dev/nvme0n1p2", mountpoint: "/", filesystem: "ext4", want: true},
		{device: "/dev/sdb1", mountpoint: "/run/media/user/usb", filesystem: "vfat", want: true},
		{device: "/dev/loop0", mountpoint: "/snap/app", filesystem: "squashfs", want: false},
		{device: "tmpfs", mountpoint: "/run", filesystem: "tmpfs", want: false},
		{device: "/dev/loop2", mountpoint: "/mnt/image", filesystem: "ext4", want: false},
	} {
		got := isPhysicalDiskPartition(disk.PartitionStat{Device: partition.device, Mountpoint: partition.mountpoint, Fstype: partition.filesystem})
		if got != partition.want {
			t.Errorf("isPhysicalDiskPartition(%+v) = %t, want %t", partition, got, partition.want)
		}
	}
}

func TestSystemMonitorViewFitsTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{{100, 30}, {60, 20}, {40, 12}, {24, 16}} {
		model := initialModel()
		model.width, model.height, model.screen = size.width, size.height, monitorScreen
		view := model.View()
		if got := lipgloss.Width(view); got != model.width {
			t.Errorf("fullscreen view width at %dx%d = %d, want %d", size.width, size.height, got, model.width)
		}
		if got := lipgloss.Height(view); got != model.height {
			t.Errorf("fullscreen view height at %dx%d = %d, want %d", size.width, size.height, got, model.height)
		}
	}
}

func TestTrafficGraphAndFormatting(t *testing.T) {
	if got := trafficGraph([]float64{0, 5, 10}); got != "·▄█" {
		t.Fatalf("trafficGraph() = %q, want ·▄█", got)
	}
	if got := formatByteRate(1024); got != "1.0 KiB/s" {
		t.Errorf("formatByteRate() = %q, want 1.0 KiB/s", got)
	}
	if math.Abs(bytesToGiB(1<<30)-1) > 0.0001 {
		t.Error("bytesToGiB conversion failed")
	}
}

func TestDigitalClockHasSeconds(t *testing.T) {
	clock := digitalClock(time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC), lipgloss.NewStyle())
	if strings.Count(clock, "\n") != 4 || !strings.Contains(clock, "███") {
		t.Fatalf("digital clock was not rendered as a five-row display: %q", clock)
	}
}
