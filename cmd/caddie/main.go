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
	"list":      cmdList,
	"ls":        cmdList,
	"source":    cmdSource,
	"edit":      cmdEdit,
	"delete":    cmdDelete,
	"rm":        cmdDelete,
	"clone":     cmdClone,
	"cp":        cmdClone,
	"repo":      cmdRepo,
	"inventory": cmdInventory,
	"help":      cmdHelp,
	"--help":    cmdHelp,
	"-h":        cmdHelp,
	"reset":     cmdReset,
	"show":      cmdShow,
	"info":      cmdShow,
	"create":    cmdCreate,
	"new":       cmdCreate,
	"init":      cmdInit,
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

func cmdList(_ []string) {
	envs := config.ListEnvs()
	if len(envs) == 0 {
		fmt.Printf("%s⚠%s  No environments found.\n", ansiYellow, ansiReset)
		fmt.Printf("%sℹ%s  Run %scaddie create <name>%s to get started.\n", ansiBlue, ansiReset, ansiCyan, ansiReset)
		return
	}

	fmt.Printf("%sEnvironments:%s\n\n", ansiBold, ansiReset)
	for _, name := range envs {
		file := config.EnvFile(name)
		displayName := config.ReadScalar(file, "name")
		description := config.ReadScalar(file, "description")
		skillCount := len(config.ReadList(file, "skills"))

		fmt.Printf("  %s%s%s%s\n", ansiBold, ansiCyan, name, ansiReset)
		if displayName != "" && displayName != name {
			fmt.Printf("    %s\n", displayName)
		}
		if description != "" {
			fmt.Printf("    %s%s%s\n", ansiDim, description, ansiReset)
		}
		if skillCount > 0 {
			fmt.Printf("    %s%d skill pattern(s)%s\n", ansiDim, skillCount, ansiReset)
		}
		fmt.Println()
	}

	fmt.Printf("  %sAuto-detect: %scaddie activate%s%s (resolves from cwd)%s\n\n",
		ansiDim, ansiCyan, ansiReset, ansiDim, ansiReset)
}

// cmdSource prints a migration message for the removed `source` family.
// Plugin sources were dropped in favor of git repos.
func cmdSource(_ []string) {
	die("`caddie source` has been removed. Use `caddie repo` to manage skill sources.")
}

func cmdEdit(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: caddie edit <name>")
	}
	name := args[0]
	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}
	file := config.EnvFile(name)

	editor := cmp.Or(os.Getenv("EDITOR"), "vim")
	// `sh -c` preserves $EDITOR's word-splitting (e.g. "code --wait").
	cmd := exec.Command("sh", "-c", editor+` "$0"`, file)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	fmt.Printf("%s✓%s  Updated: %s\n", ansiGreen, ansiReset, name)
}

func cmdDelete(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: caddie delete <name>")
	}
	name := args[0]
	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}

	if isTerminal(os.Stdin) {
		fmt.Printf("Are you sure you want to delete '%s'? [y/N] ", name)
	}
	one := make([]byte, 1)
	n, _ := io.ReadFull(bufio.NewReader(os.Stdin), one)
	fmt.Println()
	confirm := ""
	if n == 1 {
		confirm = string(one)
	}
	if confirm != "y" && confirm != "Y" {
		return
	}

	if err := os.Remove(config.EnvFile(name)); err != nil {
		die(err.Error())
	}
	fmt.Printf("%s✓%s  Deleted: %s\n", ansiGreen, ansiReset, name)
}

