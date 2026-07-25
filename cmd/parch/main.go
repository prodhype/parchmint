// parch — page archiver. Captures a URL as a single self-contained file
// that renders faithfully with no network: dialogs dismissed, lazy content
// loaded, videos frozen to stills, every resource embedded.
//
//	parch https://example.com                  → example.com.html
//	parch -o page.html https://example.com
//	parch -f mht https://example.com           → Chrome MHTML instead of HTML
//	parch -f png https://example.com           → full-page screenshot
//	parch -links new-tab https://example.com   → external links open in a new tab
//	parch -width 1280 https://example.com      → render at a 1280px-wide layout
//	parch -cache ~/.parch/cache https://…      → reuse an HTTP cache across runs
//	parch https://example.com > page.html      → content on stdout when piped
//
// Defaults, a shared cache directory, an output directory, and an output
// filename template can also be set in a TOML config file — see the internal
// config package. Precedence, high to low: CLI flags, ./.parch/config (nearest
// ancestor), ~/.parch/config, built-in defaults.
//
// Logs always go to stderr; captured content goes to the output file, or to
// stdout when -o - is given (or stdout is piped and no output is configured).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goodblaster/logos"
	"github.com/goodblaster/parchmint/capture"
	"github.com/goodblaster/parchmint/internal/config"
	"github.com/goodblaster/pscription/pipeline"
	"github.com/goodblaster/pscription/pscription"
	"github.com/goodblaster/pscription/runner"
)

// Built-in defaults, used when neither a flag nor the config file sets a value.
const (
	defFormat  = "html"
	defLinks   = "keep"
	defTimeout = 300
)

// version identifies the build; stamp releases with
// -ldflags "-X main.version=v1.2.3".
var version = "dev"

// subcommands maps each archive-ops command to its entry point — the
// dispatch table for both main and `parch help <cmd>`.
var subcommands = map[string]func([]string){
	"text":  runTextCommand,
	"lines": runLinesCommand,
	"find":  runFindCommand,
	"index": runIndexCommand,
	"mark":  runMarkCommand,
	"pdf":   runPdfCommand,
}

