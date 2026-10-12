package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoAddPrefix(t *testing.T) {
	bare := makeBareRepo(t)
	for _, tc := range []struct {
		args     []string
		prefix   string
		selector string
		filename string
	}{
		{[]string{"--prefix", "team"}, "team", "team:*", "team-alpha"},
		{[]string{"--prefix=team", "--skills-path=skills"}, "team", "team:*", "team-alpha"},
		{[]string{"skills", "--prefix", "team"}, "team", "team:*", "team-alpha"},
		{[]string{"--prefix", "true"}, "true", "mine:*", "mine-alpha"},
		{[]string{"--prefix", "false"}, "false", "mine:*", "alpha"},
		{nil, "", "mine:*", "alpha"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			dir := setupEnvDir(t)
			opts := runOpts{aiEnvDir: dir, home: t.TempDir()}
			r := runWith(t, buildGoBinary(t), opts, append([]string{"repo", "add", "mine", bare}, tc.args...)...)
			if r.exitCode != 0 {
				t.Fatalf("add failed: %s", r.stderr)
			}
			body, _ := os.ReadFile(filepath.Join(dir, "sources.yaml"))
			if tc.prefix != "" && !strings.Contains(string(body), "    prefix: \""+tc.prefix+"\"\n") {
				t.Fatalf("prefix not persisted: %s", body)
			}
			for _, hint := range []string{"skills:", "- \"" + tc.selector + "\"", ".caddie.yaml", "caddie activate"} {
				if !strings.Contains(r.stdout, hint) {
					t.Errorf("missing project setup hint %q: %s", hint, r.stdout)
				}
			}
			r = runWith(t, buildGoBinary(t), opts, "scan", "-f")
			if r.exitCode != 0 {
				t.Fatalf("scan failed: %s", r.stderr)
			}
			assertRepoLink(t, filepath.Join(dir, "skills", tc.filename), filepath.Join(dir, "repos", "mine", "skills", "alpha"))
		})
	}
}

func TestRepoEditPrefixTransitions(t *testing.T) {
	bare := makeBareRepo(t)
	for _, tc := range []struct{ before, after, oldName, newName, selector string }{
		{"", "team", "alpha", "team-alpha", "team:*"},
		{"old", "team", "old-alpha", "team-alpha", "team:*"},
		{"old", "true", "old-alpha", "mine-alpha", "mine:*"},
		{"old", "false", "old-alpha", "alpha", "mine:*"},
		{"team", "team", "team-alpha", "team-alpha", "team:*"},
	} {
		t.Run(tc.before+"_to_"+tc.after, func(t *testing.T) {
			dir := setupEnvDir(t)
			opts := runOpts{aiEnvDir: dir, home: t.TempDir()}
			cloneBareInto(t, bare, filepath.Join(dir, "repos", "mine"))
			prefix := ""
			if tc.before != "" {
				prefix = "    prefix: \"" + tc.before + "\"\n"
			}
			before := "# registry\nsources:\n  - name: \"mine\"\n    prefix: \"source-only\"\n\nrepos:\n" +
				"  - name: \"mine\"\n    url: \"" + bare + "\"\n    skills_path: \"skills\"\n" + prefix +
				"    # keep this comment\n    extra: \"unchanged\"\n" +
				"  - name: \"mine-extra\"\n    url: \"other\"\n    prefix: \"other\"\n\nsettings:\n  prefix: \"untouched\"\n"
			mustWrite(t, filepath.Join(dir, "sources.yaml"), before)
			r := runWith(t, buildGoBinary(t), opts, "scan", "-f")
			if r.exitCode != 0 {
				t.Fatalf("initial scan failed: %s", r.stderr)
			}
			// These entries must survive cleanup, even though their names look stale.
			mustWrite(t, filepath.Join(dir, "skills", "old-local", "SKILL.md"), "local")
			foreign := filepath.Join(t.TempDir(), "repos", "mine", "skills", "alpha")
			mustWrite(t, filepath.Join(foreign, "SKILL.md"), "foreign")
			if err := os.Symlink(foreign, filepath.Join(dir, "skills", "foreign-alias")); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				r = runWith(t, buildGoBinary(t), opts, "repo", "edit", "mine", "--prefix="+tc.after)
				if r.exitCode != 0 {
					t.Fatalf("edit failed: stdout=%s stderr=%s", r.stdout, r.stderr)
				}
				if !strings.Contains(r.stdout, tc.selector) || !strings.Contains(r.stdout, "caddie activate") {
					t.Errorf("missing selector or activation hint: %s", r.stdout)
				}
			}
			body, _ := os.ReadFile(filepath.Join(dir, "sources.yaml"))
			want := strings.Replace(before, "    skills_path: \"skills\"\n"+prefix,
				"    skills_path: \"skills\"\n    prefix: \""+tc.after+"\"\n", 1)
			if string(body) != want {
				t.Errorf("unexpected registry rewrite:\ngot %s\nwant %s", body, want)
			}
			assertRepoLink(t, filepath.Join(dir, "skills", tc.newName), filepath.Join(dir, "repos", "mine", "skills", "alpha"))
			if tc.oldName != tc.newName {
				if _, err := os.Lstat(filepath.Join(dir, "skills", tc.oldName)); !os.IsNotExist(err) {
					t.Errorf("stale link survived: %v", err)
				}
			}
			assertRepoLink(t, filepath.Join(dir, "skills", "foreign-alias"), foreign)
			if body, err := os.ReadFile(filepath.Join(dir, "skills", "old-local", "SKILL.md")); err != nil || string(body) != "local" {
				t.Errorf("local override changed: %q %v", body, err)
			}
			project := realTempDir(t)
			mustWrite(t, filepath.Join(project, ".caddie.yaml"), "skills:\n  - \""+tc.selector+"\"\n")
			opts.cwd = project
			r = runWith(t, buildGoBinary(t), opts, "activate")
			if r.exitCode != 0 {
				t.Fatalf("activate failed: %s", r.stderr)
			}
			assertRepoLink(t, filepath.Join(project, ".agents", "skills", tc.newName), filepath.Join(dir, "skills", tc.newName))
		})
	}
}

func TestRepoEditRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"edit"}, {"edit", "mine"}, {"edit", "mine", "--prefix"},
		{"edit", "mine", "--prefix="}, {"edit", "mine", "--prefix", "../escape"},
		{"edit", "mine", "--prefix", "a/b"}, {"edit", "mine", "--prefix", "bad:*"},
		{"edit", "mine", "--prefix", "a b"}, {"edit", "mine", "--prefix", "a\ninjected"},
		{"edit", "mine", "--prefix", "-team"}, {"edit", "mine", "--prefix", `a\b`},
		{"edit", "mine", "--prefix", "team", "extra"},
		{"edit", "mine", "--prefix", "team", "--prefix", "other"},
		{"edit", "mine", "--unknown", "team"}, {"edit", "ghost", "--prefix", "team"},
		{"edit", "../mine", "--prefix", "team"},
		{"add", "new", "u", "--prefix"}, {"add", "new", "u", "--prefix", "../escape"},
		{"add", "new", "u", "--unknown"}, {"add", "new", "u", "skills", "extra"},
		{"add", "new", "u", "skills", "--skills-path", "other"},
		{"add", "new", "u", "--skills-path"}, {"add", "new", "u", "--prefix="},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dir := setupEnvDir(t)
			before := "repos:\n  - name: \"mine\"\n    url: \"u\"\n"
			mustWrite(t, filepath.Join(dir, "sources.yaml"), before)
			r := runWith(t, buildGoBinary(t), runOpts{aiEnvDir: dir, home: t.TempDir()}, append([]string{"repo"}, args...)...)
			if r.exitCode != 1 || r.stderr == "" {
				t.Errorf("expected argument error: %+v", r)
			}
			body, _ := os.ReadFile(filepath.Join(dir, "sources.yaml"))
			if string(body) != before {
				t.Errorf("invalid command changed registry: %s", body)
			}
		})
	}
}

func TestRepoEditUncloned(t *testing.T) {
	dir := setupEnvDir(t)
	mustWrite(t, filepath.Join(dir, "sources.yaml"), "repos:\n  - name: \"mine\"\n    url: \"u\"\n")
	r := runWith(t, buildGoBinary(t), runOpts{aiEnvDir: dir}, "repo", "edit", "mine", "--prefix", "team")
	if r.exitCode != 0 || !strings.Contains(r.stdout, "not cloned") || !strings.Contains(r.stdout, "caddie repo update mine") {
		t.Fatalf("expected saved config and clone hint: %+v", r)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "sources.yaml"))
	if !strings.Contains(string(body), "prefix: \"team\"") {
		t.Errorf("prefix not saved: %s", body)
	}
}

func TestRepoEditDestinationCollision(t *testing.T) {
	dir := setupEnvDir(t)
	bare := makeBareRepo(t)
	cloneBareInto(t, bare, filepath.Join(dir, "repos", "mine"))
	mustWrite(t, filepath.Join(dir, "sources.yaml"), "repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n")
	opts := runOpts{aiEnvDir: dir, home: t.TempDir()}
	runWith(t, buildGoBinary(t), opts, "scan", "-f")
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(dir, "skills", "team-alpha")); err != nil {
		t.Fatal(err)
	}
	r := runWith(t, buildGoBinary(t), opts, "repo", "edit", "mine", "--prefix", "team")
	if r.exitCode != 1 || !strings.Contains(r.stderr, "collision") {
		t.Fatalf("expected collision error: %+v", r)
	}
	assertRepoLink(t, filepath.Join(dir, "skills", "team-alpha"), foreign)
	assertRepoLink(t, filepath.Join(dir, "skills", "alpha"), filepath.Join(dir, "repos", "mine", "skills", "alpha"))
}

