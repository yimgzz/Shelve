package store

// Node kind labels in tree output (master plan §5 DTO contract).
const (
	KindFolder  = "folder"
	KindSession = "session"
)

// TreeNode is the neutral ordered tree shape returned by Tree.
type TreeNode struct {
	Kind     string     `json:"kind"`
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Children []TreeNode `json:"children"`
}
