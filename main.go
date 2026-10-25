package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

var (
	version   = "dev"
	verbose   bool
	logToFile = true
)

type app struct {
	cfg       *Config
	cfgPath   string
	dir       string
	state     *stateStore
	log       *Logger
	usernames usernameRotator
}

type onceOptions struct {
	force   bool
	dryRun  bool
	url     string
	verbose bool
}

// checkOutcome tells the daemon loop whether the network is healthy and
// whether a login attempt actually ran.
type checkOutcome struct {
	Online    bool
	Attempted bool
	LoggedIn  bool
	Skipped   bool
}

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	case "version", "-v", "--version":
		fmt.Printf("%s %s\n", appName, version)
		return 0
	case "init":
		return cmdInit(rest)
	case "once", "login":
		return cmdOnce(cmd, rest)
	case "run":
		return cmdRun(rest)
	case "status":
		return cmdStatus(rest)
	case "discover":
		return cmdDiscover(rest)
	case "randomize":
		return cmdRandomize(rest)
	case "install":
		return cmdInstall(rest)
	case "uninstall":
		return cmdUninstall(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return 2
	}
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `%s — keep an Omada captive-portal session alive

Usage:
  %s init [--username U] [--password P] [--host H] [--force]
            [--random-range 001-112 | --fixed]
            [--rotate-last N --rotate-min A --rotate-max B]
  %s once [--force] [--dry-run] [--url PORTAL_URL]
  %s login [--url PORTAL_URL] [--dry-run]
  %s run [--interval SECONDS]
  %s status [--json]
  %s discover
  %s randomize [--range 001-112 | --off]
  %s install [--interval SECONDS]
  %s uninstall
  %s version

New configs randomize the last 3 username digits through 001-112 on every
login. Use init --fixed to keep one username, or randomize to change an
existing config.

Common flags:
  --config PATH   use a different config file
  --verbose       also write log lines to stderr

Config (created by init):
  %s

Docs: https://github.com/pritkr/omada-autologin
`, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, configLocationHint())
}

func configLocationHint() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "omada-autologin/config.json"
	}
	return filepath.Join(dir, appName, "config.json")
}

func newFlagSet(name string) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := fs.String("config", "", "config file path")
	v := fs.Bool("verbose", false, "log to stderr too")
	return fs, cfg, v
}

func finishFlags(fs *flag.FlagSet, cfg *string, v *bool) bool {
	flagConfig = *cfg
	verbose = *v
	return true
}

func openApp() (*app, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	st, err := newStateStore(dir)
	if err != nil {
		return nil, fmt.Errorf("prepare state directory: %w", err)
	}
	logPath := ""
	if logToFile {
		logPath = filepath.Join(dir, appName+".log")
	}
	log, err := newLogger(logPath, verbose)
	if err != nil {
		return nil, err
	}
	return &app{cfg: cfg, cfgPath: path, dir: dir, state: st, log: log}, nil
}

func (a *app) close() {
	_ = a.log.Close()
}