func TestRepoEditPreservesLocalDestination(t *testing.T) {
	dir := setupEnvDir(t)
	bare := makeBareRepo(t)
	cloneBareInto(t, bare, filepath.Join(dir, "repos", "mine"))
	mustWrite(t, filepath.Join(dir, "sources.yaml"), "repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n")
	mustWrite(t, filepath.Join(dir, "skills", "team-alpha", "SKILL.md"), "local override")
	r := runWith(t, buildGoBinary(t), runOpts{aiEnvDir: dir}, "repo", "edit", "mine", "--prefix", "team")
	if r.exitCode != 0 || !strings.Contains(r.stdout, "Local override 'team-alpha'") {
		t.Fatalf("expected local override warning: %+v", r)
	}
	body, err := os.ReadFile(filepath.Join(dir, "skills", "team-alpha", "SKILL.md"))
	if err != nil || string(body) != "local override" {
		t.Errorf("local override changed: %q %v", body, err)
	}
}

func TestRepoEditFailedWalkRetainsLinks(t *testing.T) {
	dir := setupEnvDir(t)
	bare := makeBareRepo(t)
	cloneBareInto(t, bare, filepath.Join(dir, "repos", "mine"))
	mustWrite(t, filepath.Join(dir, "sources.yaml"), "repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"missing\"\n")
	store := filepath.Join(dir, "skills")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../repos/mine/skills/alpha", filepath.Join(store, "alpha")); err != nil {
		t.Fatal(err)
	}
	r := runWith(t, buildGoBinary(t), runOpts{aiEnvDir: dir}, "repo", "edit", "mine", "--prefix", "team")
	if r.exitCode != 1 || !strings.Contains(r.stderr, "store sync failed") {
		t.Fatalf("expected walk failure: %+v", r)
	}
	assertRepoLink(t, filepath.Join(store, "alpha"), "../repos/mine/skills/alpha")
}

func TestRepoEditCleansRelativeStaleLinks(t *testing.T) {
	dir := setupEnvDir(t)
	bare := makeBareRepo(t)
	cloneBareInto(t, bare, filepath.Join(dir, "repos", "mine"))
	mustWrite(t, filepath.Join(dir, "sources.yaml"), "repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n")
	store := filepath.Join(dir, "skills")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../repos/mine/skills/alpha", filepath.Join(store, "old-alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../repos/mine/skills/deleted", filepath.Join(store, "old-deleted")); err != nil {
		t.Fatal(err)
	}
	r := runWith(t, buildGoBinary(t), runOpts{aiEnvDir: dir}, "repo", "edit", "mine", "--prefix", "team")
	if r.exitCode != 0 {
		t.Fatalf("edit failed: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(store, "old-alpha")); !os.IsNotExist(err) {
		t.Errorf("relative stale link survived: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(store, "old-deleted")); !os.IsNotExist(err) {
		t.Errorf("broken stale link survived: %v", err)
	}
	assertRepoLink(t, filepath.Join(store, "team-alpha"), filepath.Join(dir, "repos", "mine", "skills", "alpha"))
}

func TestRepoEditUnreadableWalkRetainsLinks(t *testing.T) {
	dir := setupEnvDir(t)
	bare := makeBareRepo(t)
	cloneBareInto(t, bare, filepath.Join(dir, "repos", "mine"))
	mustWrite(t, filepath.Join(dir, "sources.yaml"), "repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n")
	root := filepath.Join(dir, "repos", "mine", "skills")
	store := filepath.Join(dir, "skills")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(store, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, err := os.ReadDir(root); err == nil {
		t.Skip("current user can read directories without permission")
	}
	r := runWith(t, buildGoBinary(t), runOpts{aiEnvDir: dir}, "repo", "edit", "mine", "--prefix", "team")
	if r.exitCode != 1 || !strings.Contains(r.stderr, "store sync failed") {
		t.Fatalf("expected unreadable walk failure: %+v", r)
	}
	assertRepoLink(t, filepath.Join(store, "alpha"), filepath.Join(root, "alpha"))
}

func assertRepoLink(t *testing.T, link, want string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil || got != want {
		t.Fatalf("%s -> %q, want %q: %v", link, got, want, err)
	}
}
