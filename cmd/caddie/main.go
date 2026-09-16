// Command caddie is the Go entry point for the caddie tool.
package main

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Portauw/caddie/internal/config"
	"github.com/Portauw/caddie/internal/export"
	"github.com/Portauw/caddie/internal/repos"
	"github.com/Portauw/caddie/internal/skills"
	"github.com/Portauw/caddie/internal/version"
)

type handler func(args []string)

var nativeCommands = map[string]handler{
	"--version": cmdVersion,
	"-v":        cmdVersion,
	"which":     cmdWhich,
	"active":    cmdWhich,
	"source":    cmdSource,
	"edit":      cmdEdit,
	"repo":      cmdRepo,
	"inventory": cmdInventory,
	"help":      cmdHelp,
	"--help":    cmdHelp,
	"-h":        cmdHelp,
	"reset":     cmdReset,
	"init":      cmdInit,
	"setup":     cmdSetup,
	"scan":      cmdScan,
	"activate":  cmdActivate,
	"use":       cmdActivate,
	"export":    cmdExport,
}

const (
	ansiBold   = "\033[1m"
	ansiReset  = "\033[0m"
	ansiCyan   = "\033[0;36m"
	ansiDim    = "\033[2m"
	ansiYellow = "\033[1;33m"
	ansiBlue   = "\033[0;34m"
	ansiRed    = "\033[0;31m"
	ansiGreen  = "\033[0;32m"
)

// isTerminal reports whether f refers to a tty (used to suppress prompts
// when stdin is piped, matching contract-test expectations).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func die(msg string) {
	fmt.Fprintf(os.Stderr, "%s✗%s  %s\n", ansiRed, ansiReset, msg)
	os.Exit(1)
}

// resolveProfileOrDie returns the nearest .caddie.yaml profile for the current
// working directory, or dies with the standard not-found message. Extra hints
// are appended as further lines, for commands that have an alternative to
// creating a profile.
func resolveProfileOrDie(hints ...string) string {
	cwd, err := os.Getwd()
	if err != nil {
		die(err.Error())
	}
	profilePath := config.FindProfile(cwd)
	if profilePath == "" {
		msg := fmt.Sprintf("No caddie profile found for %s\n   Run %scaddie init%s to create one.",
			cwd, ansiCyan, ansiReset)
		for _, h := range hints {
			msg += "\n   " + h
		}
		die(msg)
	}
	return profilePath
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		cmdHelp(nil)
		return
	}
	if fn, ok := nativeCommands[args[0]]; ok {
		fn(args[1:])
		return
	}
	die(fmt.Sprintf("Unknown command: %s\nRun 'caddie --help' for usage.", args[0]))
}

func cmdVersion(_ []string) {
	fmt.Printf("caddie v%s\n", version.Version)
}

// cmdSource prints a migration message for the removed `source` family.
// Plugin sources were dropped in favor of git repos.
func cmdSource(_ []string) {
	die("`caddie source` has been removed. Use `caddie repo` to manage skill sources.")
}

func cmdEdit(args []string) {
	if len(args) > 0 {
		die("Usage: caddie edit  (opens the nearest .caddie.yaml)")
	}
	profilePath := resolveProfileOrDie()

	editor := cmp.Or(os.Getenv("EDITOR"), "vim")
	// `sh -c` preserves $EDITOR's word-splitting (e.g. "code --wait").
	cmd := exec.Command("sh", "-c", editor+` "$0"`, profilePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	fmt.Printf("%s✓%s  Updated: %s\n", ansiGreen, ansiReset, profilePath)
}

func cmdRepo(args []string) {
	if len(args) == 0 {
		die("Usage: caddie repo <list|add|remove|update>")
	}
	sub := args[0]
	switch sub {
	case "list", "ls":
		cmdRepoList(args[1:])
	case "add":
		cmdRepoAdd(args[1:])
	case "remove", "rm":
		cmdRepoRemove(args[1:])
	case "update":
		cmdRepoUpdate(args[1:])
	default:
		die("Unknown repo command: " + sub)
	}
}

func cmdRepoList(_ []string) {
	entries, err := repos.Parse()
	if err != nil {
		die(err.Error())
	}
	if len(entries) == 0 {
		fmt.Printf("%s⚠%s  No git repos registered. Add one with: %scaddie repo add <name> <url>%s\n",
			ansiYellow, ansiReset, ansiCyan, ansiReset)
		return
	}
	fmt.Printf("%sRegistered git repos:%s\n\n", ansiBold, ansiReset)

	for _, e := range entries {
		repoDir, nameErr := repos.CheckoutDir(e.Name)
		status := fmt.Sprintf("%s(not cloned)%s", ansiDim, ansiReset)
		if nameErr != nil {
			status = fmt.Sprintf("%s(unusable name)%s", ansiYellow, ansiReset)
		}
		if info, err := os.Stat(filepath.Join(repoDir, ".git")); nameErr == nil && err == nil && info.IsDir() {
			branch := cmp.Or(repos.GitOutput(repoDir, "rev-parse", "--abbrev-ref", "HEAD"), "?")
			shortHash := cmp.Or(repos.GitOutput(repoDir, "rev-parse", "--short", "HEAD"), "?")
			status = fmt.Sprintf("%s%s@%s%s", ansiGreen, branch, shortHash, ansiReset)
		}
		fmt.Printf("  %s%s%s  %s\n", ansiBold, e.Name, ansiReset, status)
		fmt.Printf("    %s%s%s\n", ansiDim, e.URL, ansiReset)

		prefixDisplay := "none"
		if e.UsesPrefix() {
			prefixDisplay = e.EffectivePrefix() + "-*"
		}
		fmt.Printf("    %sskills_path: %s, prefix: %s%s\n", ansiDim, e.SkillsPath, prefixDisplay, ansiReset)
		fmt.Println("")
	}
}

// cmdRepoAdd registers a git repo under `repos:` in sources.yaml, then
// shallow-clones the URL into $CONFIG_DIR/repos/<name>. Clone failures emit
// a warning but the registration still succeeds, so `caddie repo update` can
// retry later.
func cmdRepoAdd(args []string) {
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		die("Usage: caddie repo add <name> <url> [skills_path]\n  Example: caddie repo add lenny https://github.com/RefoundAI/lenny-skills skills")
	}
	name, url := args[0], args[1]
	// Before anything is written or cloned: the name becomes a directory
	// under repos.Dir() that `repo remove` later rm -rf's.
	repoDir, err := repos.CheckoutDir(name)
	if err != nil {
		die(err.Error())
	}
	skillsPath := "skills"
	if len(args) >= 3 && args[2] != "" {
		skillsPath = args[2]
	}

	exists, err := repos.Exists(name)
	if err != nil {
		die(err.Error())
	}
	if exists {
		die(fmt.Sprintf("Repo '%s' already registered. Remove it first with: caddie repo remove %s", name, name))
	}

	if err := repos.Append(repos.Entry{Name: name, URL: url, SkillsPath: skillsPath}); err != nil {
		die(err.Error())
	}

	if err := os.MkdirAll(repos.Dir(), 0o755); err != nil {
		die(err.Error())
	}
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		fmt.Printf("%sℹ%s  Cloning %s...\n", ansiBlue, ansiReset, url)
		if err := gitClone(url, repoDir); err == nil {
			walked, _ := skills.WalkRepoSkills(repoDir, skillsPath)
			skillCount := len(walked)
			fmt.Printf("%s✓%s  Cloned: %s%s%s (%d skills found)\n", ansiGreen, ansiReset, ansiBold, name, ansiReset, skillCount)
			if skillCount == 0 {
				displayPath := skillsPath
				if displayPath == "" || displayPath == "." {
					displayPath = "the repo root"
				}
				fmt.Printf("%s⚠%s  No SKILL.md files found under %s; the repo is registered but scan will be a no-op\n",
					ansiYellow, ansiReset, displayPath)
			}
		} else {
			fmt.Printf("%s⚠%s  Clone failed. Run %scaddie repo update %s%s to retry.\n", ansiYellow, ansiReset, ansiCyan, name, ansiReset)
		}
	}

	fmt.Printf("%s✓%s  Registered repo: %s%s%s\n", ansiGreen, ansiReset, ansiBold, name, ansiReset)
	fmt.Printf("%sℹ%s  Run %scaddie scan --force%s to sync skills into the store.\n", ansiBlue, ansiReset, ansiCyan, ansiReset)
}

