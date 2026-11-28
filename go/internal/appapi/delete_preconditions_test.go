package appapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/fsutil"
	"media-dedupe/internal/mediastore"
	"media-dedupe/internal/model"
	"media-dedupe/internal/project"
)

// deleteFixture is a project whose exact group has one recommended file and two
// cleanup candidates, stored the way a scan would store them (size, nanosecond
// mtime and file identity all recorded).
type deleteFixture struct {
	app     *App
	project project.Project
	groupID int64
	ids     map[string]int64
	paths   map[string]string
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	if err := os.Mkdir(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := project.Open(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "delete", Paths: []string{mediaDir}})
	if err != nil {
		t.Fatal(err)
	}
	settings := project.DefaultSettings()
	settings.AllowPermanentDelete = true
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, media: mediastore.New()}
	c, err := app.openProjectCache(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	f := &deleteFixture{app: app, project: p, groupID: 1, ids: map[string]int64{}, paths: map[string]string{}}
	for _, name := range []string{"keeper.bin", "cand-a.bin", "cand-b.bin", "outside.bin"} {
		path := filepath.Join(mediaDir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		size, mtimeNs, inode, device, err := fsutil.SafeStat(path)
		if err != nil {
			t.Fatal(err)
		}
		id, err := c.UpsertFile(model.DiscoveredFile{
			Path: path, MediaType: model.MediaImage, SizeBytes: size, MTimeNs: mtimeNs, Inode: inode, Device: device,
		})
		if err != nil {
			t.Fatal(err)
		}
		f.ids[name] = id
		f.paths[name] = path
	}
	// outside.bin is stored but deliberately not a member of the group.
	items := []model.ReportItem{
		{FileID: f.ids["keeper.bin"], Path: f.paths["keeper.bin"], Action: model.ActionKeep, SizeBytes: 9},
		{FileID: f.ids["cand-a.bin"], Path: f.paths["cand-a.bin"], Action: model.ActionCleanup, SizeBytes: 9},
		{FileID: f.ids["cand-b.bin"], Path: f.paths["cand-b.bin"], Action: model.ActionCleanup, SizeBytes: 9},
	}
	if err := c.ReplaceReportGroups([]model.ReportGroup{{
		GroupID: f.groupID, GroupType: model.GroupExact, Confidence: 1,
		RecommendedFileID: f.ids["keeper.bin"], Items: items,
	}}, 0); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *deleteFixture) deleteByFileIDs(ids ...int64) (DeleteResultDTO, error) {
	return f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent", GroupIDs: []int64{f.groupID}, FileIDs: ids,
	})
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file disappeared: %s: %v", path, err)
	}
}

// A single file whose snapshot no longer matches is skipped, not fatal: the rest
// of the batch still runs, and the skip is reported with its own reason.
func expectRejected(t *testing.T, result DeleteResultDTO, err error, want string) DeleteFailureDTO {
	t.Helper()
	if err != nil {
		t.Fatalf("per-file rejection must not become an error: %v", err)
	}
	if len(result.Succeeded) != 0 {
		t.Fatalf("file was deleted anyway: %+v", result.Succeeded)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("expected one rejection, got %+v", result.Failed)
	}
	if !strings.Contains(result.Failed[0].Message, want) {
		t.Fatalf("unexpected reason: %q", result.Failed[0].Message)
	}
	return result.Failed[0]
}

// A candidate rewritten after the scan is no longer the file whose duplicates
// were compared; deleting it by path would destroy unrelated content.
func TestDeleteRejectsRewrittenCandidate(t *testing.T) {
	f := newDeleteFixture(t)
	path := f.paths["cand-a.bin"]
	if err := os.WriteFile(path, []byte("rewritten content"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := f.deleteByFileIDs(f.ids["cand-a.bin"])
	rejection := expectRejected(t, res, err, "扫描后已被修改")
	if rejection.FileID != f.ids["cand-a.bin"] || rejection.GroupID != f.groupID {
		t.Fatalf("rejection lost its identity: %+v", rejection)
	}
	mustExist(t, path)
}

// A path that was never a member of the named group cannot be made deletable by
// sending it explicitly.
func TestDeleteRejectsNonMemberPath(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent",
		GroupIDs: []int64{f.groupID}, Paths: []string{f.paths["outside.bin"]},
	})
	if err == nil {
		t.Fatal("deleted a path outside the named group")
	}
	mustExist(t, f.paths["outside.bin"])
}