func cmdClone(args []string) {
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		die("Usage: caddie clone <source> <destination>")
	}
	src, dest := args[0], args[1]
	if !config.EnvExists(src) {
		die(fmt.Sprintf("Source environment '%s' not found.", src))
	}
	if config.EnvExists(dest) {
		die(fmt.Sprintf("Destination environment '%s' already exists.", dest))
	}

	data, err := os.ReadFile(config.EnvFile(src))
	if err != nil {
		die(err.Error())
	}
	if err := os.WriteFile(config.EnvFile(dest), data, 0o644); err != nil {
		die(err.Error())
	}
	fmt.Printf("%s✓%s  Cloned: %s -> %s\n", ansiGreen, ansiReset, src, dest)
	fmt.Printf("%sℹ%s  Edit with: %scaddie edit %s%s\n", ansiBlue, ansiReset, ansiCyan, dest, ansiReset)
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
		die(fmt.Sprintf("Canonical store not found. Run %scaddie init%s first.", ansiCyan, ansiReset))
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
// ~/.claude/skills symlink, cleans project-local skill symlinks for every
// registered environment, rm -rf's the skill store, and removes state files.
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
	fmt.Printf("  Project-local skill symlinks (for all environments with directory: set)\n")
	fmt.Println()
	fmt.Printf("%sWill keep:%s\n", ansiBold, ansiReset)
	fmt.Printf("  Environment configs (~/.config/caddie/environments/)\n")
	fmt.Printf("  Repo registry (~/.config/caddie/sources.yaml)\n")
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

	// Clean project-local skill dirs for every environment with `directory:`.
	projectCleaned := 0
	envDir := config.EnvDir()
	if entries, err := os.ReadDir(envDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			envYAML := filepath.Join(envDir, e.Name())
			ed := config.ReadScalar(envYAML, "directory")
			if ed == "" {
				continue
			}
			ed = config.ExpandTilde(ed)
			pdir := filepath.Join(ed, ".agents", "skills")
			skills.EachSymlink(pdir, func(_, full string) {
				if os.Remove(full) == nil {
					projectCleaned++
				}
			})
			// Remove .claude/skills symlink + fingerprint.
			claudeLink := filepath.Join(ed, ".claude", "skills")
			if li, err := os.Lstat(claudeLink); err == nil && li.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(claudeLink)
			}
			_ = os.Remove(filepath.Join(ed, ".claude", ".caddie-fingerprint"))
		}
	}
	if projectCleaned > 0 {
		fmt.Printf("%sℹ%s  Cleaned %d project-local symlink(s)\n", ansiBlue, ansiReset, projectCleaned)
	}

	if info, err := os.Stat(skillStore); err == nil && info.IsDir() {
		_ = os.RemoveAll(skillStore)
		fmt.Printf("%sℹ%s  Removed skill store\n", ansiBlue, ansiReset)
	}

	_ = os.Remove(filepath.Join(config.Dir(), ".last-scan"))
	_ = os.Remove(filepath.Join(config.Dir(), ".last-pull"))
	fmt.Printf("%sℹ%s  Removed state files\n", ansiBlue, ansiReset)

	fmt.Println()
	fmt.Printf("%s✓%s  Reset complete. To rebuild, run: %scaddie scan && caddie activate <env>%s\n",
		ansiGreen, ansiReset, ansiCyan, ansiReset)
}

func cmdWhich(_ []string) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Bash `cmd_which` captures resolve_env_from_cwd via $(), which swallows
	// the "unknown environment" warning from strategy 1. Match that here by
	// ignoring warnings. `activate` will surface them when it lands.
	name, _, ok := config.ResolveFromCwd(cwd)
	if !ok {
		fmt.Printf("%s⚠%s  No profile found for %s\n", ansiYellow, ansiReset, cwd)
		os.Exit(1)
	}
	fmt.Println(name)
}

func cmdShow(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: caddie show <name>")
	}
	name := args[0]
	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}
	file := config.EnvFile(name)
	displayName := config.ReadScalar(file, "name")
	directory := config.ReadScalar(file, "directory")

	label := cmp.Or(displayName, name)
	fmt.Printf("%sEnvironment: %s%s%s\n\n", ansiBold, ansiCyan, label, ansiReset)

	if directory != "" {
		expanded := config.ExpandTilde(directory)
		fmt.Printf("  %sSkills managed in:%s\n", ansiDim, ansiReset)
		fmt.Printf("    %s%s/.agents/skills/ (.claude/skills → symlink)%s\n", ansiDim, expanded, ansiReset)
		if _, err := os.Stat(filepath.Join(expanded, ".claude", ".caddie-fingerprint")); err == nil {
			fmt.Printf("    %s(fingerprint: active)%s\n", ansiDim, ansiReset)
		}
		fmt.Println()
	}

	data, err := os.ReadFile(file)
	if err != nil {
		die(err.Error())
	}
	os.Stdout.Write(data)

	fmt.Println()
	fmt.Printf("%sResolved skills:%s\n", ansiBold, ansiReset)

	patterns := config.ReadList(file, "skills")
	var ids []string
	if skills.StoreExists() {
		ids, _ = skills.ResolveIDs(patterns)
	}
	// Two zeros are emitted when there are no matches to mirror the historical
	// `grep -c . || echo 0` shell quirk that contract tests pin.
	if len(ids) == 0 {
		fmt.Printf("  0\n0 skills matched\n\n")
	} else {
		fmt.Printf("  %d skills matched\n\n", len(ids))
	}
	for _, id := range ids {
		fmt.Printf("  %s\n", id)
	}
}

