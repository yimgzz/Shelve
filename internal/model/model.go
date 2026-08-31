package model

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// AuthType selects the authentication method for a session or jump host.
type AuthType int

const (
	// AuthPassword authenticates with a stored password.
	AuthPassword AuthType = iota
	// AuthKey authenticates with an SSH private key file. The key file
	// passphrase is never stored in the model (master plan A2).
	AuthKey
)

// Auth holds exactly one credential: a password XOR a key path
// (master plan §4). Enforced by Validate.
type Auth struct {
	Type     AuthType `json:"type"`
	Password string   `json:"password,omitempty"`
	KeyPath  string   `json:"keyPath,omitempty"`
}

// JumpHost is one hop of an ordered jump-host chain (master plan D5).
type JumpHost struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	Auth Auth   `json:"auth"`
}

// Folder is a node in the session tree. Children (subfolders and sessions)
// is an ordered ID list; the store keeps it in sync with its own ordering.
type Folder struct {
	ID       string   `json:"id"`
	ParentID string   `json:"parentId"`
	Name     string   `json:"name"`
	Children []string `json:"children"`
}

// Session is a connectable SSH endpoint (master plan §4).
type Session struct {
	ID              string     `json:"id"`
	FolderID        string     `json:"folderId"`
	Name            string     `json:"name"`
	Host            string     `json:"host"`
	Port            int        `json:"port"`
	User            string     `json:"user"`
	Auth            Auth       `json:"auth"`
	JumpHosts       []JumpHost `json:"jumpHosts"`
	ExtraArgs       string     `json:"extraArgs"`
	SftpInitialPath string     `json:"sftpInitialPath"` // per-session SFTP browser start path ("" = global default, plan P002)
}

// Payload is the plaintext document encrypted inside vault.json
// (master plan §4). Root holds the ordered top-level node IDs; every
// folder's Children holds its ordered child IDs (folders and sessions),
// which is the single source of truth for sibling order (master plan A10).
type Payload struct {
	Root     []string  `json:"root"`
	Folders  []Folder  `json:"folders"`
	Sessions []Session `json:"sessions"`
}

// NewID returns a new ULID string (master plan A10 session IDs).
func NewID() string {
	return ulid.MustNewDefault(time.Now()).String()
}