// cmdRepoRemove unregisters a repo, removes store symlinks pointing into its
// checkout, and rm -rf's the checkout directory.
func cmdRepoRemove(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: caddie repo remove <name>")
	}
	name := args[0]

	hasRepos, err := repos.HasReposSection()
	if err != nil {
		die(err.Error())
	}
	if !hasRepos {
		die("No repos section found in sources.yaml")
	}
	exists, err := repos.Exists(name)
	if err != nil {
		die(err.Error())
	}
	if !exists {
		die(fmt.Sprintf("Repo '%s' not found.", name))
	}

	if err := repos.Remove(name); err != nil {
		die(err.Error())
	}

	removed := 0
	skills.EachSymlinkTarget(skills.Store(), func(_, full, target string) {
		if repos.LinkPointsTo(target, name) {
			if os.Remove(full) == nil {
				removed++
			}
		}
	})

	// The entry is gone from sources.yaml either way. Deleting the checkout
	// only happens for a name that still resolves inside repos.Dir() — an
	// entry registered before names were validated must be unregisterable
	// without this command rm -rf'ing whatever its name points at.
	repoDir, err := repos.CheckoutDir(name)
	if err != nil {
		fmt.Printf("%s⚠%s  Unregistered only: %s. Delete any stray checkout yourself.\n", ansiYellow, ansiReset, err)
	} else if info, err := os.Stat(repoDir); err == nil && info.IsDir() {
		if err := os.RemoveAll(repoDir); err != nil {
			die(err.Error())
		}
		fmt.Printf("%sℹ%s  Removed cloned repo: %s\n", ansiBlue, ansiReset, repoDir)
	}

	fmt.Printf("%s✓%s  Removed repo: %s%s%s (%d skill symlinks cleaned)\n", ansiGreen, ansiReset, ansiBold, name, ansiReset, removed)
}

func cmdInventory(args []string) {
	filter := ""
	if len(args) > 0 {
		filter = args[0]
	}
	if !skills.StoreExists() {
		die(fmt.Sprintf("Canonical store not found. Run %scaddie setup%s first.", ansiCyan, ansiReset))
	}

	fmt.Printf("%sSkill Inventory%s (%s%s%s)\n\n", ansiBold, ansiReset, ansiDim, skills.Store(), ansiReset)

	items, err := skills.Scan()
	if err != nil {
		die(err.Error())
	}

	// Group by prefix, preserving store sort order for item lists (python
	// iterates sorted(store.iterdir()) and appends in order).
	groups := map[string][]skills.Item{}
	for _, it := range items {
		groups[it.Prefix] = append(groups[it.Prefix], it)
	}
	prefixes := make([]string, 0, len(groups))
	for k := range groups {
		prefixes = append(prefixes, k)
	}
	sort.Strings(prefixes)

	for _, prefix := range prefixes {
		g := groups[prefix]
		fmt.Printf("  %s%s%s (%d skills)\n", ansiBold, prefix, ansiReset, len(g))
		for _, it := range g {
			skillID := fmt.Sprintf("%s:%s", prefix, it.Remainder)
			if filter != "" && !strings.Contains(skillID, filter) {
				continue
			}
			fmt.Printf("    %s\n", skillID)
		}
		fmt.Println()
	}

	fmt.Printf("Total: %d skills\n", len(items))
}

