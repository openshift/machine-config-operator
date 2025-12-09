package extended

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

const (
	statFormat = `--print=Name: %n\nSize: %s\nKind: %F\nPermissions: %04a/%A\nUID: %u/%U\nGID: %g/%G\nLinks: %h\nSymLink: %N\nSelinux: %C\n`
	statParser = `Name: (?P<name>.+)\n` +
		`Size: (?P<size>\d+)\n` +
		`Kind: (?P<kind>.*)\n` +
		`Permissions: (?P<octalperm>\d+)\/(?P<rwxperm>\S+)\n` +
		`UID: (?P<uidnumber>\d+)\/(?P<uidname>\S+)\n` +
		`GID: (?P<gidnumber>\d+)\/(?P<gidname>\S+)\n` +
		`Links: (?P<links>\d+)\n` +
		`SymLink: (?P<symlink>.*)\n` +
		`Selinux: (?P<selinux>.*)` // When parsing an "oc debug" command output, remember that the "utils" function will always trim the latest spaces and newlines
	startCat = "{{[[!\n"
	endCat   = "\n!]]}}"
)

// RemoteFile handles files located remotely in a node
type RemoteFile struct {
	node     *Node
	fullPath string
	statData map[string]string
	content  string
}

// NewRemoteFile creates a new instance of RemoteFile
func NewRemoteFile(node *Node, fullPath string) *RemoteFile {
	return &RemoteFile{node: node, fullPath: fullPath}
}

// Fetch gets the file information from the node
func (rf *RemoteFile) Fetch() error {
	err := rf.Stat()
	if err != nil {
		return err
	}

	if !rf.IsDirectory() {
		err = rf.fetchTextContent()
	} else {
		logger.Debugf("Remote file %s is a directory. Skipping fetch content", rf.GetName())
	}

	return err
}

// Stat get file properties only
func (rf *RemoteFile) Stat() error {

	stdout, stderr, err := rf.node.DebugNodeWithChrootStd("stat", statFormat, rf.fullPath)
	if err != nil {
		logger.Errorf("Could not fetch the remote file %s. Stderr: %s", rf.fullPath, stderr)
		return err
	}

	return rf.digest(stdout)
}

func (rf *RemoteFile) fetchTextContent() error {
	output, err := rf.node.DebugNodeWithChroot("sh", "-c", fmt.Sprintf("echo -n '%s'; cat %s; echo '%s'", startCat, rf.fullPath, endCat))
	if err != nil {
		return err
	}

	// Split by first occurrence of startCat and last occurrence of endCat
	tmpcontent := strings.SplitN(output, startCat, 2)[1]
	// take into account that "cat" introduces a newline at the end
	lastIndex := strings.LastIndex(tmpcontent, endCat)
	rf.content = tmpcontent[:lastIndex]

	logger.Debugf("remote file %s content is:\n%s", rf.fullPath, rf.content)

	return nil
}

// PushNewOwner modifies the remote file's owners, setting the provided new owner using `sudo chown newowner`
func (rf *RemoteFile) PushNewOwner(newowner string) error {
	logger.Infof("Push owner %s to file %s in node %s", newowner, rf.fullPath, rf.node.GetName())
	_, err := rf.node.DebugNodeWithChroot("sh", "-c", fmt.Sprintf("sudo chown %s %s", newowner, rf.fullPath))
	if err != nil {
		logger.Errorf("Error: %s", err)
		return err
	}
	return nil
}

// PushNewPermissions modifies the remote file's permissions, setting the provided new permissions using `chmod newperm`
func (rf *RemoteFile) PushNewPermissions(newperm string) error {
	logger.Infof("Push permissions %s to file %s in node %s", newperm, rf.fullPath, rf.node.GetName())
	_, err := rf.node.DebugNodeWithChroot("sh", "-c", fmt.Sprintf("chmod %s %s", newperm, rf.fullPath))
	if err != nil {
		logger.Errorf("Error: %s", err)
		return err
	}
	return nil
}

// PushNewTextContent modifies the remote file's content
func (rf *RemoteFile) PushNewTextContent(newTextContent string) error {
	return rf.PushNewContent([]byte(newTextContent))
}