func cmdInit(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("init")
	username := fs.String("username", "", "portal username / registration number")
	password := fs.String("password", "", "portal password")
	host := fs.String("host", "", "portal host or IP (default 192.168.1.3)")
	portalURL := fs.String("portal-url", "", "optional last-known-good portal entry URL")
	rotateLast := fs.Int("rotate-last", 0, "randomize the last N digits of the username (default 3)")
	rotateMin := fs.Int("rotate-min", 0, "smallest randomized suffix, inclusive (default 1)")
	rotateMax := fs.Int("rotate-max", 0, "largest randomized suffix, inclusive (default: 112 for 3 digits)")
	randomRange := fs.String("random-range", "", "randomized suffix range, e.g. 001-112 (default)")
	fixed := fs.Bool("fixed", false, "keep the username exactly as given (disable suffix rotation)")
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	path, err := configPath()
	if err != nil {
		return fail(err)
	}
	if _, statErr := os.Stat(path); statErr == nil && !*force {
		return fail(fmt.Errorf("config already exists at %s (use --force to overwrite)", path))
	}

	c := defaultConfig()
	if *host != "" {
		c.PortalHosts = []string{*host}
	}
	c.Username = strings.TrimSpace(*username)
	c.Password = *password
	c.PortalURL = strings.TrimSpace(*portalURL)

	customRange := strings.TrimSpace(*randomRange) != ""
	customRotate := *rotateLast != 0 || *rotateMin != 0 || *rotateMax != 0
	switch {
	case *fixed && (customRange || customRotate):
		return fail(errors.New("--fixed cannot be combined with --rotate-last, --rotate-min, --rotate-max or --random-range"))
	case customRange && customRotate:
		return fail(errors.New("use either --random-range or --rotate-last/--rotate-min/--rotate-max, not both"))
	case *rotateLast < 0:
		return fail(errors.New("--rotate-last cannot be negative"))
	case *rotateLast == 0 && (*rotateMin != 0 || *rotateMax != 0):
		return fail(errors.New("--rotate-min and --rotate-max require --rotate-last"))
	}
	switch {
	case *fixed:
		c.UsernameRotation = nil
	case customRange:
		digits, min, max, err := parseSuffixRange(*randomRange)
		if err != nil {
			return fail(err)
		}
		c.UsernameRotation = &UsernameRotation{Enabled: true, Digits: digits, Min: min, Max: max}
	case *rotateLast > 0:
		min := *rotateMin
		if min == 0 {
			min = 1
		}
		max := *rotateMax
		if max == 0 {
			max = defaultSuffixMaxFor(*rotateLast)
		}
		c.UsernameRotation = &UsernameRotation{Enabled: true, Digits: *rotateLast, Min: min, Max: max}
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if interactive && c.Username == "" {
		v, err := prompt("Username / registration number: ", false)
		if err != nil {
			return fail(err)
		}
		c.Username = strings.TrimSpace(v)
	}
	if interactive && c.Password == "" {
		v, err := prompt("Password: ", true)
		if err != nil {
			return fail(err)
		}
		c.Password = v
	}
	if interactive && *host == "" {
		v, err := prompt("Portal host ["+c.PortalHosts[0]+"]: ", false)
		if err != nil {
			return fail(err)
		}
		if v = strings.TrimSpace(v); v != "" {
			c.PortalHosts = []string{v}
		}
	}
	if interactive && !*force {
		// Offer the current known-good URL as a seed for the fallback path.
		fmt.Println("Optional: paste a portal entry URL (Enter to skip).")
		v, err := prompt("Portal URL: ", false)
		if err != nil {
			return fail(err)
		}
		if v = strings.TrimSpace(v); v != "" {
			c.PortalURL = v
		}
	}
	if err := checkRotationUsername(&c, "--fixed"); err != nil {
		return fail(err)
	}
	if err := saveConfig(path, &c); err != nil {
		return fail(err)
	}
	fmt.Printf("wrote %s\n", path)
	if desc := c.UsernameRotation.description(); desc != "" {
		fmt.Printf("username rotation: %s\n", desc)
	}
	fmt.Printf("next: %s once --dry-run\n", appName)
	if os.Geteuid() != 0 {
		fmt.Printf("then: %s install\n", appName)
	}
	return 0
}

func checkRotationUsername(c *Config, fixedHint string) error {
	r := c.UsernameRotation
	if r == nil || !r.Enabled {
		return nil
	}
	digits := r.digitsOrDefault()
	if hasTrailingDigits(c.Username, digits) {
		return nil
	}
	return fmt.Errorf("cannot rotate username: %q must end with %d digits; use %s to keep it fixed, or choose a username and range with matching widths",
		c.Username, digits, fixedHint)
}

func prompt(label string, secret bool) (string, error) {
	fmt.Print(label)
	if secret && term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func cmdOnce(name string, args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet(name)
	force := fs.Bool("force", name == "login", "log in even if the internet already works")
	dryRun := fs.Bool("dry-run", false, "discover and print the login request without sending credentials")
	portalURL := fs.String("url", "", "use this portal entry URL instead of discovering one")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()

	if _, err := a.once(onceOptions{force: *force, dryRun: *dryRun, url: *portalURL}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func (a *app) once(opts onceOptions) (checkOutcome, error) {
	if opts.url != "" {
		ctx, err := parsePortalURL(opts.url, a.cfg.PortalHosts)
		if err != nil {
			return checkOutcome{}, err
		}
		if !opts.force && !opts.dryRun {
			if res := probe(a.cfg, a.cfg.timeout()); res.Online {
				a.log.Printf("already online; nothing to do (use --force to test a login)")
				return checkOutcome{Online: true}, nil
			}
		}
		attempted, err := a.attempt(ctx, opts)
		if err != nil {
			return checkOutcome{Attempted: attempted}, err
		}
		if opts.dryRun {
			return checkOutcome{Attempted: attempted}, nil
		}
		if !attempted {
			return checkOutcome{Skipped: true}, nil
		}
		return checkOutcome{Online: true, Attempted: true, LoggedIn: true}, nil
	}

	res := probe(a.cfg, a.cfg.timeout())
	if res.Online && !opts.dryRun {
		if !opts.force {
			return checkOutcome{Online: true}, nil
		}
		ctx := a.cachedPortal()
		if ctx == nil {
			return checkOutcome{Online: true}, errors.New("already online and no cached portal URL to force a login with")
		}
		attempted, err := a.attempt(ctx, opts)
		if err != nil {
			return checkOutcome{Online: true, Attempted: attempted}, err
		}
		if !attempted {
			return checkOutcome{Online: true, Skipped: true}, nil
		}
		return checkOutcome{Online: true, Attempted: true, LoggedIn: true}, nil
	}

	var ctx *PortalContext
	switch {
	case res.Context != nil:
		ctx = res.Context
	case res.Online:
		fmt.Println("online: yes (nothing to log in to)")
		return checkOutcome{Online: true}, nil
	default:
		ctx = a.cachedPortal()
		if ctx == nil {
			if res.FoundURL != "" {
				return checkOutcome{}, fmt.Errorf("captive portal found at %s but it is not an allowed Omada entry URL", res.FoundURL)
			}
			if res.Detail != "" {
				return checkOutcome{}, fmt.Errorf("not online and no portal URL: %s", res.Detail)
			}
			return checkOutcome{}, errors.New("not online and no captive portal redirect was found")
		}
		a.log.Printf("no portal redirect parsed; using cached portal URL")
	}

	attempted, err := a.attempt(ctx, opts)
	if err != nil {
		return checkOutcome{Attempted: attempted}, err
	}
	if opts.dryRun {
		return checkOutcome{Attempted: attempted}, nil
	}
	if !attempted {
		return checkOutcome{Skipped: true}, nil
	}
	return checkOutcome{Online: true, Attempted: true, LoggedIn: true}, nil
}

func (a *app) cachedPortal() *PortalContext {
	for _, raw := range []string{a.cfg.PortalURL, a.state.load().LastPortalURL} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ctx, err := parsePortalURL(raw, a.cfg.PortalHosts)
		if err != nil {
			a.log.Printf("cached portal URL rejected: %v", err)
			continue
		}
		return ctx
	}
	return nil
}

func (a *app) attempt(ctx *PortalContext, opts onceOptions) (bool, error) {
	if !opts.force && !opts.dryRun && a.state.inBackoff() {
		if opts.verbose {
			a.log.Printf("backoff active after %d failures; skipping this attempt", failThreshold)
		}
		return false, nil
	}

	lk, ok, err := acquireLock(a.dir)
	if err != nil {
		return false, fmt.Errorf("lock: %w", err)
	}
	if !ok {
		if opts.verbose {
			a.log.Printf("another instance is running; skipping")
		}
		return false, nil
	}
	defer lk.release()

	username, err := a.usernames.next(a.cfg)
	if err != nil {
		return false, err
	}
	err = doLoginAs(a.cfg, ctx, username, opts.dryRun, a.log)
	if opts.dryRun {
		return true, err
	}
	if err != nil {
		a.state.recordFailure(err.Error())
		return true, err
	}
	a.state.recordSuccess(ctx.PortalURL)
	return true, nil
}

func cmdRun(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("run")
	interval := fs.Int("interval", 0, "online check interval in seconds (overrides config)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()
	if *interval > 0 {
		a.cfg.IntervalSeconds = *interval
	}
	if err := a.cfg.validate(); err != nil {
		return fail(err)
	}

	// On Linux this wakes immediately when NetworkManager changes the link or
	// an address, so a reconnect is handled in about a second. Elsewhere it
	// is a no-op and the normal interval is the only trigger.
	wake := make(chan struct{}, 1)
	stopWatch := watchNetwork(wake)
	defer stopWatch()

	a.log.Printf("starting daemon: online=%ds retry=%ds..%ds portal_hosts=%v",
		a.cfg.IntervalSeconds, a.cfg.RetryMinSeconds, a.cfg.RetryMaxSeconds, a.cfg.PortalHosts)
	if desc := a.cfg.UsernameRotation.description(); desc != "" {
		a.log.Printf("username rotation enabled: %s", desc)
	}
	fmt.Printf("%s running (online check every %ds, instant on network change)\n", appName, a.cfg.IntervalSeconds)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)

	retry := a.cfg.retryMin()
	for {
		out, runErr := a.once(onceOptions{})
		if runErr != nil && verbose {
			fmt.Fprintln(os.Stderr, "error:", runErr)
		}
		delay, next := a.nextDelay(out, runErr, retry)
		retry = next

		timer := time.NewTimer(delay)
	wait:
		for {
			select {
			case <-timer.C:
				break wait
			case <-wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				a.state.clearFailures()
				retry = a.cfg.retryMin()
				a.log.Printf("network change detected; checking now")
				break wait
			case s := <-sig:
				if s == syscall.SIGHUP {
					if err := a.reload(*interval); err != nil {
						a.log.Printf("reload failed: %v", err)
					} else {
						a.log.Printf("config reloaded: online=%ds retry=%ds..%ds",
							a.cfg.IntervalSeconds, a.cfg.RetryMinSeconds, a.cfg.RetryMaxSeconds)
					}
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					retry = a.cfg.retryMin()
					break wait
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				a.log.Printf("stopping on %s", s)
				return 0
			}
		}
		timer.Stop()
	}
}

// nextDelay implements the daemon's adaptive schedule: sleep for the normal
// interval while healthy, and back off exponentially while the portal is
// refusing or the network is down.
func (a *app) nextDelay(out checkOutcome, err error, retry time.Duration) (time.Duration, time.Duration) {
	if out.Online && err == nil {
		return a.cfg.interval(), a.cfg.retryMin()
	}
	if retry < a.cfg.retryMin() {
		retry = a.cfg.retryMin()
	}
	delay := retry
	next := retry * 2
	if next > a.cfg.retryMax() {
		next = a.cfg.retryMax()
	}
	return delay, next
}

func (a *app) reload(intervalOverride int) error {
	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		return err
	}
	if intervalOverride > 0 {
		cfg.IntervalSeconds = intervalOverride
	}
	a.cfg = cfg
	a.usernames.reset()
	return nil
}

type statusReport struct {
	Online           bool   `json:"online"`
	PortalURL        string `json:"portal_url,omitempty"`
	Username         string `json:"username"`
	UsernameRotation string `json:"username_rotation,omitempty"`
	ConfigPath       string `json:"config_path"`
	StatePath        string `json:"state_path"`
	LogPath          string `json:"log_path"`
	FailCount        int    `json:"fail_count"`
	LastError        string `json:"last_error,omitempty"`
}

func cmdStatus(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("status")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()

	res := probe(a.cfg, a.cfg.timeout())
	st := a.state.load()
	report := statusReport{
		Online:           res.Online,
		Username:         a.cfg.Username,
		UsernameRotation: a.cfg.UsernameRotation.description(),
		ConfigPath:       a.cfgPath,
		StatePath:        a.state.path(),
		LogPath:          a.log.Path(),
		FailCount:        st.FailCount,
		LastError:        st.LastError,
	}
	if res.Context != nil {
		report.PortalURL = res.Context.PortalURL
	} else if res.FoundURL != "" {
		report.PortalURL = res.FoundURL
	} else if ctx := a.cachedPortal(); ctx != nil {
		report.PortalURL = ctx.PortalURL
	}

	if *asJSON {
		data, _ := marshalIndent(report)
		fmt.Print(string(data))
		return 0
	}
	if report.Online {
		fmt.Println("online: yes")
	} else {
		fmt.Println("online: no")
	}
	if report.PortalURL != "" {
		fmt.Printf("portal: %s\n", report.PortalURL)
	} else {
		fmt.Println("portal: not found")
	}
	fmt.Printf("user:   %s\n", report.Username)
	if report.UsernameRotation != "" {
		fmt.Printf("rotate: %s\n", report.UsernameRotation)
	}
	fmt.Printf("config: %s\n", report.ConfigPath)
	fmt.Printf("log:    %s\n", report.LogPath)
	if st.FailCount > 0 {
		fmt.Printf("fails:  %d (last: %s)\n", st.FailCount, st.LastError)
	}
	return 0
}

func cmdDiscover(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("discover")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()

	res := probe(a.cfg, a.cfg.timeout())
	switch {
	case res.Context != nil:
		fmt.Println(res.Context.PortalURL)
		return 0
	case res.FoundURL != "":
		fmt.Println(res.FoundURL)
		return 0
	case res.Online:
		fmt.Fprintln(os.Stderr, "online: no captive portal")
		return 1
	}
	if ctx := a.cachedPortal(); ctx != nil {
		fmt.Println(ctx.PortalURL)
		return 0
	}
	fmt.Fprintln(os.Stderr, "no portal URL found")
	return 1
}

func cmdRandomize(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("randomize")
	rangeFlag := fs.String("range", "", "suffix range to rotate through (default 001-112)")
	off := fs.Bool("off", false, "disable username suffix rotation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	if *off && strings.TrimSpace(*rangeFlag) != "" {
		fmt.Fprintln(os.Stderr, "use either --range or --off, not both")
		return 2
	}

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()

	if *off {
		a.cfg.UsernameRotation = nil
		if err := saveConfig(a.cfgPath, a.cfg); err != nil {
			return fail(err)
		}
		fmt.Printf("username rotation disabled; using %s\n", a.cfg.Username)
	} else {
		raw := strings.TrimSpace(*rangeFlag)
		if raw == "" {
			raw = fmt.Sprintf("%0*d-%0*d", defaultSuffixDigits, 1, defaultSuffixDigits, defaultSuffixMax)
		}
		digits, min, max, err := parseSuffixRange(raw)
		if err != nil {
			return fail(err)
		}
		a.cfg.UsernameRotation = &UsernameRotation{Enabled: true, Digits: digits, Min: min, Max: max}
		if err := checkRotationUsername(a.cfg, "--off"); err != nil {
			return fail(err)
		}
		if err := saveConfig(a.cfgPath, a.cfg); err != nil {
			return fail(err)
		}
		prefix := a.cfg.Username[:len(a.cfg.Username)-digits]
		fmt.Printf("username rotation: %s\n", a.cfg.UsernameRotation.description())
		fmt.Printf("usernames:         %s%0*d ... %s%0*d\n",
			prefix, digits, min, prefix, digits, max)
	}
	fmt.Printf("saved %s\n", a.cfgPath)
	fmt.Println("reload or restart the daemon to apply it")
	return 0
}

func cmdInstall(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("install")
	interval := fs.Int("interval", 0, "seconds between checks (overrides config)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	a, err := openApp()
	if err != nil {
		return fail(err)
	}
	defer a.close()
	if *interval > 0 {
		// Persist the override so the installed daemon and `status` agree,
		// and so later edits plus SIGHUP actually take effect.
		a.cfg.IntervalSeconds = *interval
		if err := saveConfig(a.cfgPath, a.cfg); err != nil {
			return fail(err)
		}
	}
	if err := a.cfg.validate(); err != nil {
		return fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(fmt.Errorf("locate executable: %w", err))
	}
	exe, _ = filepath.EvalSymlinks(exe)

	spec := serviceSpec{
		Executable:  exe,
		ConfigPath:  a.cfgPath,
		Interval:    a.cfg.interval(),
		LogPath:     filepath.Join(a.dir, "service.log"),
		DefaultLog:  filepath.Join(a.dir, appName+".log"),
		Description: "Omada captive-portal auto-login",
	}
	if err := installService(spec); err != nil {
		return fail(err)
	}
	fmt.Printf("installed %s service\n", appName)
	fmt.Printf("  executable: %s\n", exe)
	fmt.Printf("  config:     %s\n", a.cfgPath)
	fmt.Printf("  mode:       daemon with restart-on-failure\n")
	fmt.Printf("  checks:     every %s online, %s..%s while offline\n",
		spec.Interval, a.cfg.retryMin(), a.cfg.retryMax())
	fmt.Printf("  log:        %s\n", spec.DefaultLog)
	for _, note := range platformNotes() {
		fmt.Printf("  note:       %s\n", note)
	}
	return 0
}

// platformNotes adds distro/desktop hints without changing service behaviour.
func platformNotes() []string {
	var notes []string
	if runtime.GOOS != "linux" {
		return notes
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		osr := strings.ToLower(string(data))
		if strings.Contains(osr, "id=cachyos") || strings.Contains(osr, "id=arch") {
			notes = append(notes, "Arch/CachyOS detected: static binary, no runtime packages needed")
		}
	}
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" ||
		strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), "hyprland") {
		notes = append(notes, "Hyprland detected: the systemd user service follows your session")
	}
	notes = append(notes, "link/address changes are handled immediately via netlink")
	return notes
}

func cmdUninstall(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("uninstall")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	finishFlags(fs, cfgFlag, verboseFlag)

	if err := uninstallService(); err != nil {
		return fail(err)
	}
	fmt.Printf("removed %s service\n", appName)
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