func cmdHelp(_ []string) {
	B, R, C, D, V := ansiBold, ansiReset, ansiCyan, ansiDim, version.Version
	fmt.Print(
		B + "caddie" + R + " v" + V + " — Skill Profile Manager for AI Coding Agents\n" +
			"\n" +
			B + "USAGE" + R + "\n" +
			"  caddie <command> [arguments] [flags]\n" +
			"\n" +
			B + "SETUP" + R + "\n" +
			"  " + C + "init" + R + "                        Create .caddie.yaml in the current folder\n" +
			"  " + C + "setup" + R + "                       One-time machine setup (config dir, skill\n" +
			"                              store, sources file, scan)\n" +
			"\n" +
			B + "PROFILE" + R + "\n" +
			"  " + C + "activate" + R + " (use)   [-f]        Resolve the nearest profile, reconcile its skills\n" +
			"                              (-f: force-pull repos)\n" +
			"  " + C + "which" + R + "    (active)            Print the resolved profile path\n" +
			"  " + C + "edit" + R + "                         Open the nearest profile in $EDITOR\n" +
			"  " + C + "reset" + R + "   [-f]                 Restore a clean state\n" +
			"\n" +
			B + "GIT REPOS" + R + "\n" +
			"  " + C + "repo" + R + " list                    Show registered git repos + status\n" +
			"  " + C + "repo" + R + " add <name> <url> [path] Register + clone a git skill repo\n" +
			"                              (path defaults to \"skills\"; use \".\" or \"\" if SKILL.md files\n" +
			"                              live at the repo root. Nested category dirs are walked.)\n" +
			"  " + C + "repo" + R + " remove <name>           Unregister repo + clean up symlinks\n" +
			"  " + C + "repo" + R + " update [name]           Pull latest changes (all or specific)\n" +
			"\n" +
			B + "DISCOVERY" + R + "\n" +
			"  " + C + "scan" + R + "    [-v] [-f]            Scan repos, sync to skill store (-f: bypass cache)\n" +
			"  " + C + "inventory" + R + "                    List all skills with prefix grouping\n" +
			"\n" +
			B + "EXPORT" + R + "\n" +
			"  " + C + "export" + R + "  --to <dir>            Copy resolved skills to local directory\n" +
			"  " + C + "export" + R + "  --to s3://b/p/        Upload resolved skills to S3\n" +
			"  " + C + "export" + R + "  --all --to <target>   Export all skills (no profile filter)\n" +
			"  Flags: --clean (remove stale), --dry-run (preview)\n" +
			"  Env:   AWS_PROFILE, AWS_ENDPOINT_URL (for S3 targets)\n" +
			"\n" +
			B + "FLAGS" + R + "\n" +
			"  -n, --dry-run             Show what would happen without executing\n" +
			"  -h, --help                Show this help message\n" +
			"  --version                 Print the caddie version\n" +
			"  -v, --verbose             Subcommand flag for detailed output, e.g. " + C + "scan -v" + R + "\n" +
			"                            (bare " + C + "caddie -v" + R + ", with no subcommand, prints the\n" +
			"                            version instead; it is short for --version there)\n" +
			"\n" +
			B + "SKILL PATTERNS" + R + "\n" +
			"  Patterns use prefix:name format with glob wildcards:\n" +
			"    \"lenny:*\"          all skills from the lenny repo\n" +
			"    \"lenny:ai-*\"       repo skills matching ai-* (e.g. ai-evals)\n" +
			"    \"local:*\"          all hand-written skills (no prefix dash)\n" +
			"    \"*\"                everything\n" +
			"\n" +
			B + "EXAMPLES" + R + "\n" +
			"  caddie init                              # Create a profile in this folder\n" +
			"  caddie setup                              # One-time machine setup\n" +
			"  caddie activate                           # Resolve nearest profile, sync skills\n" +
			"  caddie activate --dry-run                 # Preview what would change\n" +
			"  caddie activate --force                   # Pull all repos now (bypass hourly cache)\n" +
			"  caddie repo add lenny https://github.com/RefoundAI/lenny-skills  # Add git repo\n" +
			"  caddie inventory                          # See all available skills\n" +
			"\n" +
			B + "SHELL INTEGRATION" + R + "\n" +
			"  Add to ~/.zshrc (or ~/.bashrc):\n" +
			"\n" +
			"    claude() {\n" +
			"      caddie activate && command claude \"$@\"\n" +
			"    }\n" +
			"\n" +
			B + "PROJECT CONFIG" + R + "\n" +
			"  Create .caddie.yaml in your project root:\n" +
			"\n" +
			"    name: \"Backend API\"\n" +
			"    description: \"Python service\"\n" +
			"\n" +
			"    skills:\n" +
			"      - \"superpowers:*\"\n" +
			"      - \"itp-eng-backend:*\"\n" +
			"\n" +
			B + "FILES" + R + "\n" +
			"  " + D + "<folder>/.caddie.yaml" + R + "                 Profile (source of truth)\n" +
			"  " + D + "<folder>/.agents/skills/" + R + "              Managed skill symlinks\n" +
			"  " + D + "<folder>/.claude/skills" + R + "               Symlink to .agents/skills\n" +
			"  " + D + "~/.config/caddie/sources.yaml" + R + "         Registered skill repos\n" +
			"  " + D + "~/.config/caddie/skills/" + R + "              Namespaced skill store\n",
	)
}

// cmdRepoUpdate pulls every registered repo in parallel (or just one when
// args[0] is set). Returns nothing; the result is reflected in stdout and
// in the .last-pull mtime via the caller (when used as auto-pull).
func cmdRepoUpdate(args []string) {
	updated, _ := runRepoUpdate(args)
	if updated > 0 {
		fmt.Println()
		fmt.Printf("%sℹ%s  Run %scaddie scan --force%s to sync updated skills into the store.\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	}
}

// runRepoUpdate is the worker behind cmdRepoUpdate. Returns (successes, attempted).
// Output is buffered per-repo and flushed in registration order, so concurrent
// pulls don't interleave on stdout.
func runRepoUpdate(args []string) (updated, attempted int) {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}

	hasRepos, err := repos.HasReposSection()
	if err != nil {
		die(err.Error())
	}
	if !hasRepos {
		die("No git repos registered.")
	}

	entries, err := repos.Parse()
	if err != nil {
		die(err.Error())
	}

	var todo []repos.Entry
	for _, e := range entries {
		if target == "" || e.Name == target {
			todo = append(todo, e)
		}
	}
	if target != "" && len(todo) == 0 {
		die(fmt.Sprintf("Repo '%s' not found.", target))
	}

	if err := os.MkdirAll(repos.Dir(), 0o755); err != nil {
		die(err.Error())
	}

	const maxParallel = 4
	type result struct {
		out string
		ok  bool
	}
	results := make([]result, len(todo))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, e := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, e repos.Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i].out, results[i].ok = updateOneRepo(e)
		}(i, e)
	}
	wg.Wait()

	for _, r := range results {
		fmt.Print(r.out)
		if r.ok {
			updated++
		}
	}
	return updated, len(todo)
}

// updateOneRepo pulls (or clones) a single repo and returns its rendered
// status block plus whether anything actually changed.
func updateOneRepo(e repos.Entry) (string, bool) {
	var b strings.Builder
	repoDir, err := repos.CheckoutDir(e.Name)
	if err != nil {
		fmt.Fprintf(&b, "%s⚠%s  Skipped: %s\n", ansiYellow, ansiReset, err)
		return b.String(), false
	}
	gitDir := filepath.Join(repoDir, ".git")

	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		fmt.Fprintf(&b, "%sℹ%s  Repo '%s' not cloned yet. Cloning...\n", ansiBlue, ansiReset, e.Name)
		if err := gitClone(e.URL, repoDir); err == nil {
			fmt.Fprintf(&b, "%s✓%s  Cloned: %s\n", ansiGreen, ansiReset, e.Name)
			return b.String(), true
		}
		fmt.Fprintf(&b, "%s⚠%s  Failed to clone: %s\n", ansiYellow, ansiReset, e.Name)
		return b.String(), false
	}

	beforeHash := repos.GitOutput(repoDir, "rev-parse", "HEAD")
	fmt.Fprintf(&b, "%sℹ%s  Updating '%s'...\n", ansiBlue, ansiReset, e.Name)

	ctx, cancel := context.WithTimeout(context.Background(), repos.GitFetchTimeout)
	defer cancel()
	pullErr := repos.GitCommand(ctx, "-C", repoDir, "pull", "--ff-only").Run()
	if pullErr != nil {
		fmt.Fprintf(&b, "%s⚠%s  Update failed for '%s'. Try: cd %s && git pull\n",
			ansiYellow, ansiReset, e.Name, repoDir)
		return b.String(), false
	}
	afterHash := repos.GitOutput(repoDir, "rev-parse", "HEAD")
	if beforeHash == afterHash {
		fmt.Fprintf(&b, "  %s%s: already up to date%s\n", ansiDim, e.Name, ansiReset)
		return b.String(), false
	}
	commitCount := cmp.Or(repos.GitOutput(repoDir, "rev-list", beforeHash+".."+afterHash, "--count"), "?")
	fmt.Fprintf(&b, "%s✓%s  Updated: %s%s%s (%s new commit(s))\n",
		ansiGreen, ansiReset, ansiBold, e.Name, ansiReset, commitCount)
	return b.String(), true
}

// gitClone shallow-clones url into dir. Uses GitCloneTimeout (longer than
// fetch) to accommodate larger first-time pulls over slow links. The "--"
// stops a url starting with "-" from being parsed as a git flag.
func gitClone(url, dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), repos.GitCloneTimeout)
	defer cancel()
	return repos.GitCommand(ctx, "clone", "--depth", "1", "--", url, dir).Run()
}

