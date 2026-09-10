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
		repoDir := filepath.Join(repos.Dir(), e.Name)
		status := fmt.Sprintf("%s(not cloned)%s", ansiDim, ansiReset)
		if info, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && info.IsDir() {
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
	repoDir := filepath.Join(repos.Dir(), name)
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
	repoDir := filepath.Join(repos.Dir(), name)
	skills.EachSymlinkTarget(skills.Store(), func(_, full, target string) {
		if repos.LinkPointsTo(target, name) {
			if os.Remove(full) == nil {
				removed++
			}
		}
	})

	if info, err := os.Stat(repoDir); err == nil && info.IsDir() {
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
			"  " + C + "init" + R + "                        Initialize: migrate skills, first scan\n" +
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
			"  " + C + "scan" + R + "    [-v]                 Scan repos, sync to skill store\n" +
			"  " + C + "inventory" + R + "                    List all skills with prefix grouping\n" +
			"\n" +
			B + "ENVIRONMENTS" + R + "\n" +
			"  " + C + "create" + R + "  <name>               Create a new environment interactively\n" +
			"  " + C + "list" + R + "    (ls)                 List all environments\n" +
			"  " + C + "show" + R + "    <name>               Show config + resolved skills\n" +
			"  " + C + "edit" + R + "    <name>               Open environment config in $EDITOR\n" +
			"  " + C + "activate" + R + " [name] [-f]         Resolve skills for cwd (-f: force-pull repos)\n" +
			"  " + C + "clone" + R + "   <src> <dest>         Clone an environment config\n" +
			"  " + C + "delete" + R + "  <name>               Delete an environment\n" +
			"  " + C + "which" + R + "                        Show currently active environment\n" +
			"  " + C + "reset" + R + "   [-f]                 Remove symlinks, store & re-enable plugins\n" +
			"\n" +
			B + "EXPORT" + R + "\n" +
			"  " + C + "export" + R + "  <env> --to <dir>       Copy resolved skills to local directory\n" +
			"  " + C + "export" + R + "  <env> --to s3://b/p/   Upload resolved skills to S3\n" +
			"  " + C + "export" + R + "  --all --to <target>    Export all skills (no env filter)\n" +
			"  Flags: --clean (remove stale), --dry-run (preview)\n" +
			"  Env:   AWS_PROFILE, AWS_ENDPOINT_URL (for S3 targets)\n" +
			"\n" +
			B + "FLAGS" + R + "\n" +
			"  -n, --dry-run             Show what would happen without executing\n" +
			"  -v, --verbose             Show detailed output\n" +
			"  -h, --help                Show this help message\n" +
			"\n" +
			B + "SKILL PATTERNS" + R + "\n" +
			"  Patterns use prefix:name format with glob wildcards:\n" +
			"    \"lenny:*\"          all skills from the lenny repo\n" +
			"    \"lenny:ai-*\"       repo skills matching ai-* (e.g. ai-evals)\n" +
			"    \"local:*\"          all hand-written skills (no prefix dash)\n" +
			"    \"*\"                everything\n" +
			"\n" +
			B + "EXAMPLES" + R + "\n" +
			"  caddie init                              # First-time setup\n" +
			"  caddie create my-project                 # Create environment\n" +
			"  caddie activate                          # Auto-detect profile from cwd\n" +
			"  caddie activate my-project               # Explicit profile activation\n" +
			"  caddie activate --dry-run                # Preview what would change\n" +
			"  caddie activate --force                  # Pull all repos now (bypass hourly cache)\n" +
			"  caddie repo add lenny https://github.com/RefoundAI/lenny-skills  # Add git repo\n" +
			"  caddie inventory                         # See all available skills\n" +
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
			"    environment: \"my-project\"\n" +
			"\n" +
			B + "DIRECTORIES" + R + "\n" +
			"  " + D + "~/.config/caddie/skills/" + R + "            Skill store (source of truth)\n" +
			"  " + D + "~/.config/caddie/repos/" + R + "             Cloned git repos\n" +
			"  " + D + "~/.config/caddie/config.yaml" + R + "        Global settings (default_environment)\n" +
			"  " + D + "<project>/.agents/skills/" + R + "            Project skills (managed symlinks)\n" +
			"  " + D + "<project>/.claude/skills" + R + "             Symlink to .agents/skills\n" +
			"  " + D + "<project>/.caddie.yaml" + R + "              Project profile binding\n" +
			"  " + D + "~/.config/caddie/environments/" + R + "       Environment YAML files\n" +
			"  " + D + "~/.config/caddie/sources.yaml" + R + "        Repo registry\n",
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
	repoDir := filepath.Join(repos.Dir(), e.Name)
	gitDir := filepath.Join(repoDir, ".git")
	var b strings.Builder

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
	pullErr := exec.CommandContext(ctx, "git", "-C", repoDir, "pull", "--ff-only").Run()
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
// fetch) to accommodate larger first-time pulls over slow links.
func gitClone(url, dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), repos.GitCloneTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dir).Run()
}

