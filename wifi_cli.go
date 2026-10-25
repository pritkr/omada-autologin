package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// cmdWiFi inspects and manages the optional Wi-Fi recovery layer. It never
// stores a password: connecting relies on a profile already saved in the OS.
func cmdWiFi(args []string) int {
	var (
		cfgPath    string
		asJSON     bool
		force      bool
		verboseOut bool
	)
	fs := flag.NewFlagSet("wifi", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfgPath, "config", "", "config file path")
	fs.BoolVar(&asJSON, "json", false, "print JSON (status only)")
	fs.BoolVar(&force, "force", false, "connect even if the SSID is not saved or configured")
	fs.BoolVar(&verboseOut, "verbose", false, "log to stderr too")

	// Flags are accepted before the subcommand and interspersed with its
	// operands: `wifi --config P add X` and `wifi add X --config P` are the
	// same command. The first non-flag word is the subcommand.
	rest := args
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	rest = fs.Args()
	sub := "status"
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub = rest[0]
		rest = rest[1:]
	}
	var positional []string
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
		if len(rest) == 0 {
			break
		}
	}
	flagConfig = cfgPath
	verbose = verboseOut

	switch sub {
	case "status", "scan", "connect", "on", "off", "add", "remove":
	default:
		fmt.Fprintf(os.Stderr, "unknown wifi command %q\n\n", sub)
		fmt.Fprintln(os.Stderr, "usage: omada-autologin wifi [status|scan|connect SSID|on|off|add SSID|remove SSID]")
		return 2
	}
	wantsSSID := sub == "connect" || sub == "add" || sub == "remove"
	switch {
	case wantsSSID && (len(positional) != 1 || strings.TrimSpace(positional[0]) == ""):
		fmt.Fprintf(os.Stderr, "usage: omada-autologin wifi %s SSID\n", sub)
		return 2
	case !wantsSSID && len(positional) > 0:
		fmt.Fprintf(os.Stderr, "unexpected argument %q for wifi %s\n", positional[0], sub)
		return 2
	case asJSON && sub != "status":
		fmt.Fprintln(os.Stderr, "--json is only valid with `wifi status`")
		return 2
	case force && sub != "connect":
		fmt.Fprintln(os.Stderr, "--force is only valid with `wifi connect`")
		return 2
	}

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()

	switch sub {
	case "status":
		return a.wifiStatusCommand(asJSON)
	case "scan":
		return a.wifiScanCommand()
	case "connect":
		return a.wifiConnectCommand(strings.TrimSpace(positional[0]), force)
	case "on":
		return a.setWiFiEnabled(true)
	case "off":
		return a.setWiFiEnabled(false)
	case "add":
		return a.addWiFiTarget(strings.TrimSpace(positional[0]))
	case "remove":
		return a.removeWiFiTarget(strings.TrimSpace(positional[0]))
	}
	return 0
}