// cmdReset cleans managed symlinks in ~/.agents/skills, removes the
// ~/.claude/skills symlink, rm -rf's the skill store, and removes state
// files. It does NOT touch project folders: with no registry, caddie
// cannot find them, so any symlinks already created there are left
// dangling until `caddie activate` is rerun in each one.
func cmdReset(args []string) {
	force := false
	for _, a := range args {
		switch a {
		case "-f", "--force":
			force = true
		default:
			if strings.HasPrefix(a, "-") {
				die("Unknown flag: " + a)
			}
			die("Unknown argument: " + a)
		}
	}

	fmt.Printf("%scaddie reset%s — restore to clean state\n\n", ansiBold, ansiReset)

	home := config.Home()
	agentSkills := filepath.Join(home, ".agents", "skills")
	claudeSkills := filepath.Join(home, ".claude", "skills")
	skillStore := filepath.Join(config.Dir(), "skills")

	symlinkCountAgents := skills.EachSymlink(agentSkills, nil)
	storeCount := 0
	claudeIsSymlink := false
	if info, err := os.Lstat(claudeSkills); err == nil && info.Mode()&os.ModeSymlink != 0 {
		claudeIsSymlink = true
	}
	if info, err := os.Stat(skillStore); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(skillStore); err == nil {
			storeCount = len(entries)
		}
	}

	fmt.Printf("%sWill remove:%s\n", ansiBold, ansiReset)
	fmt.Printf("  %d managed symlink(s) in ~/.agents/skills/\n", symlinkCountAgents)
	if claudeIsSymlink {
		fmt.Printf("  ~/.claude/skills/ → ~/.agents/skills/ (symlink)\n")
	}
	fmt.Printf("  %d item(s) in skill store (~/.config/caddie/skills/)\n", storeCount)
	fmt.Printf("  State files (.last-scan, .last-pull)\n")
	fmt.Println()
	fmt.Printf("%sWill keep:%s\n", ansiBold, ansiReset)
	fmt.Printf("  Repo registry (~/.config/caddie/sources.yaml)\n")
	fmt.Printf("  Folder-local profiles (.caddie.yaml files in your projects)\n")
	fmt.Println()
	fmt.Printf("%sNote:%s skill symlinks already created in project folders will\n", ansiBold, ansiReset)
	fmt.Printf("  break until you run %scaddie activate%s in each one again.\n", ansiCyan, ansiReset)
	fmt.Println()

	// Prompt unconditionally (don't gate on isTerminal here — echo, not read -rp).
	if !force {
		fmt.Print("Proceed? [y/N] ")
		br := bufio.NewReader(os.Stdin)
		line, _ := br.ReadString('\n')
		confirm := strings.TrimRight(line, "\n")
		if confirm != "y" && confirm != "Y" {
			fmt.Println("Aborted.")
			return
		}
		fmt.Println()
	}

	if info, err := os.Stat(agentSkills); err == nil && info.IsDir() {
		skills.EachSymlink(agentSkills, func(_, full string) { _ = os.Remove(full) })
		fmt.Printf("%sℹ%s  Cleaned ~/.agents/skills/ symlinks\n", ansiBlue, ansiReset)
	}
	// Remove ~/.claude/skills if it's a symlink.
	if li, err := os.Lstat(claudeSkills); err == nil && li.Mode()&os.ModeSymlink != 0 {
		if os.Remove(claudeSkills) == nil {
			fmt.Printf("%sℹ%s  Removed ~/.claude/skills symlink\n", ansiBlue, ansiReset)
		}
	}

	if info, err := os.Stat(skillStore); err == nil && info.IsDir() {
		_ = os.RemoveAll(skillStore)
		fmt.Printf("%sℹ%s  Removed skill store\n", ansiBlue, ansiReset)
	}

	_ = os.Remove(filepath.Join(config.Dir(), ".last-scan"))
	_ = os.Remove(filepath.Join(config.Dir(), ".last-pull"))
	fmt.Printf("%sℹ%s  Removed state files\n", ansiBlue, ansiReset)

	fmt.Println()
	fmt.Printf("%s✓%s  Reset complete. To rebuild, run: %scaddie scan && caddie activate%s\n",
		ansiGreen, ansiReset, ansiCyan, ansiReset)
}

func cmdWhich(_ []string) {
	profilePath := resolveProfileOrDie()
	fmt.Println(profilePath)
}

// cmdInit prompts for name, description and skill patterns, then writes
// ./.caddie.yaml. Prompt text is gated on tty so piped-stdin tests still match.
func cmdInit(args []string) {
	if len(args) > 0 {
		die("Usage: caddie init  (creates .caddie.yaml in the current folder)")
	}
	cwd, err := os.Getwd()
	if err != nil {
		die(err.Error())
	}
	target := filepath.Join(cwd, ".caddie.yaml")
	if _, err := os.Stat(target); err == nil {
		die(fmt.Sprintf("A profile already exists here: %s\n   Edit it with %scaddie edit%s.",
			target, ansiCyan, ansiReset))
	}

	base := filepath.Base(cwd)
	fmt.Printf("%sCreating profile in %s%s%s\n\n", ansiBold, ansiCyan, cwd, ansiReset)

	tty := isTerminal(os.Stdin)
	br := bufio.NewReader(os.Stdin)
	readLine := func(prompt string) string {
		if tty {
			fmt.Print(prompt)
		}
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return ""
		}
		return strings.TrimRight(line, "\n")
	}

	displayName := readLine(fmt.Sprintf("Name [%s]: ", base))
	if displayName == "" {
		displayName = base
	}
	description := readLine("Description: ")

	fmt.Println()
	fmt.Println("Skill patterns (enter one per line, empty line to finish):")
	fmt.Println(`  Examples: "superpowers:*", "gws:gmail-*", "*" (all)`)
	var patterns []string
	for {
		p := readLine("  - ")
		if p == "" {
			break
		}
		patterns = append(patterns, p)
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
		fmt.Printf("%sℹ%s  Defaulting to all skills (\"*\")\n", ansiBlue, ansiReset)
	}

	var body strings.Builder
	fmt.Fprintf(&body, "name: %s\n", config.YAMLQuote(displayName))
	fmt.Fprintf(&body, "description: %s\n", config.YAMLQuote(description))
	body.WriteString("\n")
	body.WriteString("skills:\n")
	for _, p := range patterns {
		fmt.Fprintf(&body, "  - %s\n", config.YAMLQuote(p))
	}

	if err := os.WriteFile(target, []byte(body.String()), 0o644); err != nil {
		die(err.Error())
	}
	if err := ensureProjectGitignore(cwd); err != nil {
		fmt.Printf("%s⚠%s  Could not update .gitignore: %v\n", ansiYellow, ansiReset, err)
	}

	fmt.Println()
	fmt.Printf("%s✓%s  Created %s\n", ansiGreen, ansiReset, target)
	fmt.Printf("  Preview:  %scaddie activate --dry-run%s\n", ansiCyan, ansiReset)
	fmt.Printf("  Activate: %scaddie activate%s\n", ansiCyan, ansiReset)
}