// PushNewContent modifies the remote file's content
func (rf *RemoteFile) PushNewContent(newContent []byte) error {
	exists, err := rf.ExistsSafe()
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Node %s. file %s. Refuse to modify the content of a file that does not exist", rf.node.GetName(), rf.fullPath)
	}

	tmpFile := filepath.Join(e2e.TestContext.OutputDir, fmt.Sprintf("fetch-%s", exutil.GetRandomString()))

	if err := os.WriteFile(tmpFile, newContent, 0o600); err != nil {
		logger.Errorf("Could not read the fetched content from tmp file  %s. Error: %s", tmpFile, err)
		return err
	}

	if err := rf.node.CopyFromLocal(tmpFile, rf.fullPath); err != nil {
		logger.Errorf("Could not push the new content of the remote file %s. Error: %s", rf.fullPath, err)
		return err
	}
	return nil
}

// GetTextContent return the content of the text file. If the file contains binary data this method cannot be used to retrieve the file's content
func (rf *RemoteFile) GetTextContent() string {
	return rf.content
}

// Diggest the output of the 'stat' command using the 'statFormat' format. And stores the parsed information inside the 'statData' map
// To be able to understand the 'statFormat' format, it uses the 'statParser' regex. Both, 'statFormat' and 'statParser', must be coherent
func (rf *RemoteFile) digest(statOutput string) error {

	logger.Debugf("stat output: %v", statOutput)
	rf.statData = make(map[string]string)
	logger.Debugf("parsing with string: %s", statParser)
	re := regexp.MustCompile(statParser)
	match := re.FindStringSubmatch(statOutput)
	logger.Debugf("matched stat info: %v", match)
	// check whether matched string found
	if len(match) == 0 {
		return fmt.Errorf("no file stat info matched")
	}

	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" {
			rf.statData[name] = match[i]
			logger.Debugf("Parsing %s = %s", name, rf.statData[name])
		}
	}

	return nil
}

// GetUIDName returns the UID of the file in name format
func (rf *RemoteFile) GetUIDName() string {
	return rf.statData["uidname"]
}

// GetGIDName returns the GID of the file in name format
func (rf *RemoteFile) GetGIDName() string {
	return rf.statData["gidname"]
}

// GetSelinux returns the file's selinux info
func (rf *RemoteFile) GetSelinux() string {
	return rf.statData["selinux"]
}

// GetName returns the name of the file
func (rf *RemoteFile) GetName() string {
	return rf.statData["name"]
}

// GetKind returns a human readable description of the file (regular file, regular empty file, directory, symbolyc link..)
func (rf *RemoteFile) GetKind() string {
	return rf.statData["kind"]
}

// GetNpermissions deprecated. Use GetOctalPermissions instead
func (rf *RemoteFile) GetNpermissions() string {
	return rf.statData["octalperm"]
}

// GetOctalPermissions returns permissions in numeric format (0664). Always 4 digits
func (rf *RemoteFile) GetOctalPermissions() string {
	return rf.statData["octalperm"]
}

// GetUIDNumber the file's UID number
func (rf *RemoteFile) GetUIDNumber() string {
	return rf.statData["uidnumber"]
}

// GetSymLink returns the symlink description of the file (i.e: "'/tmp/actualfile'" if no link, or "'/tmp/linkfile' -> '/tmp/actualfile'" if link.
func (rf *RemoteFile) GetSymLink() string {
	return rf.statData["symlink"]
}

// GetSize returns the size of the file in bytes
func (rf *RemoteFile) GetSize() string {
	return rf.statData["size"]
}

// GetRWXPermissions returns the file permissions in ugo rwx format
func (rf *RemoteFile) GetRWXPermissions() string {
	return rf.statData["rwxperm"]
}

// GetGIDNumber returns the file's GID number
func (rf *RemoteFile) GetGIDNumber() string {
	return rf.statData["gidnumber"]
}

// GetLinks returns the number of hard links
func (rf *RemoteFile) GetLinks() string {
	return rf.statData["links"]
}

// IsDirectory returns true if it is a directory
func (rf *RemoteFile) IsDirectory() bool {
	return rf.GetRWXPermissions()[0] == 'd' && strings.Contains(rf.GetKind(), "directory")
}

// GetTextContentAsList returns the content of the text file as a list of strings, one string per line
func (rf *RemoteFile) GetTextContentAsList() []string {
	return strings.Split(rf.GetTextContent(), "\n")
}

