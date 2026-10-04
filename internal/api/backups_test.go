package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Backups end to end: on-demand backup, download limited to administrators, nightly backup (one a
// day at most), restore prepared then canceled, deletion.
func TestBackups(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	backupDir := filepath.Join(data, "backups")
	st, err := store.Open(ctx, filepath.Join(data, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", DataDir: data, CacheDir: t.TempDir(), BackupDir: backupDir, NoAutoScans: true})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(func() { srv.Close(); cancel(); a.Wait(); _ = st.Close() })
	login, err := a.Setup(ctx, "admin", "a-strong-password", domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	token := login.Token
	system := laternav1connect.NewSystemServiceClient(srv.Client(), srv.URL)
	list := func() *laternav1.ListBackupsResponse {
		t.Helper()
		resp, err := system.ListBackups(ctx, authed(&laternav1.ListBackupsRequest{}, token))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	if got := list(); len(got.GetBackups()) != 0 || got.GetRestorePending() {
		t.Fatalf("at the start: %v", got)
	}

	// On demand.
	created, err := system.CreateBackup(ctx, authed(&laternav1.CreateBackupRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	manual := created.Msg.GetBackup()
	if manual.GetKind() != laternav1.BackupKind_BACKUP_KIND_MANUAL || manual.GetSizeBytes() == 0 || !strings.HasPrefix(manual.GetName(), "laterna-manual-") {
		t.Fatalf("backup: %v", manual)
	}
	get := func(name, bearer string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/backups/"+name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
		return resp.StatusCode, string(body)
	}
	if code, body := get(manual.GetName(), token); code != http.StatusOK || body != "SQLite format 3\x00" {
		t.Errorf("download: %d %q", code, body)
	}
	if code, _ := get(manual.GetName(), ""); code != http.StatusUnauthorized {
		t.Errorf("download without a token: %d", code)
	}
	for _, name := range []string{"laterna.db", "..%2Flaterna.db", "laterna-manual-20260101T000000Z.db"} {
		if code, _ := get(name, token); code != http.StatusNotFound {
			t.Errorf("download of %s: %d", name, code)
		}
	}

	// Nightly: the purge task makes one, not two on the same day.
	for range 2 {
		if _, err := system.RunTask(ctx, authed(&laternav1.RunTaskRequest{Task: laternav1.SystemTask_SYSTEM_TASK_PURGE}, token)); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if counts, err := st.Read().CountJobs(ctx); err != nil || len(counts) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("job queue never empty")
			}
		}
	}
	autos := 0
	for _, b := range list().GetBackups() {
		if b.GetKind() == laternav1.BackupKind_BACKUP_KIND_AUTO {
			autos++
		}
	}
	if autos != 1 {
		t.Errorf("%d nightly backups", autos)
	}

	// Setting: 0 to 90.
	tooMany := int32(91)
	if _, err := system.UpdateSettings(ctx, authed(&laternav1.UpdateSettingsRequest{BackupKeep: &tooMany}, token)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("91 backups kept: %v", err)
	}

	// Restore prepared, then canceled.
	if _, err := system.RestoreBackup(ctx, authed(&laternav1.RestoreBackupRequest{Name: "laterna-manual-20260101T000000Z.db"}, token)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("restore of an unknown backup: %v", err)
	}
	if _, err := system.RestoreBackup(ctx, authed(&laternav1.RestoreBackupRequest{Name: manual.GetName()}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, store.RestoreName)); err != nil || !list().GetRestorePending() {
		t.Errorf("restore not prepared: %v", err)
	}
	cancelled, err := system.CancelRestore(ctx, authed(&laternav1.CancelRestoreRequest{}, token))
	if err != nil || !cancelled.Msg.GetCancelled() || list().GetRestorePending() {
		t.Errorf("cancel: %v %v", cancelled, err)
	}

	// Deletion.
	if _, err := system.DeleteBackup(ctx, authed(&laternav1.DeleteBackupRequest{Name: manual.GetName()}, token)); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(manual.GetName(), token); code != http.StatusNotFound {
		t.Errorf("deleted backup still served: %d", code)
	}
}
