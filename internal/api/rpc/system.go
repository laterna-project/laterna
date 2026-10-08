package rpc

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// SystemService implements laterna.v1.SystemService.
type SystemService struct {
	app *app.App
}

// GetSystemStatus describes the server.
func (s *SystemService) GetSystemStatus(ctx context.Context, _ *connect.Request[laternav1.GetSystemStatusRequest]) (*connect.Response[laternav1.GetSystemStatusResponse], error) {
	st, err := s.app.SystemStatus(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetSystemStatusResponse{Status: &laternav1.SystemStatus{
		Version: st.Version, Commit: st.Commit, StartedAt: timestamppb.New(st.StartedAt),
		Os: st.OS, Arch: st.Arch, GoVersion: st.GoVersion, Ffmpeg: st.FFmpeg,
		Encoders: st.Encoders, ToneMappers: st.ToneMappers, Gpu: st.GPU, Decoder: st.Decoder,
		DataDir: st.DataDir, CacheDir: st.CacheDir, MetadataDir: st.MetadataDir, LogDir: st.LogDir,
		Playbacks: clampInt32(st.Playbacks), FfmpegRunning: clampInt32(st.FFmpegRunning),
		Transcodes: clampInt32(st.Transcodes), TranscodeLimit: clampInt32(st.TranscodeLimit),
		JobsPending: clampInt32(st.JobsPending), JobsRunning: clampInt32(st.JobsRunning), JobsFailed: clampInt32(st.JobsFailed),
	}}), nil
}

func settingsMsg(st domain.Settings) *laternav1.Settings {
	return &laternav1.Settings{
		ServerName: st.ServerName, Language: st.Language, DownloadImages: st.DownloadImages,
		ScanInterval: durationpb.New(st.ScanInterval), MissingGrace: durationpb.New(st.MissingGrace),
		Trickplay: st.Trickplay, WatchLibraries: st.WatchLibraries, DetectSegments: st.DetectSegments,
		MaxTranscodes: clampInt32(st.MaxTranscodes), BackupKeep: clampInt32(st.BackupKeep), PublicUrl: st.PublicURL, WebUrl: st.WebURL, PasskeyRpId: st.PasskeyRPID, PasskeyOrigins: st.PasskeyOrigins,
	}
}

// GetSettings returns the settings.
func (s *SystemService) GetSettings(context.Context, *connect.Request[laternav1.GetSettingsRequest]) (*connect.Response[laternav1.GetSettingsResponse], error) {
	return connect.NewResponse(&laternav1.GetSettingsResponse{Settings: settingsMsg(s.app.Settings())}), nil
}

// UpdateSettings changes settings.
func (s *SystemService) UpdateSettings(ctx context.Context, req *connect.Request[laternav1.UpdateSettingsRequest]) (*connect.Response[laternav1.UpdateSettingsResponse], error) {
	m := req.Msg
	ch := app.SettingsChanges{
		ServerName: m.ServerName, Language: m.Language, DownloadImages: m.DownloadImages, Trickplay: m.Trickplay, WatchLibraries: m.WatchLibraries,
		DetectSegments: m.DetectSegments, PublicURL: m.PublicUrl, WebURL: m.WebUrl, PasskeyRPID: m.PasskeyRpId,
	}
	if m.MaxTranscodes != nil {
		n := int(m.GetMaxTranscodes())
		ch.MaxTranscodes = &n
	}
	if m.BackupKeep != nil {
		n := int(m.GetBackupKeep())
		ch.BackupKeep = &n
	}
	if m.GetPasskeyOrigins() != nil {
		origins := m.GetPasskeyOrigins().GetValues()
		ch.PasskeyOrigins = &origins
	}
	if m.GetScanInterval() != nil {
		d := m.GetScanInterval().AsDuration()
		ch.ScanInterval = &d
	}
	if m.GetMissingGrace() != nil {
		d := m.GetMissingGrace().AsDuration()
		ch.MissingGrace = &d
	}
	st, err := s.app.UpdateSettings(ctx, principal(ctx), ch)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateSettingsResponse{Settings: settingsMsg(st)}), nil
}