// GetFilteredTextContent returns the filetered remote file's text content as a list of strings, one string per line matching the regexp.
func (rf *RemoteFile) GetFilteredTextContent(regex string) ([]string, error) {
	content := rf.GetTextContentAsList()

	filteredContent := []string{}
	for _, line := range content {
		match, err := regexp.MatchString(regex, line)
		if err != nil {
			logger.Errorf("Error filtering content lines. Error: %s", err)
			return nil, err
		}

		if match {
			filteredContent = append(filteredContent, line)
		}
	}

	return filteredContent, nil
}

// GetFullPath returns the full path of the remote file
func (rf *RemoteFile) GetFullPath() string {
	return rf.fullPath
}

// ExistsSafe check if a file exists. Returns an error in case of not being able to know if the file exists or not
func (rf *RemoteFile) ExistsSafe() (bool, error) {
	output, err := rf.node.DebugNodeWithChroot("stat", rf.fullPath)
	logger.Infof("\n%s", output)

	if strings.Contains(output, "No such file or directory") {
		return false, nil
	}

	if err == nil {
		return true, nil
	}

	return false, err
}

// Exists is like ExistsSafe but if any error happens the file is considered to be non existent
func (rf *RemoteFile) Exists() bool {
	exists, err := rf.ExistsSafe()
	return exists && (err == nil)
}

// Rm removes the remote file from the node
func (rf *RemoteFile) Rm(args ...string) error {
	logger.Infof("Removing file %s from node %s", rf.fullPath, rf.node.GetName())
	cmd := []string{"rm"}

	if len(args) > 0 {
		cmd = append(cmd, args...)
	}

	cmd = append(cmd, rf.fullPath)
	output, err := rf.node.DebugNodeWithChroot(cmd...)
	logger.Infof("%s", output)

	return err
}

// Create this file in the node with the given content and permissions. It will be created with root/root user/grou since there are no methods to modify the user and the group.
// In the future we can modify this method to accept the user and the group as parameters
func (rf *RemoteFile) Create(content []byte, perm os.FileMode) error {
	tmpFile := filepath.Join(e2e.TestContext.OutputDir, fmt.Sprintf("fetch-%s", exutil.GetRandomString()))

	if err := os.WriteFile(tmpFile, content, perm); err != nil {
		logger.Errorf("Could not read the content from tmp file  %s. Error: %s", tmpFile, err)
		return err
	}

	if err := rf.node.CopyFromLocal(tmpFile, rf.fullPath); err != nil {
		logger.Errorf("Could not copy the file %s from local to path %s in node %s. Error: %s", tmpFile, rf.fullPath, rf.node.GetName(), err)
		return err
	}

	// Actually, the "oc adm copy-to-node" file will ignore the file permissions. Hence, we need to manually set the required permissions
	if err := rf.PushNewPermissions(fmt.Sprintf("%#o", perm)); err != nil {
		logger.Infof("Could not set the right permissions in remote file %s in node %s. Error: %s", rf.fullPath, rf.node.GetName(), err)
	}

	return nil
}

// PrintDebug prints a log message with the stats and the content of the remote file
func (rf *RemoteFile) PrintDebugInfo() error {
	logger.Infof("Remote file %s in node %s", rf.fullPath, rf.node.GetName())

	stdout, _, err := rf.node.DebugNodeWithChrootStd("stat", statFormat, rf.fullPath)
	if err != nil {
		return err
	}

	logger.Infof("Stat: \n%s", stdout)

	stdout, _, err = rf.node.DebugNodeWithChrootStd("cat", rf.fullPath)
	if err != nil {
		return err
	}

	logger.Infof("Content: \n%s", stdout)

	return nil
}

// Read is the same function as Fetch but it returns itself as a value, so that it can be used in gomega assertions easily
func (rf *RemoteFile) Read() (RemoteFile, error) {
	return *rf, rf.Fetch()
}

// String implements the stringer interface
func (rf *RemoteFile) String() string {
	return fmt.Sprintf("file %s in node %s", rf.fullPath, rf.node.GetName())
}

// HasInfo returns true if the remote file has been already fetched/read
func (rf *RemoteFile) HasInfo() bool {
	return len(rf.statData) > 0
}
