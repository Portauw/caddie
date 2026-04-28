// Package skills models the canonical skill store at $CONFIG_DIR/skills/ and
// derives prefixes for each item.
package skills

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Portauw/caddie/internal/config"
	"github.com/Portauw/caddie/internal/repos"
)

// Store returns the canonical skill store path ($CONFIG_DIR/skills).
func Store() string { return filepath.Join(config.Dir(), "skills") }

// EachSymlink visits every symlink entry under dir and calls fn for each.
// Returns the number of symlinks found. fn may be nil (count only). dir is
// silently ignored if it doesn't exist.
func EachSymlink(dir string, fn func(name, full string)) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		li, err := os.Lstat(full)
		if err != nil || li.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if fn != nil {
			fn(e.Name(), full)
		}
		n++
	}
	return n
}

// EachSymlinkTarget is like EachSymlink but also resolves the link target
// for each entry. Use this only when the target is actually needed — it
// costs an extra Readlink syscall per entry.
func EachSymlinkTarget(dir string, fn func(name, full, target string)) int {
	return EachSymlink(dir, func(name, full string) {
		target, _ := os.Readlink(full)
		fn(name, full, target)
	})
}

// SourceType classifies each store entry: "repo" for symlinks into a
// registered repo, "local" for everything else.
type SourceType string

const (
	TypeLocal SourceType = "local"
	TypeRepo  SourceType = "repo"
)

// Item is one resolved skill entry from the store.
type Item struct {
	Prefix    string
	Remainder string
	DirName   string
	Type      SourceType
}

// BuildRepoMap returns a map of "repos/<name>" -> effective prefix
// for every registered repo.
func BuildRepoMap() (map[string]string, error) {
	rps, err := repos.Parse()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, r := range rps {
		if r.Name == "" {
			continue
		}
		out["repos/"+r.Name] = r.EffectivePrefix()
	}
	return out, nil
}

// resolve classifies one store entry. Symlinks resolving into a registered
// repo get the repo's effective prefix and TypeRepo, with `<prefix>-` stripped
// from the dirname so inventory shows `lenny:foo`, not `lenny:lenny-foo`.
// Plain dirs and orphan symlinks are TypeLocal.
//
// repoKeys must be the sorted keys of repoMap so classification is
// deterministic regardless of map iteration order.
func resolve(full, dirName string, isSymlink bool, repoMap map[string]string, repoKeys []string) (prefix, remainder string, typ SourceType) {
	if isSymlink {
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			target = full
		}
		for _, k := range repoKeys {
			if strings.Contains(target, k) {
				prefix = repoMap[k]
				remainder = strings.TrimPrefix(dirName, prefix+"-")
				return prefix, remainder, TypeRepo
			}
		}
	}
	if i := strings.Index(dirName, "-"); i >= 0 {
		return dirName[:i], dirName[i+1:], TypeLocal
	}
	return "local", dirName, TypeLocal
}

// Scan reads the store and returns one Item per directory entry (symlinks-to-
// dirs included), sorted by literal dirname.
func Scan() ([]Item, error) {
	storeDir := Store()
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return nil, err
	}
	repoMap, err := BuildRepoMap()
	if err != nil {
		return nil, err
	}
	repoKeys := make([]string, 0, len(repoMap))
	for k := range repoMap {
		repoKeys = append(repoKeys, k)
	}
	sort.Strings(repoKeys)

	var items []Item
	for _, e := range entries {
		full := filepath.Join(storeDir, e.Name())
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		isSymlink := e.Type()&os.ModeSymlink != 0
		prefix, remainder, typ := resolve(full, e.Name(), isSymlink, repoMap, repoKeys)
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

// matchPattern matches a skill id against a user-supplied glob. Skill ids
// never contain '/', so path.Match is sufficient.
func matchPattern(id, pattern string) bool {
	ok, _ := path.Match(pattern, id)
	return ok
}

// SkillID returns "<prefix>:<remainder>" — the form used to match against
// user-supplied patterns.
func (i Item) SkillID() string { return i.Prefix + ":" + i.Remainder }

// matchItems returns the store items whose SkillID matches at least one
// pattern. Empty patterns → no matches.
func matchItems(patterns []string) ([]Item, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	items, err := Scan()
	if err != nil {
		return nil, err
	}
	return filterByPatterns(items, patterns), nil
}

func filterByPatterns(items []Item, patterns []string) []Item {
	var out []Item
	for _, it := range items {
		id := it.SkillID()
		for _, p := range patterns {
			if matchPattern(id, p) {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// Resolve returns the sorted dirnames of store items matching any pattern.
func Resolve(patterns []string) ([]string, error) {
	matched, err := matchItems(patterns)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matched))
	for _, it := range matched {
		out = append(out, it.DirName)
	}
	sort.Strings(out)
	return out, nil
}

// ResolveIDs returns sorted skill display IDs (prefix:dirname) for matched
// skills.
func ResolveIDs(patterns []string) ([]string, error) {
	matched, err := matchItems(patterns)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matched))
	for _, it := range matched {
		out = append(out, it.SkillID())
	}
	sort.Strings(out)
	return out, nil
}

// PatternMatchesIn returns the number of items whose SkillID matches pattern.
// Caller is expected to share one Scan() result across many patterns.
func PatternMatchesIn(items []Item, pattern string) int {
	count := 0
	for _, it := range items {
		if matchPattern(it.SkillID(), pattern) {
			count++
		}
	}
	return count
}