// cmdSetup performs idempotent filesystem setup: config dir, skill store,
// sources file, then runs scan.
func cmdSetup(args []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			die("Unknown flag: " + a)
		}
		die("Unknown argument: " + a)
	}

	fmt.Printf("%sSetting up caddie...%s\n\n", ansiBold, ansiReset)

	configDir := config.Dir()
	skillStore := skills.Store()
	sourcesFile := repos.File()

	_ = os.MkdirAll(configDir, 0o755)
	_ = os.MkdirAll(skillStore, 0o755)
	fmt.Printf("%s✓%s  Config directory: %s\n", ansiGreen, ansiReset, configDir)
	fmt.Printf("%s✓%s  Skill store: %s\n", ansiGreen, ansiReset, skillStore)

	if _, err := os.Stat(sourcesFile); os.IsNotExist(err) {
		if err := os.WriteFile(sourcesFile, []byte("sources:\n"), 0o644); err != nil {
			die(err.Error())
		}
		fmt.Printf("%sℹ%s  Add repos with: %scaddie repo add <name> <url>%s\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	} else {
		fmt.Printf("%sℹ%s  Sources file already exists: %s\n", ansiBlue, ansiReset, sourcesFile)
	}

	fmt.Println()
	cmdScan(nil)

	fmt.Println()
	fmt.Printf("%sℹ%s  Next steps:\n", ansiBlue, ansiReset)
	fmt.Printf("  %scaddie repo list%s             Review registered repos\n", ansiCyan, ansiReset)
	fmt.Printf("  %scaddie inventory%s             See all discovered skills\n", ansiCyan, ansiReset)
	fmt.Printf("  %scaddie init%s                  Create your first profile\n", ansiCyan, ansiReset)
	fmt.Println()
	fmt.Printf("%sℹ%s  Shell integration (add to ~/.zshrc):\n", ansiBlue, ansiReset)
	fmt.Println()
	fmt.Printf("  %sclaude() {\n", ansiDim)
	fmt.Printf("    caddie activate && command claude \"\\$@\"\n")
	fmt.Printf("  }%s\n", ansiReset)
}

// ensureClaudeSkillsSymlink makes claudeDir a relative symlink to
// "../.agents/skills". If claudeDir is a real directory, its contents are
// migrated into agentsDir first.
//
// Nothing is ever deleted. The previous version migrated only directories
// whose name was still free in agentsDir, then os.RemoveAll'd claudeDir —
// so loose files (README.md, NOTES.txt) and any name-colliding directory
// were destroyed without a prompt, a warning or a backup. .claude/skills is
// exactly where Claude Code reads project skills from, so a real directory
// full of hand-written content there is the expected pre-adoption state, and
// the README wires `caddie activate` into the claude() shell function — this
// fired the first time the user launched claude in such a project.
//
// Now every entry is moved, an entry whose name is already taken is left
// alone, and claudeDir is removed with os.Remove, which only succeeds once
// it is empty. Anything left behind blocks the symlink and is reported
// instead of being deleted.
func ensureClaudeSkillsSymlink(claudeDir, agentsDir string) error {
	if li, err := os.Lstat(claudeDir); err == nil && li.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(claudeDir)
		if target == "../.agents/skills" {
			return nil
		}
		// Check whether it resolves to agentsDir.
		resolved, err1 := filepath.EvalSymlinks(claudeDir)
		absAgents, err2 := filepath.EvalSymlinks(agentsDir)
		if err1 == nil && err2 == nil && resolved == absAgents {
			return nil
		}
		_ = os.Remove(claudeDir)
	}
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		_ = os.MkdirAll(agentsDir, 0o755)
		entries, err := os.ReadDir(claudeDir)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", claudeDir, err)
		}
		// Check every name first and move nothing if any is taken. A partial
		// migration is worse than none: the free-named entries would move to
		// .agents/skills while .claude/skills stays a real directory (the
		// symlink is never created), and Claude Code reads exactly that
		// directory — so the skills that moved silently vanish from the tool
		// that was using them.
		var blocked []string
		for _, e := range entries {
			// Lstat, so an existing symlink at dst counts as taken too.
			if _, err := os.Lstat(filepath.Join(agentsDir, e.Name())); err == nil {
				blocked = append(blocked, e.Name())
			}
		}
		if len(blocked) > 0 {
			return fmt.Errorf("left %s as-is: %s already exist under %s — move or remove one side, then re-run",
				claudeDir, strings.Join(blocked, ", "), agentsDir)
		}
		// The name pre-check proves the destinations are free, not that every
		// rename will succeed — EXDEV (if .agents is a symlink onto another
		// volume), EACCES, ENOSPC are all still possible. Undo what moved so
		// a failure mid-way can't leave the half-state this function exists
		// to prevent.
		var moved []string
		for _, e := range entries {
			// Rename moves files, directories and symlinks alike.
			if err := os.Rename(filepath.Join(claudeDir, e.Name()), filepath.Join(agentsDir, e.Name())); err != nil {
				var stuck []string
				for _, name := range moved {
					if os.Rename(filepath.Join(agentsDir, name), filepath.Join(claudeDir, name)) != nil {
						stuck = append(stuck, name)
					}
				}
				if len(stuck) > 0 {
					// Saying "left as-is" would be untrue, and these entries
					// are invisible to Claude Code where they now sit.
					return fmt.Errorf("could not move %s (%w), and %s are now under %s — move them back by hand",
						e.Name(), err, strings.Join(stuck, ", "), agentsDir)
				}
				return fmt.Errorf("left %s as-is: cannot move %s: %w", claudeDir, e.Name(), err)
			}
			moved = append(moved, e.Name())
		}
		// Only succeeds when the migration emptied it.
		if err := os.Remove(claudeDir); err != nil {
			return fmt.Errorf("left %s in place: %w", claudeDir, err)
		}
	}
	_ = os.MkdirAll(filepath.Dir(claudeDir), 0o755)
	_ = os.Symlink("../.agents/skills", claudeDir)
	return nil
}

type scanOpts struct {
	verbose, force, skipFetch bool
	out                       io.Writer // nil means os.Stdout
}

// cmdScan sweeps ~/.agents/skills/ into the store, cleans broken store
// symlinks, then iterates every registered repo (checking for remote updates
// and syncing skills). Cached by mtime of .last-scan (60s) unless --force.
func cmdScan(args []string) {
	var opts scanOpts
	for _, a := range args {
		switch a {
		case "-v", "--verbose":
			opts.verbose = true
		case "-f", "--force":
			opts.force = true
		default:
			if strings.HasPrefix(a, "-") {
				die("Unknown flag: " + a)
			}
			die("Unknown argument: " + a)
		}
	}
	runScan(opts)
}

