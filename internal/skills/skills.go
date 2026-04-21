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

// sortedKeys returns the map's keys in sorted order. Needed because Go map
// iteration is randomized; bash/python walked their dicts in insertion order
// but keys in our fixtures are substring-disjoint in practice, so stable
// lexicographic order keeps classification deterministic.
func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// resolve mirrors PY_PREFIX_HELPER._get_prefix + inventory's get_source_type
// in a single pass. For symlinks, resolves the target once and substring-
// matches against sourceMap (→ prefix = source name, type = plugin) then
// repoMap (→ prefix = repo prefix, type = repo), fallback ("plugin", plugin).
// For plain dirs, splits on first "-"; "local" if no hyphen.
func resolve(full, dirName string, isSymlink bool, sourceMap, repoMap map[string]string) (prefix, remainder string, typ SourceType) {
	if isSymlink {
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			target = full
		}
		for _, k := range sortedKeys(sourceMap) {
			if strings.Contains(target, k) {
				return sourceMap[k], dirName, TypePlugin
			}
		}
		for _, k := range sortedKeys(repoMap) {
			if strings.Contains(target, k) {
				return repoMap[k], dirName, TypeRepo
			}
		}
		return "plugin", dirName, TypePlugin
	}
	if i := strings.Index(dirName, "-"); i >= 0 {
		return dirName[:i], dirName[i+1:], TypeLocal
	}
	return "local", dirName, TypeLocal
}

// Scan reads the store and returns one Item per directory entry (symlinks
// to dirs included — python's is_dir() follows symlinks), sorted by literal
// dirname.
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
		full := filepath.Join(storeDir, e.Name())
		// DirEntry.Type() uses the cached syscall from ReadDir — cheap.
		// Follow symlinks before classifying as dir, matching is_dir().
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		isSymlink := e.Type()&os.ModeSymlink != 0
		prefix, remainder, typ := resolve(full, e.Name(), isSymlink, sourceMap, repoMap)
		items = append(items, Item{
			Prefix:    prefix,
			Remainder: remainder,
			DirName:   e.Name(),
			Type:      typ,
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
