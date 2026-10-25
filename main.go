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
	cfg     *Config
	cfgPath string
	dir     string
	state   *stateStore
	log     *Logger
}

type onceOptions struct {
	force   bool
	dryRun  bool
	url     string
	verbose bool
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
  %s once [--force] [--dry-run] [--url PORTAL_URL]
  %s login [--url PORTAL_URL] [--dry-run]
  %s run [--interval SECONDS]
  %s status [--json]
  %s discover
  %s install [--interval SECONDS]
  %s uninstall
  %s version

Common flags:
  --config PATH   use a different config file
  --verbose       also write log lines to stderr

Config (created by init):
  %s

Docs: https://github.com/pritkr/omada-autologin
`, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, configLocationHint())
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
	if err := saveConfig(path, &c); err != nil {
		return fail(err)
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Printf("next: %s once --dry-run\n", appName)
	if os.Geteuid() != 0 {
		fmt.Printf("then: %s install\n", appName)
	}
	return 0
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

	if err := a.once(onceOptions{force: *force, dryRun: *dryRun, url: *portalURL}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func (a *app) once(opts onceOptions) error {
	if opts.url != "" {
		ctx, err := parsePortalURL(opts.url, a.cfg.PortalHosts)
		if err != nil {
			return err
		}
		if !opts.force && !opts.dryRun {
			if res := probe(a.cfg, a.cfg.timeout()); res.Online {
				a.log.Printf("already online; nothing to do (use --force to test a login)")
				return nil
			}
		}
		return a.attempt(ctx, opts)
	}

	res := probe(a.cfg, a.cfg.timeout())
	if res.Online && !opts.dryRun {
		if !opts.force {
			return nil
		}
		ctx := a.cachedPortal()
		if ctx == nil {
			return errors.New("already online and no cached portal URL to force a login with")
		}
		return a.attempt(ctx, opts)
	}

	var ctx *PortalContext
	if res.Context != nil {
		ctx = res.Context
	} else if res.Online {
		fmt.Println("online: yes (nothing to log in to)")
		return nil
	} else {
		ctx = a.cachedPortal()
		if ctx == nil {
			if res.Detail != "" {
				return fmt.Errorf("not online and no portal URL: %s", res.Detail)
			}
			return errors.New("not online and no captive portal redirect was found")
		}
		a.log.Printf("no portal redirect parsed; using cached portal URL")
	}
	return a.attempt(ctx, opts)
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

func (a *app) attempt(ctx *PortalContext, opts onceOptions) error {
	if !opts.force && !opts.dryRun && a.state.inBackoff() {
		if opts.verbose {
			a.log.Printf("backoff active after %d failures; skipping this attempt", failThreshold)
		}
		return nil
	}

	lk, ok, err := acquireLock(a.dir)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	if !ok {
		if opts.verbose {
			a.log.Printf("another instance is running; skipping")
		}
		return nil
	}
	defer lk.release()

	err = doLogin(a.cfg, ctx, opts.dryRun, a.log)
	if opts.dryRun {
		return err
	}
	if err != nil {
		a.state.recordFailure(err.Error())
		return err
	}
	a.state.recordSuccess(ctx.PortalURL)
	return nil
}

func cmdRun(args []string) int {
	fs, cfgFlag, verboseFlag := newFlagSet("run")
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
		a.cfg.IntervalSeconds = *interval
	}
	if a.cfg.IntervalSeconds < 5 {
		return fail(errors.New("interval must be at least 5 seconds"))
	}

	a.log.Printf("starting loop: interval=%ds portal_hosts=%v", a.cfg.IntervalSeconds, a.cfg.PortalHosts)
	fmt.Printf("%s running every %ds (Ctrl-C to stop)\n", appName, a.cfg.IntervalSeconds)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ticker := time.NewTicker(a.cfg.interval())
	defer ticker.Stop()

	run := func() {
		if err := a.once(onceOptions{}); err != nil && verbose {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
	run()
	for {
		select {
		case <-ticker.C:
			run()
		case s := <-sig:
			a.log.Printf("stopping on %s", s)
			return 0
		}
	}
}

type statusReport struct {
	Online     bool   `json:"online"`
	PortalURL  string `json:"portal_url,omitempty"`
	Username   string `json:"username"`
	ConfigPath string `json:"config_path"`
	StatePath  string `json:"state_path"`
	LogPath    string `json:"log_path"`
	FailCount  int    `json:"fail_count"`
	LastError  string `json:"last_error,omitempty"`
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
		Online:     res.Online,
		Username:   a.cfg.Username,
		ConfigPath: a.cfgPath,
		StatePath:  a.state.path(),
		LogPath:    a.log.Path(),
		FailCount:  st.FailCount,
		LastError:  st.LastError,
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
		a.cfg.IntervalSeconds = *interval
	}
	if a.cfg.IntervalSeconds < 5 {
		return fail(errors.New("interval must be at least 5 seconds"))
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
	if runtime.GOOS == "windows" && spec.Interval < time.Minute {
		// Task Scheduler cannot repeat faster than once a minute.
		spec.Interval = time.Minute
	}
	if err := installService(spec); err != nil {
		return fail(err)
	}
	fmt.Printf("installed %s service\n", appName)
	fmt.Printf("  executable: %s\n", exe)
	fmt.Printf("  config:     %s\n", a.cfgPath)
	fmt.Printf("  interval:   %s\n", spec.Interval)
	if runtime.GOOS == "windows" {
		fmt.Println("  note:       Windows Task Scheduler runs at most once per minute")
	}
	fmt.Printf("  log:        %s\n", spec.DefaultLog)
	return 0
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