func runScan(opts scanOpts) {
	out := opts.out
	if out == nil {
		out = os.Stdout
	}
	verbose, force, skipFetch := opts.verbose, opts.force, opts.skipFetch
	store := skills.Store()
	_ = os.MkdirAll(store, 0o755)

	cachePath := filepath.Join(config.Dir(), ".last-scan")
	if !force {
		if info, err := os.Stat(cachePath); err == nil {
			age := time.Since(info.ModTime())
			if age < 60*time.Second {
				if verbose {
					fmt.Fprintf(out, "%sℹ%s  Scan cached (%ds ago, use --force to rescan)\n",
						ansiBlue, ansiReset, int(age.Seconds()))
				}
				return
			}
		}
	}

	fmt.Fprintf(out, "%sℹ%s  Scanning skill sources...\n", ansiBlue, ansiReset)

	var log skills.LogFn
	if verbose {
		log = func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
	}

	brokenRemoved := skills.CleanBrokenStoreLinks(log)
	if brokenRemoved > 0 {
		fmt.Fprintf(out, "%sℹ%s  Removed %d broken symlink(s) from store\n", ansiBlue, ansiReset, brokenRemoved)
	}

	totalRepo, updates, shadowed, perRepo := skills.SyncRepos(log, skipFetch)
	for _, r := range perRepo {
		if r.Warning != "" {
			switch r.Warning {
			case "not cloned":
				fmt.Fprintf(out, "%s⚠%s  Repo '%s': not cloned. Run %scaddie repo update %s%s\n",
					ansiYellow, ansiReset, r.Name, ansiCyan, r.Name, ansiReset)
			case "skills_path not found":
				fmt.Fprintf(out, "%s⚠%s  Repo '%s': skills_path '%s' not found\n",
					ansiYellow, ansiReset, r.Name, r.SkillsPath)
			case "skills_path outside repo":
				fmt.Fprintf(out, "%s⚠%s  Repo '%s': skills_path '%s' resolves outside the checkout — nothing was scanned\n",
					ansiYellow, ansiReset, r.Name, r.SkillsPath)
			case "unusable repo name":
				fmt.Fprintf(out, "%s⚠%s  Repo '%s': name can't be used as a directory. Remove it with %scaddie repo remove '%s'%s\n",
					ansiYellow, ansiReset, r.Name, ansiCyan, r.Name, ansiReset)
			default:
				fmt.Fprintf(out, "%s⚠%s  Repo '%s': %s\n", ansiYellow, ansiReset, r.Name, r.Warning)
			}
			continue
		}
		fmt.Fprintf(out, "%sℹ%s  Repo '%s': %d skills from %s/\n",
			ansiBlue, ansiReset, r.Name, r.Count, r.SkillsPath)
	}

	localCount, total := skills.CountStore()
	fmt.Fprintln(out)
	touch(cachePath)

	fmt.Fprintf(out, "%s✓%s  Scan complete: %s%d%s skills in canonical store (%d local, %d from repos)\n",
		ansiGreen, ansiReset, ansiBold, total, ansiReset, localCount, totalRepo)

	if len(updates) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%sℹ%s  Repo updates available:\n", ansiBlue, ansiReset)
		for _, u := range updates {
			fmt.Fprintf(out, "  %s•%s %s%s%s: %s commit(s) behind\n",
				ansiYellow, ansiReset, ansiBold, u.Name, ansiReset, u.Behind)
		}
		fmt.Fprintf(out, "  %sRun %scaddie repo update%s%s to pull changes%s\n",
			ansiDim, ansiCyan, ansiReset, ansiDim, ansiReset)
	}

	if len(shadowed) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%s⚠%s  Local skills shadowing repo versions:\n", ansiYellow, ansiReset)
		for _, s := range shadowed {
			fmt.Fprintf(out, "  %s•%s %s%s%s shadows repo '%s'\n",
				ansiYellow, ansiReset, ansiBold, s.SkillName, ansiReset, s.RepoName)
		}
		fmt.Fprintf(out, "  %sTo use the repo version, remove the local copy:%s\n", ansiDim, ansiReset)
		fmt.Fprintf(out, "  %s  rm -rf %s/<skill-name> && caddie scan --force%s\n", ansiDim, store, ansiReset)
	}
}

// cmdActivate resolves the caddie profile by walking up from cwd to the
// filesystem root looking for the nearest .caddie.yaml, syncs repos,
// resolves skills, fingerprints them, and reconciles the profile folder's
// .agents/skills directory (with .claude/skills symlinked to it). Always
// writes a fingerprint file and gitignore entries for the profile folder.
func cmdActivate(args []string) {
	dryRun := false
	force := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "--force", "-f":
			force = true
		default:
			die(fmt.Sprintf("Unknown argument: %s\ncaddie activate no longer takes a profile name; it resolves the nearest .caddie.yaml from the current directory.", a))
		}
	}

	profilePath := resolveProfileOrDie()

	profileDir := filepath.Dir(profilePath)
	displayName := config.ReadScalar(profilePath, "name")
	label := cmp.Or(displayName, filepath.Base(profileDir))

	targetAgents := filepath.Join(profileDir, ".agents", "skills")
	fingerprintFile := filepath.Join(profileDir, ".claude", ".caddie-fingerprint")

	fmt.Printf("%s%s%s %s→ %s%s\n", ansiBold, label, ansiReset, ansiDim, profileDir, ansiReset)

	activateSyncRepos(force)

	patterns := config.ReadList(profilePath, "skills")

	var orphans []string
	if items, err := skills.Scan(); err == nil {
		for _, p := range patterns {
			if p != "" && skills.PatternMatchesIn(items, p) == 0 {
				orphans = append(orphans, p)
			}
		}
	}
	warnOrphans := func() {
		for _, p := range orphans {
			fmt.Printf("%s⚠%s  Pattern %s\"%s\"%s matches 0 skills, the source may have been removed\n",
				ansiYellow, ansiReset, ansiCyan, p, ansiReset)
		}
	}

	// Resolve matched skills + per-prefix counts.
	matched, prefixSummary := resolveMatchedWithSummary(patterns)
	skillCount := len(matched)

	if dryRun {
		// Dry run is an explicit request to be told what is going on, so it
		// always discloses stale patterns (unlike the reconciling path below,
		// which only warns when something actually changed).
		warnOrphans()
		fmt.Println()
		fmt.Printf("%sDry run — would activate:%s\n\n", ansiYellow, ansiReset)
		fmt.Printf("  Profile: %s%s%s\n", ansiBold, label, ansiReset)
		fmt.Printf("  Folder:  %s\n", profileDir)
		fmt.Printf("  Skills:  %d (%s)\n", skillCount, prefixSummary)
		fmt.Println()
		fmt.Printf("  %sWould manage:%s\n", ansiDim, ansiReset)
		fmt.Printf("    %s%s/ (.claude/skills → symlink)%s\n", ansiDim, targetAgents, ansiReset)
		fmt.Println()
		fmt.Printf("  Matched skills:\n")
		for _, s := range matched {
			fmt.Printf("    %s\n", s)
		}
		return
	}

	newFP := skills.ComputeFingerprint(matched)
	store := skills.Store()
	if data, err := os.ReadFile(fingerprintFile); err == nil {
		old := strings.TrimRight(string(data), "\n")
		// Trust the fingerprint only if the on-disk symlinks still point into
		// the current store. This catches stale links left over from a config
		// dir rename — without this, ReconcileSkillDir would never run.
		if old == newFP && symlinksHealthy(targetAgents, store, matched) {
			// Re-check the .claude/skills link even on the fast path. A
			// migration blocked by a name collision leaves that directory
			// real and the symlink uncreated, which the fingerprint doesn't
			// capture — without this the warning printed once and the
			// project then stayed quietly broken across every later run.
			if err := ensureClaudeSkillsSymlink(filepath.Join(profileDir, ".claude", "skills"), targetAgents); err != nil {
				fmt.Printf("%s⚠%s  %v\n", ansiYellow, ansiReset, err)
			}
			fmt.Printf("%s✓%s %d skills (unchanged)\n", ansiGreen, ansiReset, skillCount)
			return
		}
	}

	// Past the fast path: something changed, so this run reconciles. Warn
	// about stale patterns now rather than on every activate — otherwise a
	// long-lived stale pattern would print on every single invocation.
	warnOrphans()

	res, err := skills.ReconcileSkillDir(targetAgents, matched, store)
	if err != nil {
		die(err.Error())
	}
	totalAdded, totalRemoved := res.Added, res.Removed

	if err := ensureClaudeSkillsSymlink(filepath.Join(profileDir, ".claude", "skills"), targetAgents); err != nil {
		fmt.Printf("%s⚠%s  %v\n", ansiYellow, ansiReset, err)
	}
	if err := ensureProjectGitignore(profileDir); err != nil {
		fmt.Printf("%s⚠%s  Could not update .gitignore: %v\n", ansiYellow, ansiReset, err)
	}

	_ = os.MkdirAll(filepath.Dir(fingerprintFile), 0o755)
	if err := os.WriteFile(fingerprintFile, []byte(newFP+"\n"), 0o644); err != nil {
		fmt.Printf("%s⚠%s  Could not write fingerprint (next activate will reconcile again): %v\n",
			ansiYellow, ansiReset, err)
	}

	changeDesc := ""
	switch {
	case totalAdded > 0 && totalRemoved > 0:
		changeDesc = fmt.Sprintf(" (+%d added, -%d removed)", totalAdded, totalRemoved)
	case totalAdded > 0:
		changeDesc = fmt.Sprintf(" (+%d added)", totalAdded)
	case totalRemoved > 0:
		changeDesc = fmt.Sprintf(" (-%d removed)", totalRemoved)
	}
	fmt.Printf("%s✓%s %d skills%s\n", ansiGreen, ansiReset, skillCount, changeDesc)
}

