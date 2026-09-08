package wailsvc

import (
	"shelve/internal/model"
	"shelve/internal/sftp"
	"shelve/internal/store"
	"time"
)

// DTOs are the JSON contract for the generated frontend bindings
// (master plan §5). They never expose internal structs, and secrets
// never leave the vault: passwords are collapsed to HasPassword.

// NodeDTO is a session-tree node.
type NodeDTO struct {
	Kind     string    `json:"kind"` // "folder" | "session"
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Children []NodeDTO `json:"children"`
}

// JumpHostDTO exposes a jump host without its password. Bastion marks a
// bastion-style hop (plan P009): the last handshake, target-embedding.
type JumpHostDTO struct {
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	User        string         `json:"user"`
	AuthType    model.AuthType `json:"authType"`
	HasPassword bool           `json:"hasPassword"`
	KeyPath     string         `json:"keyPath,omitempty"`
	Bastion     bool           `json:"bastion,omitempty"`
}

// SessionDTO is a session read view: no password material. CredentialID
// and JumpHostRef expose only references to saved entities (plan P003 /
// plan P006); never the underlying secrets.
type SessionDTO struct {
	ID              string         `json:"id"`
	FolderID        string         `json:"folderId"`
	Name            string         `json:"name"`
	Host            string         `json:"host"`
	Port            int            `json:"port"`
	User            string         `json:"user"`
	AuthType        model.AuthType `json:"authType"`
	HasPassword     bool           `json:"hasPassword"`
	KeyPath         string         `json:"keyPath,omitempty"`
	JumpHosts       []JumpHostDTO  `json:"jumpHosts"`
	ExtraArgs       string         `json:"extraArgs"`
	SftpInitialPath string         `json:"sftpInitialPath,omitempty"`
	CredentialID    string         `json:"credentialId,omitempty"`
	JumpHostRef     string         `json:"jumpHostRef,omitempty"`
}

// SessionInput carries a session draft from the frontend. It may include
// passwords; they only ever reach the vault inside the encrypted payload.
// CredentialID references a named credential whose User+Auth are resolved
// at connect/test time (plan P003); JumpHostRef references a saved jump
// host whose hop replaces the inline chain (plan P006).
type SessionInput struct {
	ID              string          `json:"id,omitempty"`
	FolderID        string          `json:"folderId"`
	Name            string          `json:"name"`
	Host            string          `json:"host"`
	Port            int             `json:"port"`
	User            string          `json:"user"`
	AuthType        model.AuthType  `json:"authType"`
	Password        string          `json:"password,omitempty"`
	KeyPath         string          `json:"keyPath,omitempty"`
	JumpHosts       []JumpHostInput `json:"jumpHosts"`
	ExtraArgs       string          `json:"extraArgs"`
	SftpInitialPath string          `json:"sftpInitialPath,omitempty"`
	CredentialID    string          `json:"credentialId,omitempty"`
	JumpHostRef     string          `json:"jumpHostRef,omitempty"`
}

// JumpHostInput is the write view of a jump host. Bastion marks a
// bastion-style hop (plan P009).
type JumpHostInput struct {
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	User     string         `json:"user"`
	AuthType model.AuthType `json:"authType"`
	Password string         `json:"password,omitempty"`
	KeyPath  string         `json:"keyPath,omitempty"`
	Bastion  bool           `json:"bastion,omitempty"`
}

// CredentialDTO is a credential read view (plan P003 §4.3): secret-free —
// passwords collapse to HasPassword, exactly like sessions/jump hosts.
type CredentialDTO struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	User        string         `json:"user"`
	AuthType    model.AuthType `json:"authType"`
	HasPassword bool           `json:"hasPassword"`
	KeyPath     string         `json:"keyPath,omitempty"`
}

// SavedJumpHostDTO is a saved jump host read view (plan P006):
// secret-free — passwords collapse to HasPassword, exactly like
// sessions/jump hosts.
type SavedJumpHostDTO struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	User        string         `json:"user"`
	AuthType    model.AuthType `json:"authType"`
	HasPassword bool           `json:"hasPassword"`
	KeyPath     string         `json:"keyPath,omitempty"`
	Bastion     bool           `json:"bastion,omitempty"`
}

// SavedJumpHostInput carries a saved jump host draft from the frontend.
// The password, when present, goes straight into the encrypted vault and
// is never echoed back (§8).
type SavedJumpHostInput struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	User     string         `json:"user"`
	AuthType model.AuthType `json:"authType"`
	Password string         `json:"password,omitempty"`
	KeyPath  string         `json:"keyPath,omitempty"`
	Bastion  bool           `json:"bastion,omitempty"`
}

func (in SavedJumpHostInput) toModel() model.SavedJumpHost {
	return model.SavedJumpHost{
		ID:      in.ID,
		Name:    in.Name,
		Host:    in.Host,
		Port:    in.Port,
		User:    in.User,
		Auth:    model.Auth{Type: in.AuthType, Password: in.Password, KeyPath: in.KeyPath},
		Bastion: in.Bastion,
	}
}

