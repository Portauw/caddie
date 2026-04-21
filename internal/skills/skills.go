// Package skills models the canonical skill store at $CONFIG_DIR/skills/ and
// derives prefixes for each item. Mirrors the PY_PREFIX_HELPER + cmd_inventory
// python blocks in the bash implementation line-for-line — do not generalize
// until more commands that touch the store are ported.
package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Portauw/ai-env/internal/config"
	"github.com/Portauw/ai-env/internal/repos"
	"github.com/Portauw/ai-env/internal/sources"
)

// Store returns the canonical skill store path ($CONFIG_DIR/skills).
func Store() string { return filepath.Join(config.Dir(), "skills") }

// SourceType is the classification printed next to each skill in `inventory`:
// "plugin" for symlinks into the plugin cache, "repo" for symlinks into a
// registered repo, "local" for non-symlink directories (no marker).
type SourceType string

const (
	TypeLocal  SourceType = "local"
	TypePlugin SourceType = "plugin"
	TypeRepo   SourceType = "repo"
)

// Item is one resolved skill entry from the store.
type Item struct {
	Prefix    string     // "local", "plugin", a source name, or a repo prefix
	Remainder string     // portion after the prefix in the display ID
	DirName   string     // literal basename in the store
	Type      SourceType // how the inventory classifies it
}

// BuildMaps returns (source_map, repo_map) exactly like PY_PREFIX_HELPER
// _build_maps does. source_map key is "marketplace/plugin" -> source name.
// repo_map key is "repos/<name>" -> effective prefix (see repos.Entry.RepoName).
func BuildMaps() (sourceMap, repoMap map[string]string, err error) {
	srcs, err := sources.Parse()
	if err != nil {
		return nil, nil, err
	}
	rps, err := repos.Parse()
	if err != nil {
		return nil, nil, err
	}
	sourceMap = make(map[string]string)
	repoMap = make(map[string]string)
	for _, s := range srcs {
		if s.Name == "" || s.Marketplace == "" || s.Plugin == "" {
			continue
		}
		sourceMap[s.Marketplace+"/"+s.Plugin] = s.Name
	}
	for _, r := range rps {
		if r.Name == "" {
			continue
		}
		repoMap["repos/"+r.Name] = r.RepoName()
	}
	return sourceMap, repoMap, nil
}

// getPrefix mirrors PY_PREFIX_HELPER._get_prefix for one item.
// For symlinks: resolve target, substring-match against source_map then
// repo_map keys, fallback "plugin". For plain dirs: split on first "-".
func getPrefix(storeDir, dirName string, sourceMap, repoMap map[string]string) (prefix, remainder string) {
	full := filepath.Join(storeDir, dirName)
	info, err := os.Lstat(full)
	if err != nil {
		return "local", dirName
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			target = full
		}
		for k, v := range sourceMap {
			if strings.Contains(target, k) {
				return v, dirName
			}
		}
		for k, v := range repoMap {
			if strings.Contains(target, k) {
				return v, dirName
			}
		}
		return "plugin", dirName
	}
	if i := strings.Index(dirName, "-"); i >= 0 {
		return dirName[:i], dirName[i+1:]
	}
	return "local", dirName
}

// classify mirrors inventory's get_source_type: symlinks whose resolved
// target contains one of the repo_map keys are "repo"; other symlinks are
// "plugin"; non-symlinks are "local".
func classify(storeDir, dirName string, repoMap map[string]string) SourceType {
	full := filepath.Join(storeDir, dirName)
	info, err := os.Lstat(full)
	if err != nil {
		return TypeLocal
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return TypeLocal
	}
	target, err := filepath.EvalSymlinks(full)
	if err != nil {
		target = full
	}
	for k := range repoMap {
		if strings.Contains(target, k) {
			return TypeRepo
		}
	}
	return TypePlugin
}

// Scan reads the store and returns one Item per directory entry, sorted by
// literal dirname (matching python's sorted(store.iterdir())).
func Scan() ([]Item, error) {
	storeDir := Store()
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return nil, err
	}
	sourceMap, repoMap, err := BuildMaps()
	if err != nil {
		return nil, err
	}

	var items []Item
	for _, e := range entries {
		// python: `if not item.is_dir(): continue`. is_dir() follows
		// symlinks, so we do too.
		full := filepath.Join(storeDir, e.Name())
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		prefix, remainder := getPrefix(storeDir, e.Name(), sourceMap, repoMap)
		items = append(items, Item{
			Prefix:    prefix,
			Remainder: remainder,
			DirName:   e.Name(),
			Type:      classify(storeDir, e.Name(), repoMap),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DirName < items[j].DirName })
	return items, nil
}

// StoreExists reports whether the canonical skill store directory exists.
func StoreExists() bool {
	info, err := os.Stat(Store())
	return err == nil && info.IsDir()
}
