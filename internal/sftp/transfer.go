package sftp

// Phase 5b streaming transfers (master plan §2 D4, A5): uploads/downloads
// stream the bytes in Go and only paths/events cross IPC. Each tab runs a
// single FIFO worker so at most one transfer is in flight per tab; extra
// requests queue. Progress is reported via the sftp:progress event, throttled
// to ≥256 KB or ≥250 ms, plus a terminal event (done == total). The master
// plan §5 payload is extended with tabID, fileName and an optional error field
// (non-empty on failure) — documented extension consumed by the 5c panel.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"
)

// Go→JS event names emitted by this package (master plan §5 event contract).
const (
	// EventProgress carries a ProgressPayload (sftp:progress).
	EventProgress = "sftp:progress"
	// EventToast carries a ToastPayload (app:toast).
	EventToast = "app:toast"
)

// ProgressPayload is the sftp:progress event body. It extends the master
// plan §5 payload {transferID, direction, doneBytes, totalBytes} with tabID,
// fileName and an optional error field that is non-empty only when a transfer
// failed (documented extension of the master payload).
type ProgressPayload struct {
	TransferID string `json:"transferID"`
	TabID      string `json:"tabID"`
	Direction  string `json:"direction"` // "up" | "down"
	FileName   string `json:"fileName"`
	DoneBytes  int64  `json:"doneBytes"`
	TotalBytes int64  `json:"totalBytes"`
	Error      string `json:"error,omitempty"`
}

// ToastPayload is the app:toast event body (master plan §5).
type ToastPayload struct {
	Level   string `json:"level"` // "info" | "error"
	Message string `json:"message"`
}

// ErrSftpDialogUnsupported is returned by PickLocalFiles when the native
// open-file dialog is unavailable in this build. The 5c frontend then shows a
// manual multi-path prompt instead (master plan phase 5b task 1 fallback).
var ErrSftpDialogUnsupported = errors.New("sftp: native file dialog unsupported in this build")

// Progress throttle thresholds (master plan phase 5b task 1).
const (
	progressThrottleBytes    = 256 << 10 // 256 KB
	progressThrottleInterval = 250 * time.Millisecond
)

// transferJob is one unit of work for a tab's FIFO transfer worker.
type transferJob struct {
	upload     bool
	tabID      string
	localPaths []string // upload
	remoteDir  string   // upload
	remotePath string   // download
	result     chan transferResult
}

// transferResult carries the outcome back to the synchronous caller.
type transferResult struct {
	path string
	err  error
}

// newULID returns a fresh ULID string (used for transfer and temp identifiers).
func newULID() string {
	return ulid.Make().String()
}

// emitToast emits an app:toast event (no-op when no emitter is attached).
func (m *Manager) emitToast(level, message string) {
	if m.emit == nil {
		return
	}
	m.emit.Emit(EventToast, ToastPayload{Level: level, Message: message})
}

// emitProgress emits an sftp:progress event (no-op when no emitter attached).
func (m *Manager) emitProgress(tabID, transferID, direction, fileName string, done, total int64, errMsg string) {
	if m.emit == nil {
		return
	}
	m.emit.Emit(EventProgress, ProgressPayload{
		TransferID: transferID,
		TabID:      tabID,
		Direction:  direction,
		FileName:   fileName,
		DoneBytes:  done,
		TotalBytes: total,
		Error:      errMsg,
	})
}

// progressSink throttles progress events for one transfer.
type progressSink struct {
	m            *Manager
	tabID        string
	transferID   string
	direction    string
	fileName     string
	total        int64
	lastEmitByte int64
	lastEmitTime time.Time
}

// report emits an intermediate event only when the byte or time threshold was
// crossed since the last emission.
func (p *progressSink) report(done int64) {
	now := time.Now()
	if done-p.lastEmitByte < progressThrottleBytes && now.Sub(p.lastEmitTime) < progressThrottleInterval {
		return
	}
	p.lastEmitByte = done
	p.lastEmitTime = now
	p.m.emitProgress(p.tabID, p.transferID, p.direction, p.fileName, done, p.total, "")
}

// final always emits a terminal event (done == total on success; a non-empty
// errMsg on failure — documented extension of the master payload).
func (p *progressSink) final(done int64, errMsg string) {
	p.m.emitProgress(p.tabID, p.transferID, p.direction, p.fileName, done, p.total, errMsg)
}