// resolveMatchedWithSummary returns sorted matched skill dirnames and a
// "prefix: count, prefix: count" summary used by activate's status line.
func resolveMatchedWithSummary(patterns []string) ([]string, string) {
	if len(patterns) == 0 {
		return nil, ""
	}
	items, err := skills.Scan()
	if err != nil {
		return nil, ""
	}
	dirToPrefix := map[string]string{}
	for _, it := range items {
		id := it.SkillID()
		for _, p := range patterns {
			if ok, _ := path.Match(p, id); ok {
				dirToPrefix[it.DirName] = it.Prefix
				break
			}
		}
	}
	dirs := make([]string, 0, len(dirToPrefix))
	counts := map[string]int{}
	for d, p := range dirToPrefix {
		dirs = append(dirs, d)
		counts[p]++
	}
	sort.Strings(dirs)

	prefixes := make([]string, 0, len(counts))
	for p := range counts {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		parts = append(parts, fmt.Sprintf("%s: %d", p, counts[p]))
	}
	return dirs, strings.Join(parts, ", ")
}

// activateSyncRepos pulls registered git repos at most once per hour (or
// every call when force is true), then runs scan to refresh the skill store.
// Repo-update output stays visible; scan output is suppressed to keep
// activate's summary clean. The .last-pull mtime is only refreshed when at
// least one repo successfully updated, so a transient network failure does
// not suppress retries for an hour.
func activateSyncRepos(force bool) {
	pullPath := filepath.Join(config.Dir(), ".last-pull")
	shouldPull := force
	if !shouldPull {
		info, err := os.Stat(pullPath)
		if err != nil || time.Since(info.ModTime()) >= time.Hour {
			shouldPull = true
		}
	}
	pulled := false
	if shouldPull {
		if has, err := repos.HasReposSection(); err == nil && has {
			updated, attempted := runRepoUpdate(nil)
			// Only mark fresh when at least one pull made progress, or we had
			// nothing to do (no repos found in the file).
			if attempted == 0 || updated > 0 {
				touch(pullPath)
			}
			pulled = attempted > 0
		} else {
			touch(pullPath)
		}
	}

	// Run scan with output discarded — keep activate's summary clean.
	runScan(scanOpts{force: force, skipFetch: pulled, out: io.Discard})
}

// symlinksHealthy checks two things: (1) every expected skill is a symlink
// under targetDir pointing at the matching entry inside store — used to
// invalidate a stale fingerprint after the config dir is renamed (or the
// store path otherwise changes), since without this ReconcileSkillDir is
// short-circuited and stale links live forever; and (2) targetDir holds no
// more store-pointing symlinks than expected — this catches stale extras
// left behind by a shrinking profile (e.g. a pattern that used to match more
// skills) that would otherwise never get cleaned, since a shrinking profile
// with an otherwise-correct set of links and an unchanged fingerprint input
// would pass check (1) forever.
//
// Real files and directories (hand-placed skills, macOS .DS_Store/Icon
// artifacts) are ignored entirely — never counted, never a reason to fail.
// Symlinks that point somewhere other than store are also ignored by the
// count: they are not caddie-managed, so their presence (or absence) says
// nothing about whether this directory needs reconciling.
func symlinksHealthy(targetDir, store string, expected []string) bool {
	wantCount := 0
	for _, name := range expected {
		if name == "" {
			continue
		}
		wantCount++
		want := filepath.Join(store, name)
		got, err := os.Readlink(filepath.Join(targetDir, name))
		if err != nil || got != want {
			return false
		}
	}

	storeLinks := 0
	skills.EachSymlinkTarget(targetDir, func(name, full, target string) {
		if filepath.Dir(target) == store {
			storeLinks++
		}
	})
	return storeLinks <= wantCount
}

// touch updates p's mtime to now, creating the file if it doesn't exist.
func touch(p string) {
	now := time.Now()
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
	}
	_ = os.Chtimes(p, now, now)
}