// An unknown group id must not behave like "no group restriction", and a retry
// that outlives its group must not block the groups that still exist.
func TestDeleteRejectsUnknownGroup(t *testing.T) {
	f := newDeleteFixture(t)
	res, err := f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent",
		GroupIDs: []int64{999999}, Paths: []string{f.paths["cand-a.bin"]},
	})
	if err != nil {
		t.Fatalf("unknown group must be reported, not fatal: %v", err)
	}
	if len(res.Succeeded) != 0 {
		t.Fatalf("deleted through an unknown group id: %+v", res.Succeeded)
	}
	if len(res.Failed) != 1 || res.Failed[0].GroupID != 999999 || !strings.Contains(res.Failed[0].Message, "不在当前结果中") {
		t.Fatalf("unknown group not reported: %+v", res.Failed)
	}
	mustExist(t, f.paths["cand-a.bin"])

	// Mixed request: the real group is still deleted, the stale one is reported.
	mixed, err := f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent",
		GroupIDs: []int64{f.groupID, 999999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed.Succeeded) != 2 || len(mixed.Failed) != 1 {
		t.Fatalf("mixed batch: %+v", mixed)
	}
}

// The recommended file is what the group keeps; removing it silently inverts the
// decision the results page showed.
func TestDeleteRejectsRecommendedFile(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.deleteByFileIDs(f.ids["keeper.bin"])
	if err == nil {
		t.Fatal("deleted the recommended file")
	} else if !strings.Contains(err.Error(), "推荐保留文件") {
		t.Fatalf("unexpected error: %v", err)
	}
	mustExist(t, f.paths["keeper.bin"])
}

// Deleting without a group leaves nothing to check the paths against.
func TestDeleteRequiresGroupScope(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent", Paths: []string{f.paths["cand-a.bin"]},
	})
	if err == nil {
		t.Fatal("deleted without a group scope")
	}
	mustExist(t, f.paths["cand-a.bin"])
}

