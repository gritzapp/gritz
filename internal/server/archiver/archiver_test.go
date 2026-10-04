package archiver

import (
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestArchiver_Tick_ArchivesEligibleTask(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusCompleted})
	// Negative auto_archive means archive immediately once terminal, which
	// keeps the test fast and deterministic.
	task.AutoArchive = -time.Hour
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, got.Archived, "task should be archived after tick")
}

// A draft is not terminal, but it is archivable, so the query admits it.
func TestArchiver_Tick_ArchivesDraft(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusDraft})
	task.AutoArchive = -time.Hour
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, got.Archived, "draft should be archived after tick")
}

func TestArchiver_Tick_SkipsTaskWithoutAutoArchive(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)
	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusCompleted})

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, !got.Archived, "task without auto_archive must not be archived")
}

func TestArchiver_Tick_SkipsNonTerminalTask(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusRunning})
	task.AutoArchive = -time.Hour
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, !got.Archived, "running task must not be auto-archived")
}

func TestArchiver_Tick_SkipsTaskWithPendingCommand(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusCompleted})
	task.AutoArchive = -time.Hour
	task.Command = model.TaskCommandRestart
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, !got.Archived, "task with pending command must not be auto-archived")
}

func TestArchiver_Tick_SkipsFutureDeadline(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusCompleted})
	task.AutoArchive = time.Hour
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, !got.Archived, "task with future deadline must not be auto-archived yet")
}

// TestArchiver_Tick_SessionTimezoneDoesNotSkewDeadline is a regression test for
// the premature auto-archive bug (issue #1092). The archiver query compares the
// naive UTC `updated_at` column against the database clock; if that comparison
// is resolved in a non-UTC session timezone, the deadline is skewed by the
// session's UTC offset and a just-updated task is archived early. Drive a Tick
// through a session pinned far east of UTC (Asia/Tokyo, +9h) against a task with
// a one-hour window: the deadline is comfortably in the future, so it must not
// archive regardless of the session timezone.
func TestArchiver_Tick_SessionTimezoneDoesNotSkewDeadline(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	// pgx treats unrecognized connection-string parameters as session runtime
	// parameters, so `timezone` pins this pool's sessions to Asia/Tokyo.
	u, err := url.Parse(dsn)
	assert.NilError(t, err)
	q := u.Query()
	q.Set("timezone", "Asia/Tokyo")
	u.RawQuery = q.Encode()

	db, err := store.Open(u.String(), false)
	assert.NilError(t, err)
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	org := teststore.CreateOrg(t, st, nil)
	task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusCompleted})
	task.AutoArchive = time.Hour
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	a := New(Options{Store: st})
	assert.NilError(t, a.Tick(t.Context()))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, !got.Archived, "task with a future deadline must not be archived under a non-UTC session timezone")
}

func TestArchiver_Tick_BatchSizeBounded(t *testing.T) {
	// Not t.Parallel(): the archiver query is server-wide (no org filter), so
	// a batch picked while another test's tasks are also eligible would not
	// deterministically pick "this test's" tasks.
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	const total = 5
	taskIDs := make([]int64, total)
	for i := 0; i < total; i++ {
		task := teststore.CreateTask(t, st, org, &teststore.TaskOptions{Status: model.TaskStatusCompleted})
		task.AutoArchive = -time.Hour
		assert.NilError(t, st.UpdateTask(t.Context(), nil, task))
		taskIDs[i] = task.ID
	}

	a := New(Options{Store: st, BatchSize: 2})
	assert.NilError(t, a.Tick(t.Context()))

	// Exactly batch-size tasks should be archived; the rest remain for later ticks.
	archived := 0
	for _, id := range taskIDs {
		got, err := st.GetTask(t.Context(), nil, id, org.OrgID)
		assert.NilError(t, err)
		if got.Archived {
			archived++
		}
	}
	assert.Equal(t, archived, 2, "expected batch-size limit to bound archives per tick")
}

func TestArchiver_TickRoundTripPersistsAutoArchive(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)

	d := 42 * time.Minute
	task := teststore.CreateTask(t, st, org, nil)
	task.AutoArchive = d
	assert.NilError(t, st.UpdateTask(t.Context(), nil, task))

	got, err := st.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.AutoArchive, d)
}