// findRepoRoot walks from dir up to "/" looking for a .git entry, and returns
// the directory containing it, or "" when dir is not inside a git repository.
func findRepoRoot(dir string) string {
	for dir != "/" && dir != "" {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// ensureProjectGitignore appends managed entries to the repo root's
// .gitignore (idempotently), walking up from projectDir to find that root.
// It is a no-op (nil error) when projectDir is not inside a git repository.
func ensureProjectGitignore(projectDir string) error {
	root := findRepoRoot(projectDir)
	if root == "" {
		return nil
	}
	gitignore := filepath.Join(root, ".gitignore")
	// Anchor each entry to this profile's own directory. A bare
	// ".claude/skills/" is an unanchored gitignore pattern: written at the
	// repo root it matches that name at *every* depth, so activating in
	// apps/web also ignored apps/api/.caddie.yaml and every other profile in
	// the monorepo — including ones a teammate committed on purpose. At its
	// worst, when $HOME is a dotfiles repo, activating in any folder below it
	// stopped the user's real ~/.claude/skills from being tracked.
	prefix := "/"
	if rel, err := filepath.Rel(root, projectDir); err == nil && rel != "." &&
		!strings.HasPrefix(rel, "..") {
		prefix = "/" + escapeGitignorePath(filepath.ToSlash(rel)) + "/"
	}
	entries := []string{
		prefix + ".claude/skills/",
		prefix + ".agents/skills/",
		prefix + ".claude/.caddie-fingerprint",
		prefix + ".caddie.yaml",
	}

	var body []byte
	if b, err := os.ReadFile(gitignore); err == nil {
		body = b
	}

	// Rebuild caddie's own block rather than appending a second one: the
	// "already present" check compared exact line text, so anchoring would
	// otherwise leave the old unanchored lines in place — still winning, and
	// under a duplicate header — and never reach anyone who had already run
	// caddie.
	//
	// Only lines that are caddie's own patterns are touched. Everything else
	// keeps its position, because .gitignore is order-sensitive: a "!" negation
	// only works after the rule it negates, so absorbing a user's lines into a
	// sorted block would silently change what git ignores.
	const header = "# caddie managed skill directories"
	legacy := map[string]bool{
		".claude/skills/":             true,
		".agents/skills/":             true,
		".claude/.caddie-fingerprint": true,
		".caddie.yaml":                true,
	}
	// CRLF: compare on the trimmed line, and write CRLF back when the file is
	// consistently CRLF, so a Windows checkout doesn't get its endings
	// rewritten. A file with mixed endings is normalised to LF — it is
	// already inconsistent, and picking the majority would be guesswork.
	nl := "\n"
	if crlf := strings.Count(string(body), "\r\n"); crlf > 0 && crlf == strings.Count(string(body), "\n") {
		nl = "\r\n"
	}
	// An anchored entry is "/" or "/<dir>/" followed by one of the four
	// suffixes. Requiring the suffix to start at a separator matters: a
	// plain HasSuffix also claims "/data/backup.caddie.yaml", an ordinary
	// rule for a file that happens to end in those characters, and moving a
	// user's line is what this is here to avoid.
	isAnchored := func(line string) bool {
		if !strings.HasPrefix(line, "/") {
			return false
		}
		for suffix := range legacy {
			if rest := strings.TrimSuffix(line, suffix); rest != line && strings.HasSuffix(rest, "/") {
				return true
			}
		}
		return false
	}

	// Lines are emitted in three groups — everything before caddie's header,
	// then caddie's block, then everything after it. Keeping the "after"
	// group after matters: a negation of one of caddie's own rules
	// ("!/.caddie.yaml", to commit a shared profile) can only work below the
	// rule it negates, and hoisting it silently stopped it working — stickily,
	// since re-adding it would be hoisted again.
	var kept, before, after []string
	seenHeader := false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case line == header:
			seenHeader = true // dropped; re-emitted below
		case isAnchored(line):
			kept = append(kept, line)
		case seenHeader && legacy[line]:
			// An unanchored line caddie itself wrote, dropped so the anchored
			// replacement is the only rule left. Scoped to lines after the
			// header: the same four strings are perfectly reasonable rules for
			// a team to write on purpose, and deleting one of those — every
			// activate, so re-adding it never sticks — is not caddie's call.
		case seenHeader:
			after = append(after, line)
		default:
			before = append(before, line)
		}
	}
	for _, e := range entries {
		if !slices.Contains(kept, e) {
			kept = append(kept, e)
		}
	}
	slices.Sort(kept)

	var out strings.Builder
	if trimmed := strings.TrimRight(strings.Join(before, nl), "\r\n"); trimmed != "" {
		out.WriteString(trimmed + nl + nl)
	}
	out.WriteString(header + nl)
	for _, e := range kept {
		out.WriteString(e + nl)
	}
	if trimmed := strings.TrimRight(strings.Join(after, nl), "\r\n"); trimmed != "" {
		out.WriteString(nl + trimmed + nl)
	}
	if out.String() == string(body) {
		return nil
	}
	return os.WriteFile(gitignore, []byte(out.String()), 0o644)
}

// escapeGitignorePath escapes the glob metacharacters gitignore honours, so a
// directory literally named "[slug]" (an ordinary dynamic route in Next.js,
// SvelteKit or Remix) produces a pattern that matches it rather than a
// character class that matches nothing.
func escapeGitignorePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`\[]*?`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cmdExport resolves the cwd profile's skills (or all skills with --all),
// then dispatches to internal/export for either a local directory or an
// s3:// URL.
func cmdExport(args []string) {
	var (
		target string
		dryRun bool
		clean  bool
		all    bool
		force  bool
	)

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--to":
			// Taking args[i+1] unconditionally swallowed the next flag:
			// `--clean --to --dry-run` exported to a directory literally
			// named "--dry-run" with dry-run off and --clean live, which
			// with the deletion below is a real rm of whatever was there.
			if i+1 >= len(args) {
				die("--to needs a directory or s3:// URL")
			}
			if strings.HasPrefix(args[i+1], "-") {
				die(fmt.Sprintf("--to needs a directory or s3:// URL, got the flag %s", args[i+1]))
			}
			target = args[i+1]
			i++
		case "--dry-run", "-n":
			dryRun = true
		case "--clean":
			clean = true
		case "--all":
			all = true
		case "--force", "-f":
			force = true
		default:
			if strings.HasPrefix(a, "-") {
				die("Unknown flag: " + a)
			}
			die(fmt.Sprintf("Unknown argument: %s\ncaddie export no longer takes a profile name; it resolves the nearest .caddie.yaml from the current directory.", a))
		}
	}

	if target == "" {
		die("Usage: caddie export [--all] --to <dir-or-s3> [--clean] [--dry-run] [--force]")
	}

	var patterns []string
	if all {
		patterns = []string{"*:*"}
	} else {
		patterns = config.ReadList(resolveProfileOrDie(
			fmt.Sprintf("Or use %s--all%s to export every skill in the store.", ansiCyan, ansiReset)), "skills")
	}

	matched, err := skills.Resolve(patterns)
	if err != nil {
		die(err.Error())
	}

	// Resolve symlinks so the export copies the real source directory.
	store := skills.Store()
	var entries []export.Entry
	for _, dir := range matched {
		real, err := filepath.EvalSymlinks(filepath.Join(store, dir))
		if err != nil {
			continue
		}
		entries = append(entries, export.Entry{Name: dir, RealDir: real})
	}

	if len(entries) == 0 {
		fmt.Printf("%s⚠%s  No skills matched.\n", ansiYellow, ansiReset)
		return
	}

	opts := export.Options{Clean: clean, DryRun: dryRun, ConfirmRemove: func(names []string) bool {
		if force {
			return true
		}
		fmt.Printf("%s⚠%s  %s--clean%s will recursively delete these directories under %s%s%s, which caddie did not export:\n",
			ansiYellow, ansiReset, ansiBold, ansiReset, ansiBold, target, ansiReset)
		for _, n := range names {
			fmt.Printf("    %s%s/%s\n", ansiRed, n, ansiReset)
		}
		fmt.Print("Proceed? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer != "y" && answer != "Y" {
			return false
		}
		return true
	}}
	if strings.HasPrefix(target, "s3://") {
		if err := export.S3(target, entries, opts); err != nil {
			die(err.Error())
		}
		return
	}
	if err := export.Local(target, entries, opts); err != nil {
		die(err.Error())
	}
}