// CredentialInput carries a credential draft from the frontend. The
// password, when present, goes straight into the encrypted vault and is
// never echoed back (§8).
type CredentialInput struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	User     string         `json:"user"`
	AuthType model.AuthType `json:"authType"`
	Password string         `json:"password,omitempty"`
	KeyPath  string         `json:"keyPath,omitempty"`
}

func (in CredentialInput) toModel() model.Credential {
	return model.Credential{
		ID:   in.ID,
		Name: in.Name,
		User: in.User,
		Auth: model.Auth{Type: in.AuthType, Password: in.Password, KeyPath: in.KeyPath},
	}
}

// ToCredentialDTO converts a model credential to its secret-free read
// view (plan P003 §4.3).
func ToCredentialDTO(c model.Credential) CredentialDTO {
	return CredentialDTO{
		ID:          c.ID,
		Name:        c.Name,
		User:        c.User,
		AuthType:    c.Auth.Type,
		HasPassword: c.Auth.Password != "",
		KeyPath:     c.Auth.KeyPath,
	}
}

// ToSavedJumpHostDTO converts a model saved jump host to its secret-free
// read view (plan P006).
func ToSavedJumpHostDTO(jh model.SavedJumpHost) SavedJumpHostDTO {
	return SavedJumpHostDTO{
		ID:          jh.ID,
		Name:        jh.Name,
		Host:        jh.Host,
		Port:        jh.Port,
		User:        jh.User,
		AuthType:    jh.Auth.Type,
		HasPassword: jh.Auth.Password != "",
		KeyPath:     jh.Auth.KeyPath,
		Bastion:     jh.Bastion,
	}
}

func (in SessionInput) toModel() model.Session {
	sess := model.Session{
		ID:              in.ID,
		FolderID:        in.FolderID,
		Name:            in.Name,
		Host:            in.Host,
		Port:            in.Port,
		User:            in.User,
		Auth:            model.Auth{Type: in.AuthType, Password: in.Password, KeyPath: in.KeyPath},
		ExtraArgs:       in.ExtraArgs,
		SftpInitialPath: in.SftpInitialPath,
		CredentialID:    in.CredentialID,
		JumpHostRef:     in.JumpHostRef,
	}
	sess.JumpHosts = make([]model.JumpHost, 0, len(in.JumpHosts))
	for _, j := range in.JumpHosts {
		sess.JumpHosts = append(sess.JumpHosts, model.JumpHost{
			Host:    j.Host,
			Port:    j.Port,
			User:    j.User,
			Auth:    model.Auth{Type: j.AuthType, Password: j.Password, KeyPath: j.KeyPath},
			Bastion: j.Bastion,
		})
	}
	return sess
}

func toJumpDTO(j model.JumpHost) JumpHostDTO {
	return JumpHostDTO{
		Host:        j.Host,
		Port:        j.Port,
		User:        j.User,
		AuthType:    j.Auth.Type,
		HasPassword: j.Auth.Password != "",
		KeyPath:     j.Auth.KeyPath,
		Bastion:     j.Bastion,
	}
}

// ToSessionDTO converts a model session to its secret-free read view.
func ToSessionDTO(sess model.Session) SessionDTO {
	dto := SessionDTO{
		ID:              sess.ID,
		FolderID:        sess.FolderID,
		Name:            sess.Name,
		Host:            sess.Host,
		Port:            sess.Port,
		User:            sess.User,
		AuthType:        sess.Auth.Type,
		HasPassword:     sess.Auth.Password != "",
		KeyPath:         sess.Auth.KeyPath,
		ExtraArgs:       sess.ExtraArgs,
		SftpInitialPath: sess.SftpInitialPath,
		CredentialID:    sess.CredentialID,
		JumpHostRef:     sess.JumpHostRef,
	}
	dto.JumpHosts = make([]JumpHostDTO, 0, len(sess.JumpHosts))
	for _, j := range sess.JumpHosts {
		dto.JumpHosts = append(dto.JumpHosts, toJumpDTO(j))
	}
	return dto
}

func toNodeDTOs(nodes []store.TreeNode) []NodeDTO {
	out := make([]NodeDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodeDTO{
			Kind:     n.Kind,
			ID:       n.ID,
			Name:     n.Name,
			Children: toNodeDTOs(n.Children),
		})
	}
	return out
}

// SftpEntryDTO is one SFTP listing row for the frontend (master plan §5).
// TextLike mirrors the sftp package's display-only classification (icon
// hint); EditRemoteText accepts any file ≤ 2 MiB unless its content probes
// as binary (ErrBinary).
type SftpEntryDTO struct {
	Name     string    `json:"name"`
	IsDir    bool      `json:"isDir"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modTime"`
	TextLike bool      `json:"textLike"`
}

func toSftpEntryDTOs(entries []sftp.Entry) []SftpEntryDTO {
	out := make([]SftpEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, SftpEntryDTO{
			Name:     e.Name,
			IsDir:    e.IsDir,
			Size:     e.Size,
			ModTime:  e.ModTime,
			TextLike: e.TextLike,
		})
	}
	return out
}