func main() {
	// Subcommands (the capture flow stays the bare `parch <url>` form).
	if len(os.Args) > 1 {
		if run, ok := subcommands[os.Args[1]]; ok {
			run(os.Args[2:])
			return
		}
		switch os.Args[1] {
		case "version", "-version", "--version":
			fmt.Println("parch " + version)
			return
		case "help":
			runHelpCommand(os.Args[2:])
			return
		}
	}

	var (
		output  string
		format  string
		links   string
		rxFile  string
		profile string
		cache   string
		headful bool
		timeout int
		width   int
		text    bool
		verbose bool
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <url>              capture a page as one file\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "       %s <command> [options] <args>   work with captured archives\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "\nCommands:")
		fmt.Fprintln(os.Stderr, "  text     plain text back out of an archive (-json: the raw layer; -blocks: NDJSON)")
		fmt.Fprintln(os.Stderr, "  lines    one plain-text line per paragraph block")
		fmt.Fprintln(os.Stderr, "  find     grep across archives' text layers (multi-file, -l/-c/-q/-r, regex/fuzzy)")
		fmt.Fprintln(os.Stderr, "  index    OCR an archive's images into its text layer")
		fmt.Fprintln(os.Stderr, "  mark     write a highlighted copy of an archive")
		fmt.Fprintln(os.Stderr, "  pdf      render an archive to a searchable, highlighted PDF")
		fmt.Fprintln(os.Stderr, "  version  print the parch version")
		fmt.Fprintln(os.Stderr, "  help     usage for a command (`parch help find`)")
		fmt.Fprintln(os.Stderr, "\nCapture options:")
		flag.PrintDefaults()
	}
	flag.StringVar(&output, "o", "", "output file; '-' for stdout (default: config filename, else derived from URL)")
	flag.StringVar(&output, "output", "", "alias of -o")
	flag.StringVar(&format, "f", defFormat, "output format: html (self-contained page), mht, pdf, png, jpeg, webp")
	flag.StringVar(&format, "format", defFormat, "alias of -f")
	flag.StringVar(&links, "links", defLinks, "link policy: keep (unchanged), new-tab (external links open in a new tab), disable (links kept but unclickable)")
	flag.StringVar(&rxFile, "rx", "", "run a .rx pscription as the prep before serializing (e.g. to log in first)")
	flag.StringVar(&profile, "profile", "", "persistent Chrome user-data dir; log in once and reuse the session")
	flag.StringVar(&cache, "cache", "", "shared Chrome HTTP cache dir; reused across runs to speed up repeat loads")
	flag.BoolVar(&headful, "show", false, "launch a visible browser (e.g. to log in at a wait:user step)")
	flag.IntVar(&timeout, "timeout", defTimeout, "timeout in seconds")
	var highlight stringsFlag
	flag.IntVar(&width, "width", 0, "viewport/layout width in px (0 = default 1600)")
	flag.BoolVar(&text, "text", true, "embed the text layer in HTML archives (read back with 'parch text <file>')")
	flag.Var(&highlight, "highlight", "wrap matches of this phrase in <mark> before capture (repeatable; same matching as 'parch find')")
	flag.BoolVar(&verbose, "v", false, "debug logging")
	flag.BoolVar(&verbose, "verbose", false, "alias of -v")
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	url := flag.Arg(0)
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}

	// Logs go to stderr so captured content can be piped from stdout.
	level := logos.LevelInfo
	if verbose {
		level = logos.LevelDebug
	}
	logger := logos.NewLogger(level, logos.ConsoleFormatter(), os.Stderr)
	logos.SetDefaultLogger(logger)

	// Layer in config: CLI flags override the config file, which overrides
	// built-in defaults. Load first, then resolve each option by precedence.
	cfg, loaded, err := config.Load()
	if err != nil {
		logger.With("error", err).Error("failed to read config")
		os.Exit(1)
	}
	set := explicitFlags()
	for _, path := range loaded {
		logger.With("config", path).Debug("loaded config")
	}

	format = pickStr(set, "f", format, cfg.Defaults.Format, defFormat)
	links = pickStr(set, "links", links, cfg.Defaults.Links, defLinks)
	profile = pickStr(set, "profile", profile, cfg.Defaults.Profile, "")
	cache = pickStr(set, "cache", cache, cfg.CacheDir, "")
	timeout = pickInt(set, "timeout", timeout, cfg.Defaults.Timeout, defTimeout)
	width = pickInt(set, "width", width, cfg.Defaults.Width, 0)

	backend := backendFor(format)
	if backend == nil {
		fmt.Fprintf(os.Stderr, "unknown format %q (want html, mht, pdf, png, jpeg, or webp)\n", format)
		os.Exit(2)
	}

	// Prep recipe: either the default staticize, or a custom pscription
	// (e.g. one that logs in via a headful wait-for-user step) whose steps
	// run before serialization, in the same browser session.
	var recipe pipeline.Recipe
	if rxFile != "" {
		p, err := pscription.ParseFile(rxFile)
		if err != nil {
			logger.With("error", err).Error("failed to parse pscription")
			os.Exit(1)
		}
		r, _, err := p.Recipe("staticize")
		if err != nil {
			logger.With("error", err).Error("failed to build recipe from pscription")
			os.Exit(1)
		}
		recipe = r
	} else {
		recipe = pipeline.Staticize()
	}

	switch links {
	case "keep":
		// default: links exactly as the page had them
	case "new-tab", "disable":
		recipe = recipe.Append(pipeline.RewriteLinksStep(links))
	default:
		fmt.Fprintf(os.Stderr, "unknown link policy %q (want keep, new-tab, or disable)\n", links)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	runCfg := runner.DefaultConfig()
	runCfg.Headful = headful
	if width > 0 {
		runCfg.ViewportWidth = int64(width)
	}
	if profile != "" {
		dir, err := prepareDir(config.ExpandPath(profile))
		if err != nil {
			logger.With("error", err).Error("bad profile dir")
			os.Exit(1)
		}
		runCfg.UserDataDir = dir
	}
	if cache != "" {
		dir, err := prepareDir(config.ExpandPath(cache))
		if err != nil {
			logger.With("error", err).Error("bad cache dir")
			os.Exit(1)
		}
		runCfg.DiskCacheDir = dir
		logger.With("cache", dir).Debug("using shared HTTP cache")
	}

	logger.With("url", url).With("format", format).Info("archiving")
	start := time.Now()

	opts := capture.Options{
		TextLayer: text && (format == "html" || format == "mht"),
		Highlight: highlight,
	}
	snap, err := capture.CaptureWithOptions(ctx, url, runCfg, recipe, backend, opts)
	if err != nil {
		logger.With("error", err).Error("capture failed")
		os.Exit(1)
	}

	// Decide the destination now that we have the title (needed by {title}).
	dest, toStdout := resolveDest(set["o"], output, cfg, url, snap.Title, backend.Ext(), start)

	if toStdout {
		if _, err := os.Stdout.Write(snap.Bytes); err != nil {
			logger.With("error", err).Error("failed to write to stdout")
			os.Exit(1)
		}
	} else {
		if dir := filepath.Dir(dest); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				logger.With("error", err).Error("failed to create output dir")
				os.Exit(1)
			}
		}
		if err := os.WriteFile(dest, snap.Bytes, 0644); err != nil {
			logger.With("error", err).Error("failed to write output")
			os.Exit(1)
		}
	}

	out := dest
	if toStdout {
		out = "(stdout)"
	}
	logger.With("output", out).
		With("title", snap.Title).
		With("bytes", len(snap.Bytes)).
		With("duration", time.Since(start).Round(time.Second).String()).
		Info("archived")
}