// cmdCreate prompts for display name, description, directory, and skill
// patterns; writes an environment YAML. Prompt text is gated on tty so the
// contract tests that pipe stdin still match.
func cmdCreate(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: caddie create <name>")
	}
	name := args[0]
	if config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' already exists. Use 'caddie edit %s' to modify it.", name, name))
	}
	file := config.EnvFile(name)

	fmt.Printf("%sCreating environment: %s%s%s\n\n", ansiBold, ansiCyan, name, ansiReset)

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

	displayName := readLine(fmt.Sprintf("Display name [%s]: ", name))
	if displayName == "" {
		displayName = name
	}
	description := readLine("Description: ")
	directory := readLine(fmt.Sprintf("Working directory [~/Dev/%s]: ", name))
	if directory == "" {
		directory = "~/Dev/" + name
	}

	fmt.Println()
	fmt.Println("Skill patterns (enter one per line, empty line to finish):")
	fmt.Println(`  Examples: "gws:*", "superpowers:*", "local:*", "*" (all)`)
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
	fmt.Fprintf(&body, "directory: \"%s\"\n", directory)
	body.WriteString("\n")
	body.WriteString("skills:\n")
	for _, p := range patterns {
		fmt.Fprintf(&body, "  - \"%s\"\n", p)
	}
	body.WriteString("\n")
	body.WriteString("# agents:\n")
	body.WriteString("#   claude:\n")
	body.WriteString("#     model: \"claude-sonnet-4-6\"\n")
	body.WriteString("#     permission_mode: \"plan\"\n")
	body.WriteString("#     system_prompt_file: \"./CLAUDE.md\"\n")

	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		die(err.Error())
	}
	if err := os.WriteFile(file, []byte(body.String()), 0o644); err != nil {
		die(err.Error())
	}

	fmt.Println()
	fmt.Printf("%s✓%s  Created environment: %s%s%s\n", ansiGreen, ansiReset, ansiBold, name, ansiReset)
	fmt.Printf("%sℹ%s  Config: %s%s%s\n", ansiBlue, ansiReset, ansiDim, file, ansiReset)
	fmt.Printf("  Edit:     %scaddie edit %s%s\n", ansiCyan, name, ansiReset)
	fmt.Printf("  Preview:  %scaddie activate %s --dry-run%s\n", ansiCyan, name, ansiReset)
	fmt.Printf("  Activate: %scaddie activate %s%s\n", ansiCyan, name, ansiReset)
}