type wifiReport struct {
	Enabled     bool     `json:"enabled"`
	Interface   string   `json:"interface,omitempty"`
	SSID        string   `json:"ssid,omitempty"`
	IPv4        string   `json:"ipv4,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	MAC         string   `json:"mac,omitempty"`
	LinkUp      bool     `json:"link_up"`
	Reachable   bool     `json:"portal_reachable"`
	Targets     []string `json:"targets,omitempty"`
	LastSSID    string   `json:"last_ssid,omitempty"`
	Prefer      bool     `json:"prefer"`
	ResetStall  bool     `json:"reset_on_stall"`
	InterfaceID string   `json:"interface_override,omitempty"`
}

func (a *app) wifiReport() wifiReport {
	st := currentWiFiStatusWithSSID(a.cfg)
	state := a.state.load()
	return wifiReport{
		Enabled:    a.wifiEnabled(),
		Interface:  st.Interface,
		SSID:       st.SSID,
		IPv4:       st.IPv4,
		Gateway:    st.Gateway,
		MAC:        st.MAC,
		LinkUp:     st.LinkUp,
		Reachable:  portalReachable(a.cfg, state),
		Targets:    wifiTargets(a.cfg, state),
		LastSSID:   state.LastSSID,
		Prefer:     a.cfg.WiFi != nil && a.cfg.WiFi.Prefer,
		ResetStall: a.cfg.WiFi != nil && a.cfg.WiFi.ResetOnStall,
		InterfaceID: func() string {
			if a.cfg.WiFi == nil {
				return ""
			}
			return a.cfg.WiFi.Interface
		}(),
	}
}

func (a *app) wifiStatusCommand(asJSON bool) int {
	report := a.wifiReport()
	if asJSON {
		data, _ := marshalIndent(report)
		fmt.Print(string(data))
		return 0
	}
	fmt.Printf("auto-reconnect: %s\n", onOff(report.Enabled))
	fmt.Printf("interface:      %s\n", orUnknown(report.Interface))
	fmt.Printf("ssid:           %s\n", orUnknown(report.SSID))
	fmt.Printf("address:        %s\n", orUnknown(report.IPv4))
	fmt.Printf("gateway:        %s\n", orUnknown(report.Gateway))
	fmt.Printf("mac:            %s\n", orUnknown(report.MAC))
	fmt.Printf("portal:         %s\n", reachableText(report.Reachable))
	if len(report.Targets) > 0 {
		fmt.Printf("targets:        %s\n", strings.Join(report.Targets, ", "))
	} else {
		fmt.Println("targets:        (learned after the first successful login)")
	}
	if report.LastSSID != "" {
		fmt.Printf("last good ssid: %s\n", report.LastSSID)
	}
	if report.Prefer {
		fmt.Println("preference:     switch to a target even when another network works")
	}
	return 0
}

func (a *app) wifiScanCommand() int {
	visible, saved := scanSSIDs(wifiInterface(a.cfg))
	targets := map[string]bool{}
	for _, t := range wifiTargets(a.cfg, a.state.load()) {
		targets[t] = true
	}
	fmt.Printf("visible: %s\n", joinOrNone(visible))
	fmt.Printf("saved:   %s\n", joinOrNone(saved))
	if len(targets) > 0 {
		var list []string
		for t := range targets {
			list = append(list, t)
		}
		sort.Strings(list)
		fmt.Printf("targets: %s\n", strings.Join(list, ", "))
	}
	if len(saved) == 0 {
		fmt.Println("hint: save the network in System Settings / NetworkManager first; this tool never stores Wi-Fi passwords")
	}
	return 0
}

func (a *app) wifiConnectCommand(ssid string, force bool) int {
	iface := wifiInterface(a.cfg)
	if iface == "" {
		return fail(errors.New("no Wi-Fi interface found"))
	}
	saved := wifiKnownSSIDs(iface)
	targets := wifiTargets(a.cfg, a.state.load())
	// Prefer the case the OS or config already uses so the join request
	// matches the real SSID even when the user typed a different case.
	if match := matchSSID(saved, ssid); match != "" {
		ssid = match
	} else if match := matchSSID(targets, ssid); match != "" {
		ssid = match
	} else if !force {
		return fail(fmt.Errorf("%q is neither saved in the OS nor configured; save it first or pass --force", ssid))
	}
	fmt.Printf("connecting to %q on %s...\n", ssid, iface)
	if err := wifiConnectSSID(iface, ssid); err != nil {
		return fail(err)
	}
	if !a.waitForLink(30 * time.Second) {
		return fail(fmt.Errorf("connected to %q but no IPv4/default route appeared", ssid))
	}
	added := a.addTargetInMemory(ssid)
	if err := saveConfig(a.cfgPath, a.cfg); err != nil {
		return fail(err)
	}
	if added {
		fmt.Printf("connected to %q and saved it as a target\n", ssid)
	} else {
		fmt.Printf("connected to %q\n", ssid)
	}
	return 0
}

func (a *app) setWiFiEnabled(enabled bool) int {
	if a.cfg.WiFi == nil {
		a.cfg.WiFi = &WiFiConfig{}
	}
	a.cfg.WiFi.Enabled = enabled
	if a.cfg.WiFi.Prefer && !enabled {
		a.cfg.WiFi.Prefer = false
	}
	if err := saveConfig(a.cfgPath, a.cfg); err != nil {
		return fail(err)
	}
	if enabled {
		fmt.Printf("Wi-Fi auto-reconnect enabled")
		if targets := wifiTargets(a.cfg, a.state.load()); len(targets) > 0 {
			fmt.Printf(" (targets: %s)", strings.Join(targets, ", "))
		}
		fmt.Println()
	} else {
		fmt.Println("Wi-Fi auto-reconnect disabled")
	}
	return 0
}

// addTargetInMemory reports whether the target was new.
func (a *app) addTargetInMemory(ssid string) bool {
	if a.cfg.WiFi == nil {
		a.cfg.WiFi = &WiFiConfig{}
	}
	a.cfg.WiFi.Enabled = true
	for _, existing := range a.cfg.WiFi.SSIDs {
		if sameSSID(existing, ssid) {
			return false
		}
	}
	a.cfg.WiFi.SSIDs = append(a.cfg.WiFi.SSIDs, ssid)
	return true
}

func (a *app) addWiFiTarget(ssid string) int {
	added := a.addTargetInMemory(ssid)
	if err := saveConfig(a.cfgPath, a.cfg); err != nil {
		return fail(err)
	}
	if !added {
		fmt.Printf("%q is already a Wi-Fi target\n", ssid)
		return 0
	}
	fmt.Printf("added %q to Wi-Fi targets\n", ssid)
	return 0
}

// removeWiFiTarget removes a configured target and also forgets the learned
// last-good SSID when it matches. Clearing only the list would leave the
// daemon reconnecting to the learned SSID, which is surprising after an
// explicit remove.
func (a *app) removeWiFiTarget(ssid string) int {
	removed := false
	if a.cfg.WiFi != nil {
		out := a.cfg.WiFi.SSIDs[:0]
		configChanged := false
		for _, existing := range a.cfg.WiFi.SSIDs {
			if sameSSID(existing, ssid) {
				removed = true
				configChanged = true
				continue
			}
			out = append(out, existing)
		}
		if configChanged {
			a.cfg.WiFi.SSIDs = out
			if err := saveConfig(a.cfgPath, a.cfg); err != nil {
				return fail(err)
			}
		}
	}
	state := a.state.load()
	if sameSSID(state.LastSSID, ssid) {
		state.LastSSID = ""
		if err := a.state.save(state); err != nil {
			return fail(err)
		}
		removed = true
	}
	if !removed {
		return fail(fmt.Errorf("%q is not a configured or learned target", ssid))
	}
	fmt.Printf("removed %q from Wi-Fi targets\n", ssid)
	return 0
}

// matchSSID returns the spelling used by the OS/config for a case-insensitive
// match, or "" when the list has no match.
func matchSSID(list []string, want string) string {
	for _, item := range list {
		item = strings.TrimSpace(item)
		if sameSSID(item, want) {
			return item
		}
	}
	return ""
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "(none)"
	}
	return strings.Join(list, ", ")
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown)"
	}
	return s
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func reachableText(v bool) string {
	if v {
		return "reachable"
	}
	return "unreachable"
}
