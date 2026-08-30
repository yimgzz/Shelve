// Package sftp provides SFTP operations over an active engine connection
// (list, mkdir, rename, remove, get/put) and the temp-file "edit text file
// with system editor" flow (master plan §2 D4, A5: data streams in Go,
// only paths cross IPC).
//
// Phase 5a implements the per-tab client manager, remote path resolution,
// the browse operations (List/Mkdir/Rename/Remove) with TextLike
// classification, and the engine lifecycle hook. Upload/download/edit land
// in Phase 5b. The package talks to the engine through the TabProvider
// interface only — it never imports internal/sshengine.
package sftp