// streamCopy copies src → dst while feeding cumulative progress to sink.
// It returns the number of bytes copied and any write/read error.
func (m *Manager) streamCopy(dst io.Writer, src io.Reader, sink *progressSink) (int64, error) {
	buf := make([]byte, 64<<10)
	var done int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			wn, werr := dst.Write(buf[:n])
			done += int64(wn)
			sink.report(done)
			if werr != nil {
				return done, werr
			}
			if wn != n {
				return done, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	return done, nil
}

// enqueue places a job on the tab's FIFO worker queue, starting the worker on
// first use. The caller blocks on job.result until the worker finishes.
func (m *Manager) enqueue(job *transferJob) {
	m.transfersMu.Lock()
	ch, ok := m.transferChans[job.tabID]
	if !ok {
		ch = make(chan *transferJob)
		m.transferChans[job.tabID] = ch
		go m.transferWorker(job.tabID, ch)
	}
	m.transfersMu.Unlock()
	ch <- job
}

// transferWorker processes one tab's transfers strictly in FIFO order.
func (m *Manager) transferWorker(tabID string, ch chan *transferJob) {
	for job := range ch {
		res := transferResult{}
		if job.upload {
			res.err = m.doUpload(job.tabID, job.localPaths, job.remoteDir)
		} else {
			res.path, res.err = m.doDownload(job.tabID, job.remotePath)
		}
		job.result <- res
	}
}

// Upload streams local files into remoteDir, one in-flight transfer per tab
// (later calls queue FIFO). Progress events are emitted per file. On the first
// file failure the remaining queue for this batch is stopped, an app:toast is
// shown and the error is returned (the transfer's terminal progress event
// carries the same error in its Error field).
func (m *Manager) Upload(tabID string, localPaths []string, remoteDir string) error {
	job := &transferJob{
		upload:     true,
		tabID:      tabID,
		localPaths: localPaths,
		remoteDir:  remoteDir,
		result:     make(chan transferResult, 1),
	}
	m.enqueue(job)
	res := <-job.result
	return res.err
}

// doUpload runs the actual upload batch on the worker goroutine.
func (m *Manager) doUpload(tabID string, localPaths []string, remoteDir string) error {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return err
	}
	absDir, err := m.resolve(tabID, remoteDir)
	if err != nil {
		return err
	}
	for _, lp := range localPaths {
		f, err := os.Open(lp)
		if err != nil {
			// Missing/unreadable local file → typed error + toast; stop the
			// rest of this batch's queue (documented).
			merr := fmt.Errorf("sftp: open local %s: %w", lp, err)
			m.failUpload(tabID, filepath.Base(lp), merr)
			return merr
		}
		st, serr := f.Stat()
		if serr != nil {
			_ = f.Close()
			merr := fmt.Errorf("sftp: stat local %s: %w", lp, serr)
			m.failUpload(tabID, filepath.Base(lp), merr)
			return merr
		}
		remoteFile := path.Join(absDir, filepath.Base(lp))
		rf, cerr := c.Create(remoteFile)
		if cerr != nil {
			_ = f.Close()
			merr := fmt.Errorf("sftp: create %s: %w", remoteFile, cerr)
			m.failUpload(tabID, filepath.Base(lp), merr)
			return merr
		}
		transferID := newULID()
		sink := &progressSink{
			m: m, tabID: tabID, transferID: transferID, direction: "up",
			fileName: filepath.Base(lp), total: st.Size(), lastEmitTime: time.Now(),
		}
		done, cerr := m.streamCopy(rf, f, sink)
		_ = rf.Close()
		_ = f.Close()
		if cerr != nil {
			merr := fmt.Errorf("sftp: upload %s: %w", remoteFile, cerr)
			sink.final(done, merr.Error())
			m.emitToast("error", merr.Error())
			return merr
		}
		sink.final(st.Size(), "") // terminal event, done == total
	}
	return nil
}

// failUpload emits the terminal error progress event and a toast for one file.
func (m *Manager) failUpload(tabID, fileName string, err error) {
	m.emitProgress(tabID, newULID(), "up", fileName, 0, 0, err.Error())
	m.emitToast("error", err.Error())
}

// Download streams a remote file to tmp/<ULID><ext> (0600) and returns the
// local temp path. Progress is reported with direction "down". The temp file
// is removed on failure.
func (m *Manager) Download(tabID, remotePath string) (string, error) {
	job := &transferJob{
		upload:     false,
		tabID:      tabID,
		remotePath: remotePath,
		result:     make(chan transferResult, 1),
	}
	m.enqueue(job)
	res := <-job.result
	return res.path, res.err
}

// doDownload runs the actual download on the worker goroutine.
func (m *Manager) doDownload(tabID, remotePath string) (string, error) {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return "", err
	}
	abs, err := m.resolve(tabID, remotePath)
	if err != nil {
		return "", err
	}
	st, err := c.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("sftp: stat %s: %w", abs, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("sftp: download %s: is a directory", abs)
	}
	if err := m.ensureTmp(); err != nil {
		return "", err
	}
	tempPath := filepath.Join(m.tmpDir, newULID()+path.Ext(abs))
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	rf, err := c.Open(abs)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("sftp: open %s: %w", abs, err)
	}
	transferID := newULID()
	sink := &progressSink{
		m: m, tabID: tabID, transferID: transferID, direction: "down",
		fileName: path.Base(abs), total: st.Size(), lastEmitTime: time.Now(),
	}
	done, cerr := m.streamCopy(f, rf, sink)
	_ = rf.Close()
	_ = f.Close()
	if cerr != nil {
		_ = os.Remove(tempPath)
		merr := fmt.Errorf("sftp: download %s: %w", abs, cerr)
		sink.final(done, merr.Error())
		m.emitToast("error", merr.Error())
		return "", merr
	}
	sink.final(st.Size(), "") // terminal event, done == total
	return tempPath, nil
}

// DownloadThenSave downloads to a temp file and returns the final path. This
// build uses the documented fallback: no native save dialog API is wired, so
// it keeps the temp file, shows an app:toast with the path, and returns it
// (master plan phase 5b task 1 fallback — recorded in the commit message).
func (m *Manager) DownloadThenSave(tabID, remotePath string) (string, error) {
	p, err := m.Download(tabID, remotePath)
	if err != nil {
		return "", err
	}
	m.emitToast("info", "Downloaded to "+p)
	return p, nil
}

// PickLocalFiles opens the native file picker. This build has no wired dialog
// API, so it always returns the typed ErrSftpDialogUnsupported; the 5c
// frontend falls back to a manual multi-path prompt (task 1, recorded).
func (m *Manager) PickLocalFiles(multi bool) ([]string, error) {
	return nil, ErrSftpDialogUnsupported
}

// ensureTmp creates the config tmp/ directory as 0700 (master plan §4, §8.8).
func (m *Manager) ensureTmp() error {
	if err := os.MkdirAll(m.tmpDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(m.tmpDir, 0o700)
}