// cmdReset cleans managed symlinks in ~/.agents/skills, removes the
// ~/.claude/skills symlink, rm -rf's the skill store, and removes state
// files. It does NOT touch project folders: with no registry, caddie
// cannot find them, so any symlinks already created there are left
// dangling until `caddie activate` is rerun in each one.
func cmdReset(args []string) {
	force := false
	if len(args) > 0 && (args[0] == "-f" || args[0] == "--force") {
		force = true
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
	fmt.Fprintf(&body, "name: \"%s\"\n", displayName)
	fmt.Fprintf(&body, "description: \"%s\"\n", description)
	body.WriteString("\n")
	body.WriteString("skills:\n")
	for _, p := range patterns {
		fmt.Fprintf(&body, "  - \"%s\"\n", p)
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
	fmt.Printf("%sSetting up caddie...%s\n\n", ansiBold, ansiReset)

	home := config.Home()
	configDir := config.Dir()
	skillStore := skills.Store()
	sourcesFile := repos.File()

	_ = os.MkdirAll(configDir, 0o755)
	_ = os.MkdirAll(skillStore, 0o755)
	fmt.Printf("%s✓%s  Config directory: %s\n", ansiGreen, ansiReset, configDir)
	fmt.Printf("%s✓%s  Skill store: %s\n", ansiGreen, ansiReset, skillStore)

	// Global skill dirs are no longer managed. Remove caddie's own symlink,
	// but never touch a real directory the user owns.
	claudeSkills := filepath.Join(home, ".claude", "skills")
	if li, err := os.Lstat(claudeSkills); err == nil && li.Mode()&os.ModeSymlink != 0 {
		if target, _ := os.Readlink(claudeSkills); target == "../.agents/skills" {
			if os.Remove(claudeSkills) == nil {
				fmt.Printf("%sℹ%s  Removed the managed ~/.claude/skills symlink\n", ansiBlue, ansiReset)
			}
		}
	}

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
// migrated into agentsDir before recreating the symlink.
func ensureClaudeSkillsSymlink(claudeDir, agentsDir string) {
	if li, err := os.Lstat(claudeDir); err == nil && li.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(claudeDir)
		if target == "../.agents/skills" {
			return
		}
		// Check whether it resolves to agentsDir.
		resolved, err1 := filepath.EvalSymlinks(claudeDir)
		absAgents, err2 := filepath.EvalSymlinks(agentsDir)
		if err1 == nil && err2 == nil && resolved == absAgents {
			return
		}
		_ = os.Remove(claudeDir)
	}
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		_ = os.MkdirAll(agentsDir, 0o755)
		if entries, err := os.ReadDir(claudeDir); err == nil {
			for _, e := range entries {
				src := filepath.Join(claudeDir, e.Name())
				dst := filepath.Join(agentsDir, e.Name())
				li, err := os.Lstat(src)
				if err != nil {
					continue
				}
				if li.Mode()&os.ModeSymlink == 0 && li.IsDir() {
					if _, err := os.Stat(dst); os.IsNotExist(err) {
						_ = os.Rename(src, dst)
					}
				} else if li.Mode()&os.ModeSymlink != 0 {
					if _, err := os.Lstat(dst); os.IsNotExist(err) {
						target, _ := os.Readlink(src)
						_ = os.Symlink(target, dst)
					}
				}
			}
		}
		_ = os.RemoveAll(claudeDir)
	}
	_ = os.MkdirAll(filepath.Dir(claudeDir), 0o755)
	_ = os.Symlink("../.agents/skills", claudeDir)
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

	newFP := skills.ComputeFingerprint(profileDir, matched)
	store := skills.Store()
	if data, err := os.ReadFile(fingerprintFile); err == nil {
		old := strings.TrimRight(string(data), "\n")
		// Trust the fingerprint only if the on-disk symlinks still point into
		// the current store. This catches stale links left over from a config
		// dir rename — without this, ReconcileSkillDir would never run.
		if old == newFP && symlinksHealthy(targetAgents, store, matched) {
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

	ensureClaudeSkillsSymlink(filepath.Join(profileDir, ".claude", "skills"), targetAgents)
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

// symlinksHealthy verifies every expected symlink under targetDir points at
// the matching entry inside store. Used to invalidate a stale fingerprint
// after the config dir is renamed (or the store path otherwise changes) —
// without this, ReconcileSkillDir is short-circuited and stale links live
// forever. Readlink is sub-microsecond so checking all of them is fine.
func symlinksHealthy(targetDir, store string, expected []string) bool {
	for _, name := range expected {
		if name == "" {
			continue
		}
		want := filepath.Join(store, name)
		got, err := os.Readlink(filepath.Join(targetDir, name))
		if err != nil || got != want {
			return false
		}
	}
	return true
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
	entries := []string{
		".claude/skills/",
		".agents/skills/",
		".agents/SOURCES.md",
		".claude/.caddie-fingerprint",
		".caddie.yaml",
	}

	var body []byte
	if b, err := os.ReadFile(gitignore); err == nil {
		body = b
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		existing[line] = true
	}

	needsUpdate := false
	for _, e := range entries {
		if !existing[e] {
			needsUpdate = true
			break
		}
	}
	if !needsUpdate {
		return nil
	}

	var out strings.Builder
	out.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	out.WriteString("# caddie managed skill directories\n")
	for _, e := range entries {
		if !existing[e] {
			out.WriteString(e)
			out.WriteByte('\n')
			existing[e] = true
		}
	}
	return os.WriteFile(gitignore, []byte(out.String()), 0o644)
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
	)

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--to":
			if i+1 >= len(args) {
				die("Unknown flag: --to")
			}
			target = args[i+1]
			i++
		case "--dry-run", "-n":
			dryRun = true
		case "--clean":
			clean = true
		case "--all":
			all = true
		default:
			if strings.HasPrefix(a, "-") {
				die("Unknown flag: " + a)
			}
			die(fmt.Sprintf("Unknown argument: %s\ncaddie export no longer takes a profile name; it resolves the nearest .caddie.yaml from the current directory.", a))
		}
	}

	if target == "" {
		die("Usage: caddie export [--all] --to <dir-or-s3> [--clean] [--dry-run]")
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

	opts := export.Options{Clean: clean, DryRun: dryRun}
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
