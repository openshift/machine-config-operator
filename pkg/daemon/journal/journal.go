package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/golang/glog"
	"github.com/pkg/errors"
)

type Entry struct {
	Message string `json:"MESSAGE"`
	Cursor  string `json:"__CURSOR"`
}

func Query(args ...string) ([]Entry, error) {
	baseArgs := []string{"-b", "-o", "json"}
	// The reason we use journalctl as a subprocess is because currently the MCD
	// container is RHEL7, but RHCOS is RHEL8, and we chroot into the host.
	cmd := exec.Command("journalctl", append(baseArgs, args...)...)
	initialStdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(initialStdout))
	// First, gather all matched messages.
	retMessages := []Entry{}
	for scanner.Scan() {
		var msg Entry
		err := json.Unmarshal(scanner.Bytes(), &msg)
		if err != nil {
			return nil, err
		}
		retMessages = append(retMessages, msg)
	}

	return retMessages, nil
}

// QueryAndStream runs journalctl as a subprocess, parsing its output and returning a stream of entries.
// The "-b" argument is always passed which limits to entries from the current boot.
func QueryAndStream(stopCh <-chan struct{}, errCh chan<- error, args ...string) ([]Entry, <-chan Entry, error) {
	retMessages, err := Query(args...)
	if err != nil {
		return nil, nil, err
	}
	// The next invocation of journalctl will watch for further messages.
	baseArgs := []string{"-b", "-f", "-o", "json"}
	// If we found any matched messages, ensure we don't return duplicates
	// by asking for messages after the cursor from the last message.
	if len(retMessages) > 0 {
		lastCursor := retMessages[len(retMessages)-1].Cursor
		baseArgs = append(baseArgs, fmt.Sprintf("--after-cursor=%s", lastCursor))
	}
	watchCmd := exec.Command("journalctl", append(baseArgs, args...)...)
	stdout, err := watchCmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := watchCmd.Start(); err != nil {
		return nil, nil, err
	}
	scanner := bufio.NewScanner(stdout)
	retChannel := make(chan Entry)
	go func() {
		for {
			select {
			case <-stopCh:
				glog.Info("got stop request, killing journal monitor process")
				watchCmd.Process.Kill()
				watchCmd.Wait()
				break
			}
		}
	}()
	go func() {
		for scanner.Scan() {
			var msg Entry
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				err := errors.Wrapf(err, "parsing journal")
				glog.Errorf("journal watcher: %v", err)
				errCh <- err
				break
			}
			retChannel <- msg
		}
		if err := scanner.Err(); err != nil {
			err := errors.Wrapf(err, "reading journal")
			glog.Errorf("journal watcher: %v", err)
			errCh <- err
		}
	}()
	return retMessages, retChannel, nil
}
