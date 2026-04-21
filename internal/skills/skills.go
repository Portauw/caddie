// Package skills models the canonical skill store at $CONFIG_DIR/skills/ and
// derives prefixes for each item. Mirrors the PY_PREFIX_HELPER + cmd_inventory
// python blocks in the bash implementation line-for-line — do not generalize
// until more commands that touch the store are ported.
package skills

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Portauw/ai-env/internal/config"
	"github.com/Portauw/ai-env/internal/repos"
)

// Store returns the canonical skill store path ($CONFIG_DIR/skills).
func Store() string { return filepath.Join(config.Dir(), "skills") }

// SourceType classifies each store entry: "repo" for symlinks into a
// registered repo, "local" for everything else (plain dirs or symlinks that
// don't resolve into a known repo).
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
// (see repos.Entry.RepoName) for every registered repo.
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
		out["repos/"+r.Name] = r.RepoName()
	}
	return out, nil
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

// resolve classifies one store entry. Symlinks that resolve into a registered
// repo get the repo's prefix and TypeRepo; everything else (plain dirs, orphan
// symlinks) is TypeLocal — "local" if there's no prefix hyphen, otherwise the
// prefix is the portion before the first "-".
func resolve(full, dirName string, isSymlink bool, repoMap map[string]string) (prefix, remainder string, typ SourceType) {
	if isSymlink {
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			target = full
		}
		for _, k := range sortedKeys(repoMap) {
			if strings.Contains(target, k) {
				return repoMap[k], dirName, TypeRepo
			}
		}
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
	repoMap, err := BuildRepoMap()
	if err != nil {
		return nil, err
	}

	var items []Item
	for _, e := range entries {
		full := filepath.Join(storeDir, e.Name())
		// Follow symlinks before classifying as dir (matches python is_dir()).
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		isSymlink := e.Type()&os.ModeSymlink != 0
		prefix, remainder, typ := resolve(full, e.Name(), isSymlink, repoMap)
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

// matchPattern matches a skill id against a user-supplied pattern. Skill ids
// never contain '/', so stdlib path.Match (which handles *, ?, and [abc]
// classes) is a sufficient stand-in for python's fnmatch.fnmatch.
func matchPattern(id, pattern string) bool {
	ok, _ := path.Match(pattern, id)
	return ok
}

// SkillID returns "<prefix>:<remainder>" — the form used to match against
// user-supplied patterns in resolve_skills (python skill_map key).
func (i Item) SkillID() string { return i.Prefix + ":" + i.Remainder }

// DisplayID returns "<prefix>:<dirname>" — the form derive_skill_ids prints
// (python `f'{prefix}:{dirname}'`). Slightly different from SkillID when the
// dirname had a prefix stripped to form remainder.
func (i Item) DisplayID() string { return i.Prefix + ":" + i.DirName }

// matchItems returns the store items whose SkillID matches at least one
// pattern. Empty patterns → no matches (python `if not patterns: exit(0)`).
func matchItems(patterns []string) ([]Item, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	items, err := Scan()
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

// Resolve mirrors bash resolve_skills: sorted literal dirnames of matched
// store items.
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
// skills. Mirrors `resolve_skills | derive_skill_ids | sort` in cmd_show.
func ResolveIDs(patterns []string) ([]string, error) {
	matched, err := matchItems(patterns)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matched))
	for _, it := range matched {
		out = append(out, it.DisplayID())
	}
	sort.Strings(out)
	return out, nil
}