// cmdInit performs idempotent filesystem setup: config dirs, skill store,
// backups, migrations, ~/.claude/skills symlink, example env, then runs scan.
func cmdInit(args []string) {
	fmt.Printf("%sInitializing caddie skill profile manager...%s\n\n", ansiBold, ansiReset)

	home := config.Home()
	configDir := config.Dir()
	envDir := config.EnvDir()
	skillStore := skills.Store()
	claudeSkills := filepath.Join(home, ".claude", "skills")
	agentSkills := filepath.Join(home, ".agents", "skills")
	sourcesFile := repos.File()

	_ = os.MkdirAll(configDir, 0o755)
	_ = os.MkdirAll(envDir, 0o755)
	_ = os.MkdirAll(skillStore, 0o755)
	fmt.Printf("%s✓%s  Config directory: %s\n", ansiGreen, ansiReset, configDir)
	fmt.Printf("%s✓%s  Environments: %s\n", ansiGreen, ansiReset, envDir)
	fmt.Printf("%s✓%s  Skill store: %s\n", ansiGreen, ansiReset, skillStore)

	date := time.Now().Format("2006-01-02")
	for _, d := range []string{claudeSkills, agentSkills} {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil || len(entries) == 0 {
			continue
		}
		backup := d + ".bak." + date
		if _, err := os.Stat(backup); err == nil {
			fmt.Printf("%sℹ%s  Backup already exists: %s\n", ansiBlue, ansiReset, backup)
			continue
		}
		cmd := exec.Command("cp", "-a", d, backup)
		if cmd.Run() == nil {
			fmt.Printf("%s✓%s  Backed up %s to %s\n", ansiGreen, ansiReset, d, backup)
		}
	}

	migrated := 0
	for _, src := range []string{claudeSkills, agentSkills} {
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			continue
		}
		for _, e := range entries {
			full := filepath.Join(src, e.Name())
			li, err := os.Lstat(full)
			if err != nil {
				continue
			}
			if li.Mode()&os.ModeSymlink != 0 {
				continue
			}
			base := e.Name()
			if !li.IsDir() && strings.HasSuffix(base, ".md") {
				skillName := strings.TrimSuffix(base, ".md")
				skillDir := filepath.Join(skillStore, skillName)
				if _, err := os.Stat(skillDir); err == nil {
					continue
				}
				if err := os.MkdirAll(skillDir, 0o755); err != nil {
					continue
				}
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0o644); err != nil {
					continue
				}
				_ = os.Remove(full)
				fmt.Printf("%sℹ%s  Migrated file: %s -> %s/SKILL.md (from %s)\n", ansiBlue, ansiReset, base, skillName, src)
				migrated++
			} else if li.IsDir() {
				target := filepath.Join(skillStore, base)
				if _, err := os.Stat(target); err == nil {
					continue
				}
				if err := os.Rename(full, target); err != nil {
					continue
				}
				fmt.Printf("%sℹ%s  Migrated dir: %s (from %s)\n", ansiBlue, ansiReset, base, src)
				migrated++
			}
		}
	}
	if migrated > 0 {
		fmt.Printf("%s✓%s  Migrated %d items to skill store\n", ansiGreen, ansiReset, migrated)
	} else {
		fmt.Printf("%sℹ%s  No bare files/dirs to migrate\n", ansiBlue, ansiReset)
	}

	ensureClaudeSkillsSymlink(claudeSkills, agentSkills)
	fmt.Printf("%s✓%s  Set up ~/.claude/skills → ~/.agents/skills\n", ansiGreen, ansiReset)

	if _, err := os.Stat(sourcesFile); os.IsNotExist(err) {
		if err := os.WriteFile(sourcesFile, []byte("sources:\n"), 0o644); err != nil {
			die(err.Error())
		}
		fmt.Printf("%sℹ%s  Add repos with: %scaddie repo add <name> <url>%s\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	} else {
		fmt.Printf("%sℹ%s  Sources file already exists: %s\n", ansiBlue, ansiReset, sourcesFile)
	}

	if entries, err := os.ReadDir(envDir); err == nil && len(entries) == 0 {
		example := filepath.Join(envDir, "example.yaml")
		exampleBody := `name: "Example"
description: "An example environment -- edit or delete me"
directory: "~/projects/example"

skills:
  - "*"

# agents:
#   claude:
#     model: "claude-sonnet-4-6"
#     permission_mode: "plan"
`
		_ = os.WriteFile(example, []byte(exampleBody), 0o644)
		fmt.Printf("%sℹ%s  Created example environment: %scaddie show example%s\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	}

	fmt.Println()
	cmdScan(nil)

	fmt.Println()
	fmt.Printf("%sℹ%s  Next steps:\n", ansiBlue, ansiReset)
	fmt.Printf("  %scaddie repo list%s             Review registered repos\n", ansiCyan, ansiReset)
	fmt.Printf("  %scaddie inventory%s             See all discovered skills\n", ansiCyan, ansiReset)
	fmt.Printf("  %scaddie create my-project%s     Create your first environment\n", ansiCyan, ansiReset)
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

	home := config.Home()
	var log skills.LogFn
	if verbose {
		log = func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
	}

	swept := skills.SweepAgentDir(home, log)
	if swept > 0 {
		fmt.Fprintf(out, "%sℹ%s  Swept %d new skill(s) from agent dirs into store\n", ansiBlue, ansiReset, swept)
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

	// Orphaned-pattern warnings: any env pattern matching zero store items.
	envDir := config.EnvDir()
	envEntries, err := os.ReadDir(envDir)
	if err != nil {
		return
	}
	scannedItems, _ := skills.Scan()
	hasWarn := false
	for _, ef := range envEntries {
		if ef.IsDir() || !strings.HasSuffix(ef.Name(), ".yaml") {
			continue
		}
		envName := strings.TrimSuffix(ef.Name(), ".yaml")
		patterns := config.ReadList(filepath.Join(envDir, ef.Name()), "skills")
		for _, p := range patterns {
			if p == "" {
				continue
			}
			if skills.PatternMatchesIn(scannedItems, p) == 0 {
				if !hasWarn {
					fmt.Fprintln(out)
					hasWarn = true
				}
				fmt.Fprintf(out, "%s⚠%s  Environment %s%s%s: pattern %s\"%s\"%s matches 0 skills — source may have been removed\n",
					ansiYellow, ansiReset, ansiBold, envName, ansiReset, ansiCyan, p, ansiReset)
			}
		}
	}
}

// cmdActivate resolves the target environment (explicit arg → .caddie.yaml
// → directory match → interactive picker), syncs repos, resolves skills,
// fingerprints, and minimally reconciles the target .agents/skills directory.
// Writes a fingerprint + .gitignore entries for project-local targets.
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
			die("Unknown argument: " + a)
		}
	}

	cwd, _ := os.Getwd()
	profile := config.FindProfile(cwd)
	if profile == "" {
		die(fmt.Sprintf("No caddie profile found for %s\n   Run %scaddie init%s to create one.",
			cwd, ansiCyan, ansiReset))
	}

	profileDir := filepath.Dir(profile)
	displayName := config.ReadScalar(profile, "name")
	label := cmp.Or(displayName, filepath.Base(profileDir))

	targetAgents := filepath.Join(profileDir, ".agents", "skills")
	fingerprintFile := filepath.Join(profileDir, ".claude", ".caddie-fingerprint")

	fmt.Printf("%s%s%s %s→ %s%s\n", ansiBold, label, ansiReset, ansiDim, profileDir, ansiReset)

	activateSyncRepos(force)

	patterns := config.ReadList(profile, "skills")

	// Resolve matched skills + per-prefix counts.
	matched, prefixSummary := resolveMatchedWithSummary(patterns)
	skillCount := len(matched)

	if dryRun {
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

	res, err := skills.ReconcileSkillDir(targetAgents, matched, store)
	if err != nil {
		die(err.Error())
	}
	totalAdded, totalRemoved := res.Added, res.Removed

	ensureClaudeSkillsSymlink(filepath.Join(profileDir, ".claude", "skills"), targetAgents)
	ensureProjectGitignore(profileDir)

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

// ensureProjectGitignore appends managed entries to <project>/.gitignore
// (idempotently) when the project has a .git directory.
func ensureProjectGitignore(projectDir string) {
	if info, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil || !info.IsDir() {
		return
	}
	gitignore := filepath.Join(projectDir, ".gitignore")
	entries := []string{
		".claude/skills/",
		".agents/skills/",
		".agents/SOURCES.md",
		".claude/.caddie-fingerprint",
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
		return
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
	_ = os.WriteFile(gitignore, []byte(out.String()), 0o644)
}

// cmdExport resolves the env's skills (or all skills with --all), then
// dispatches to internal/export for either a local directory or an s3:// URL.
func cmdExport(args []string) {
	var (
		name   string
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
			name = a
		}
	}

	if target == "" {
		die("Usage: caddie export [<env>|--all] --to <dir-or-s3> [--clean] [--dry-run]")
	}
	if !all && name == "" {
		die("Specify an environment name or use --all.\nUsage: caddie export [<env>|--all] --to <target> [--clean] [--dry-run]")
	}
	if !all && !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}

	var patterns []string
	if all {
		patterns = []string{"*:*"}
	} else {
		patterns = config.ReadList(config.EnvFile(name), "skills")
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
