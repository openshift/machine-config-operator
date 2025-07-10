package extended

import "fmt"

// RemoteFile handles files located remotely in a node
type RemoteFile struct {
	node     Node
	fullPath string
	statData map[string]string
	content  string
}

// NewRemoteFile creates a new instance of RemoteFile
func NewRemoteFile(node Node, fullPath string) *RemoteFile {
	return &RemoteFile{node: node, fullPath: fullPath}
}

// String implements the stringer interface
func (rf RemoteFile) String() string {
	return fmt.Sprintf("file %s in node %s", rf.fullPath, rf.node.GetName())
}