// ListJobs describes the job queue.
func (s *SystemService) ListJobs(ctx context.Context, _ *connect.Request[laternav1.ListJobsRequest]) (*connect.Response[laternav1.ListJobsResponse], error) {
	o, err := s.app.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListJobsResponse{}
	for _, c := range o.Counts {
		resp.Counts = append(resp.Counts, &laternav1.JobCount{Kind: c.Kind, State: c.State, Count: clampInt32(c.N)})
	}
	for _, f := range o.Failed {
		resp.Failed = append(resp.Failed, &laternav1.FailedJob{
			Id: f.ID, Kind: f.Kind, Label: f.Label, Attempts: clampInt32(f.Attempts), LastError: f.LastError, FailedAt: timestamppb.New(f.At),
		})
	}
	return connect.NewResponse(resp), nil
}

// RetryJobs retries failed jobs.
func (s *SystemService) RetryJobs(ctx context.Context, req *connect.Request[laternav1.RetryJobsRequest]) (*connect.Response[laternav1.RetryJobsResponse], error) {
	n, err := s.app.RetryJobs(ctx, req.Msg.GetIds())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RetryJobsResponse{Count: clampInt32(n)}), nil
}

// DeleteJobs forgets failed jobs.
func (s *SystemService) DeleteJobs(ctx context.Context, req *connect.Request[laternav1.DeleteJobsRequest]) (*connect.Response[laternav1.DeleteJobsResponse], error) {
	n, err := s.app.DeleteJobs(ctx, req.Msg.GetIds())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteJobsResponse{Count: clampInt32(n)}), nil
}

var systemTasks = map[laternav1.SystemTask]app.Task{
	laternav1.SystemTask_SYSTEM_TASK_SCAN_LIBRARIES: app.TaskScanLibraries,
	laternav1.SystemTask_SYSTEM_TASK_PURGE:          app.TaskPurge,
}

// RunTask runs a scheduled task.
func (s *SystemService) RunTask(ctx context.Context, req *connect.Request[laternav1.RunTaskRequest]) (*connect.Response[laternav1.RunTaskResponse], error) {
	task, ok := systemTasks[req.Msg.GetTask()]
	if !ok {
		return nil, domain.Invalid("system.unknown_task")
	}
	if err := s.app.RunTask(ctx, task); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RunTaskResponse{}), nil
}

var logLevels = map[laternav1.LogLevel]slog.Level{
	laternav1.LogLevel_LOG_LEVEL_DEBUG: slog.LevelDebug,
	laternav1.LogLevel_LOG_LEVEL_INFO:  slog.LevelInfo,
	laternav1.LogLevel_LOG_LEVEL_WARN:  slog.LevelWarn,
	laternav1.LogLevel_LOG_LEVEL_ERROR: slog.LevelError,
}

func logLevelMsg(l slog.Level) laternav1.LogLevel {
	switch {
	case l >= slog.LevelError:
		return laternav1.LogLevel_LOG_LEVEL_ERROR
	case l >= slog.LevelWarn:
		return laternav1.LogLevel_LOG_LEVEL_WARN
	case l >= slog.LevelInfo:
		return laternav1.LogLevel_LOG_LEVEL_INFO
	default:
		return laternav1.LogLevel_LOG_LEVEL_DEBUG
	}
}

// ListLogs returns the latest log messages.
func (s *SystemService) ListLogs(_ context.Context, req *connect.Request[laternav1.ListLogsRequest]) (*connect.Response[laternav1.ListLogsResponse], error) {
	m := req.Msg
	level, ok := logLevels[m.GetMinLevel()]
	if !ok {
		level = slog.LevelDebug
	}
	entries, err := s.app.RecentLogs(level, m.GetQuery(), int(m.GetLimit()))
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListLogsResponse{}
	for _, e := range entries {
		msg := &laternav1.LogEntry{Time: timestamppb.New(e.Time), Level: logLevelMsg(e.Level), Message: e.Message}
		for _, a := range e.Attrs {
			msg.Attrs = append(msg.Attrs, &laternav1.LogAttr{Key: a.Key, Value: a.Value})
		}
		resp.Entries = append(resp.Entries, msg)
	}
	return connect.NewResponse(resp), nil
}

// ListLogFiles lists the log files.
func (s *SystemService) ListLogFiles(context.Context, *connect.Request[laternav1.ListLogFilesRequest]) (*connect.Response[laternav1.ListLogFilesResponse], error) {
	files, err := s.app.LogFiles()
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListLogFilesResponse{}
	for _, f := range files {
		resp.Files = append(resp.Files, &laternav1.LogFile{Name: f.Name, Size: f.Size, ModifiedAt: timestamppb.New(f.ModTime)})
	}
	return connect.NewResponse(resp), nil
}
