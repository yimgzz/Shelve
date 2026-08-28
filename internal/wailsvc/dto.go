package wailsvc

import (
	"dummy-ssh-manager/internal/model"
	"dummy-ssh-manager/internal/store"
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

// JumpHostDTO exposes a jump host without its password.
type JumpHostDTO struct {
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	User        string         `json:"user"`
	AuthType    model.AuthType `json:"authType"`
	HasPassword bool           `json:"hasPassword"`
	KeyPath     string         `json:"keyPath,omitempty"`
}

// SessionDTO is a session read view: no password material.
type SessionDTO struct {
	ID          string         `json:"id"`
	FolderID    string         `json:"folderId"`
	Name        string         `json:"name"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	User        string         `json:"user"`
	AuthType    model.AuthType `json:"authType"`
	HasPassword bool           `json:"hasPassword"`
	KeyPath     string         `json:"keyPath,omitempty"`
	JumpHosts   []JumpHostDTO  `json:"jumpHosts"`
	ExtraArgs   string         `json:"extraArgs"`
}

// SessionInput carries a session draft from the frontend. It may include
// passwords; they only ever reach the vault inside the encrypted payload.
type SessionInput struct {
	ID        string          `json:"id,omitempty"`
	FolderID  string          `json:"folderId"`
	Name      string          `json:"name"`
	Host      string          `json:"host"`
	Port      int             `json:"port"`
	User      string          `json:"user"`
	AuthType  model.AuthType  `json:"authType"`
	Password  string          `json:"password,omitempty"`
	KeyPath   string          `json:"keyPath,omitempty"`
	JumpHosts []JumpHostInput `json:"jumpHosts"`
	ExtraArgs string          `json:"extraArgs"`
}

// JumpHostInput is the write view of a jump host.
type JumpHostInput struct {
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	User     string         `json:"user"`
	AuthType model.AuthType `json:"authType"`
	Password string         `json:"password,omitempty"`
	KeyPath  string         `json:"keyPath,omitempty"`
}

func (in SessionInput) toModel() model.Session {
	sess := model.Session{
		ID:        in.ID,
		FolderID:  in.FolderID,
		Name:      in.Name,
		Host:      in.Host,
		Port:      in.Port,
		User:      in.User,
		Auth:      model.Auth{Type: in.AuthType, Password: in.Password, KeyPath: in.KeyPath},
		ExtraArgs: in.ExtraArgs,
	}
	sess.JumpHosts = make([]model.JumpHost, 0, len(in.JumpHosts))
	for _, j := range in.JumpHosts {
		sess.JumpHosts = append(sess.JumpHosts, model.JumpHost{
			Host: j.Host,
			Port: j.Port,
			User: j.User,
			Auth: model.Auth{Type: j.AuthType, Password: j.Password, KeyPath: j.KeyPath},
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
	}
}

// ToSessionDTO converts a model session to its secret-free read view.
func ToSessionDTO(sess model.Session) SessionDTO {
	dto := SessionDTO{
		ID:          sess.ID,
		FolderID:    sess.FolderID,
		Name:        sess.Name,
		Host:        sess.Host,
		Port:        sess.Port,
		User:        sess.User,
		AuthType:    sess.Auth.Type,
		HasPassword: sess.Auth.Password != "",
		KeyPath:     sess.Auth.KeyPath,
		ExtraArgs:   sess.ExtraArgs,
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
