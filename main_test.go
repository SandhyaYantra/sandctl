package main

import (
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	model.hasSnapshot = true
	model.snapshot = systemSnapshot{
		CPUPercent: []float64{10, 20, 30, 40},
		RAMTotal:   8 << 30, RAMUsed: 4 << 30, RAMAvailable: 4 << 30, RAMCached: 1 << 30, RAMFree: 512 << 20,
		RootDiskTotal: 10 << 30, RootDiskUsed: 8 << 30, RootDiskFree: 2 << 30,
		Load1: 1.25, Load5: 0.75, Load15: 0.5,
		Interface: "wlan0", LocalIP: "192.168.1.5", NetworkOK: true,
		Traffic: trafficRates{RXBytesPerSecond: 1024, TXBytesPerSecond: 512, TotalRX: 1 << 20, TotalTX: 512 << 10},
	}
	view := model.dashboardView(lipgloss.NewStyle())
	for _, expected := range []string{"CPU PER CORE", "C0", "10%", "Load Avg", "MEMORY", "Available", "Cached", "DISKS /", "8.0 GiB", "NETWORK", "wlan0", "192.168.1.5", "Download", "Upload"} {
		if !strings.Contains(view, expected) {
			t.Errorf("dashboard missing %q", expected)
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