// runHelpCommand implements `parch help [cmd]`: the subcommand's own
// usage (via its -help path), or the top-level usage without one.
func runHelpCommand(args []string) {
	if len(args) == 0 {
		flag.Usage()
		return
	}
	if run, ok := subcommands[args[0]]; ok {
		run([]string{"-help"})
		return
	}
	fmt.Fprintf(os.Stderr, "parch: unknown command %q\n", args[0])
	flag.Usage()
	os.Exit(2)
}

func backendFor(format string) capture.Backend {
	switch format {
	case "html":
		return capture.SingleFile{}
	case "mht":
		return capture.MHT{}
	case "pdf":
		return capture.PDF{}
	case "png":
		return capture.Screenshot{}
	case "jpeg", "jpg":
		return capture.JPEG{}
	case "webp":
		return capture.WebP{}
	}
	return nil
}

// resolveDest picks the output path and whether to write to stdout, applying:
//   - "-o -"                → stdout
//   - explicit "-o <path>"  → that path, verbatim
//   - config output_dir or filename → a file built from them
//   - otherwise             → stdout when piped, else the URL-derived name in cwd
func resolveDest(explicitO bool, output string, cfg config.Config, url, title, ext string, now time.Time) (dest string, toStdout bool) {
	if output == "-" {
		return "", true
	}
	if explicitO {
		return output, false
	}

	name := cfg.Filename
	if name != "" {
		name = config.Render(name, config.Vars{URL: url, Title: title, Ext: ext, Now: now})
	}

	outputDir := config.ExpandPath(cfg.OutputDir)

	// With no output configured, keep the historical behavior: content to
	// stdout when piped, otherwise a URL-derived filename in the cwd.
	if name == "" && outputDir == "" {
		if !stdoutIsTerminal() {
			return "", true
		}
		return deriveFilename(url) + ext, false
	}

	if name == "" {
		name = deriveFilename(url) + ext
	}
	return filepath.Join(outputDir, name), false
}

// captureAliases maps long flag spellings to the canonical short names
// used in config-precedence checks, so `--format mht` counts as an
// explicit -f.
var captureAliases = map[string]string{"format": "f", "output": "o", "verbose": "v"}

// explicitFlags reports which flags were actually given on the command line
// (as opposed to left at their default), so config can fill only the rest.
// Aliases are folded onto their canonical name.
func explicitFlags() map[string]bool {
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		name := f.Name
		if canon, ok := captureAliases[name]; ok {
			name = canon
		}
		set[name] = true
	})
	return set
}

func pickStr(set map[string]bool, name, flagVal, cfgVal, def string) string {
	if set[name] {
		return flagVal
	}
	if cfgVal != "" {
		return cfgVal
	}
	return def
}

func pickInt(set map[string]bool, name string, flagVal, cfgVal, def int) int {
	if set[name] {
		return flagVal
	}
	if cfgVal != 0 {
		return cfgVal
	}
	return def
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// stringsFlag collects a repeatable string flag.
type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ", ") }
func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// prepareDir expands ~ (handled by the caller) and ensures the directory exists.
func prepareDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create dir %q: %w", dir, err)
	}
	return dir, nil
}

// deriveFilename turns a URL into a safe base filename.
func deriveFilename(url string) string {
	name := url
	if i := strings.Index(name, "://"); i != -1 {
		name = name[i+3:]
	}
	name = strings.TrimPrefix(name, "www.")
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '?', '&', '=', ':', '%', '#':
			return '_'
		}
		return r
	}, name)
	return strings.Trim(name, "_")
}