// Replacing the file behind a path (same size, restored mtime) is a different
// file than the one the scan hashed.
func TestDeleteRejectsReplacedPath(t *testing.T) {
	f := newDeleteFixture(t)
	path := f.paths["cand-a.bin"]
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	size, _, inode, _, err := fsutil.SafeStat(path)
	if err != nil {
		t.Fatal(err)
	}
	if inode == 0 {
		t.Skip("platform reports no file identity")
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement.tmp")
	if err := os.WriteFile(replacement, []byte(strings.Repeat("x", int(size))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	// Keep size and timestamp identical so only the identity differs.
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	res, err := f.deleteByFileIDs(f.ids["cand-a.bin"])
	expectRejected(t, res, err, "已指向另一个文件")
	mustExist(t, path)
}

// The explicit path still works when it names a real member and the file is
// unchanged: older callers send paths without file ids.
func TestDeleteResolvesPathWithinGroup(t *testing.T) {
	f := newDeleteFixture(t)
	result, err := f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent",
		GroupIDs: []int64{f.groupID}, Paths: []string{f.paths["cand-a.bin"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Succeeded) != 1 || len(result.Failed) != 0 {
		t.Fatalf("delete result: %+v", result)
	}
	if _, err := os.Stat(f.paths["cand-a.bin"]); !os.IsNotExist(err) {
		t.Fatalf("candidate still present: %v", err)
	}
	mustExist(t, f.paths["keeper.bin"])
	mustExist(t, f.paths["cand-b.bin"])
}

// Bulk delete keeps working and never promotes review items.
func TestDeleteBulkUsesCleanupCandidates(t *testing.T) {
	f := newDeleteFixture(t)
	result, err := f.app.DeleteFiles(DeleteRequestDTO{ProjectID: f.project.ID, Mode: "permanent", GroupIDs: []int64{f.groupID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("bulk delete result: %+v", result)
	}
	mustExist(t, f.paths["keeper.bin"])
	if _, err := os.Stat(f.paths["cand-a.bin"]); !os.IsNotExist(err) {
		t.Fatalf("candidate-a still present: %v", err)
	}
}

// A file that vanished before the delete is reported per file instead of
// blocking the rest of the batch.
func TestDeleteToleratesAlreadyGoneFile(t *testing.T) {
	f := newDeleteFixture(t)
	if err := os.Remove(f.paths["cand-a.bin"]); err != nil {
		t.Fatal(err)
	}
	result, err := f.app.DeleteFiles(DeleteRequestDTO{ProjectID: f.project.ID, Mode: "permanent", GroupIDs: []int64{f.groupID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Succeeded) != 1 || len(result.Failed) != 1 {
		t.Fatalf("partial delete result: %+v", result)
	}
	mustExist(t, f.paths["keeper.bin"])
}

// A snapshot check must not drift with wall-clock: rewriting mtime only is a
// change too.
func TestDeleteRejectsTouchedCandidate(t *testing.T) {
	f := newDeleteFixture(t)
	path := f.paths["cand-a.bin"]
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := f.deleteByFileIDs(f.ids["cand-a.bin"])
	expectRejected(t, res, err, "扫描后已被修改")
	mustExist(t, path)
}

// A group whose remaining candidate could not be deleted must stay pending:
// otherwise the failed file leaves the pending list together with its group and
// can never be retried.
func TestDeletePartialFailureKeepsGroupPending(t *testing.T) {
	root := t.TempDir()
	dirOK := filepath.Join(root, "ok")
	dirLocked := filepath.Join(root, "locked")
	for _, dir := range []string{dirOK, dirLocked} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := project.Open(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "partial", Paths: []string{dirOK, dirLocked}})
	if err != nil {
		t.Fatal(err)
	}
	settings := project.DefaultSettings()
	settings.AllowPermanentDelete = true
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, media: mediastore.New()}
	c, err := app.openProjectCache(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	type member struct {
		name string
		dir  string
	}
	keepers := member{"keeper.bin", dirOK}
	cleanable := []member{{"cand-a.bin", dirOK}, {"cand-b.bin", dirLocked}}
	ids := map[string]int64{}
	var items []model.ReportItem
	for _, m := range append([]member{keepers}, cleanable...) {
		path := filepath.Join(m.dir, m.name)
		if err := os.WriteFile(path, []byte(m.name), 0o600); err != nil {
			t.Fatal(err)
		}
		size, mtimeNs, inode, device, err := fsutil.SafeStat(path)
		if err != nil {
			t.Fatal(err)
		}
		id, err := c.UpsertFile(model.DiscoveredFile{
			Path: path, MediaType: model.MediaImage, SizeBytes: size, MTimeNs: mtimeNs, Inode: inode, Device: device,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[m.name] = id
		action := model.ActionCleanup
		if m.name == keepers.name {
			action = model.ActionKeep
		}
		items = append(items, model.ReportItem{FileID: id, Path: path, Action: action, SizeBytes: size})
	}
	if err := c.ReplaceReportGroups([]model.ReportGroup{{
		GroupID: 1, GroupType: model.GroupExact, Confidence: 1,
		RecommendedFileID: ids[keepers.name], Items: items,
	}}, 0); err != nil {
		t.Fatal(err)
	}
	c.Close()

	// Removing a file needs write access on its parent directory.
	if err := os.Chmod(dirLocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dirLocked, 0o700) })

	result, err := app.DeleteFiles(DeleteRequestDTO{ProjectID: p.ID, Mode: "permanent", GroupIDs: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Succeeded) != 1 || len(result.Failed) != 1 {
		t.Fatalf("partial delete result: %+v", result)
	}
	if result.Failed[0].FileID != ids["cand-b.bin"] || result.Failed[0].GroupID != 1 {
		t.Fatalf("failure lost its identity: %+v", result.Failed[0])
	}
	if len(result.MarkedProcessed) != 0 {
		t.Fatalf("group marked processed with a candidate left: %+v", result.MarkedProcessed)
	}
	pending, err := app.ListGroups(p.ID, GroupFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Items) != 1 || pending.Items[0].CleanupCount != 1 {
		t.Fatalf("failed group left the pending list: %+v", pending)
	}

	// Retrying only the failed file must finish the group, without replaying the
	// file that was already removed.
	if err := os.Chmod(dirLocked, 0o700); err != nil {
		t.Fatal(err)
	}
	retry, err := app.DeleteFiles(DeleteRequestDTO{
		ProjectID: p.ID, Mode: "permanent", GroupIDs: []int64{1}, FileIDs: []int64{ids["cand-b.bin"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Succeeded) != 1 || len(retry.Failed) != 0 {
		t.Fatalf("retry result: %+v", retry)
	}
	if len(retry.MarkedProcessed) != 1 || retry.MarkedProcessed[0] != 1 {
		t.Fatalf("retry did not close the group: %+v", retry.MarkedProcessed)
	}
	mustExist(t, filepath.Join(dirOK, keepers.name))
}

// A retry sends file ids. If the group was pruned between the two requests those
// ids cannot be placed, and the retry must report them rather than fail as a
// whole — the file itself is untouched either way.
func TestRetryAfterGroupPrunedReportsInsteadOfFailing(t *testing.T) {
	f := newDeleteFixture(t)
	// Drop the group the way a rescan that no longer finds it would, keeping the
	// files on disk.
	c, err := cache.Open(f.app.store.CachePath(f.project.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM duplicate_items WHERE group_id = ?`,
		`DELETE FROM duplicate_groups WHERE id = ?`,
	} {
		if _, err := c.DB().Exec(stmt, f.groupID); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()

	result, err := f.app.DeleteFiles(DeleteRequestDTO{
		ProjectID: f.project.ID, Mode: "permanent", GroupIDs: []int64{f.groupID},
		FileIDs: []int64{f.ids["cand-b.bin"]},
	})
	if err != nil {
		t.Fatalf("pruned group must be reported, not fatal: %v", err)
	}
	if len(result.Succeeded) != 0 {
		t.Fatalf("deleted without a live group: %+v", result.Succeeded)
	}
	if len(result.Failed) != 1 || result.Failed[0].GroupID != f.groupID || result.Failed[0].FileID != 0 {
		t.Fatalf("expected one non-retryable report for the pruned group: %+v", result.Failed)
	}
	mustExist(t, f.paths["cand-b.bin"])
}

// An id that cannot be placed in any named group is reported, not deleted, and
// carries no fileId so the UI does not offer a retry that would fail again.
func TestDeleteReportsUnplaceableFileID(t *testing.T) {
	f := newDeleteFixture(t)
	result, err := f.deleteByFileIDs(424242)
	if err != nil {
		t.Fatalf("unplaceable id must be reported, not fatal: %v", err)
	}
	if len(result.Succeeded) != 0 {
		t.Fatalf("deleted something: %+v", result.Succeeded)
	}
	if len(result.Failed) != 1 || result.Failed[0].FileID != 0 || !strings.Contains(result.Failed[0].Message, "不属于所选组") {
		t.Fatalf("unexpected report: %+v", result.Failed)
	}
	mustExist(t, f.paths["cand-a.bin"])
	mustExist(t, f.paths["cand-b.bin"])
}
