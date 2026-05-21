package util

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type synchronizedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func newSynchronizedBuffer() *synchronizedBuffer {
	return &synchronizedBuffer{}
}

func (sb *synchronizedBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *synchronizedBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

// SSHClient handles SSH connections to remote machines
type SSHClient struct {
	User       string
	Host       string
	Port       int
	PrivateKey string
}

func (sshClient *SSHClient) getConfig() (*ssh.ClientConfig, error) {
	pemBytes, err := os.ReadFile(sshClient.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key %s: %v", sshClient.PrivateKey, err)
	}
	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %v", err)
	}
	return &ssh.ClientConfig{
		User:            sshClient.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         150 * time.Second,
	}, nil
}

// RunOutput runs cmd on the remote host and returns its combined standard output and standard error.
func (sshClient *SSHClient) RunOutput(cmd string) (string, error) {
	config, err := sshClient.getConfig()
	if err != nil {
		return "", fmt.Errorf("failed to get SSH config: %v", err)
	}

	address := net.JoinHostPort(sshClient.Host, strconv.Itoa(sshClient.Port))
	connection, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return "", fmt.Errorf("failed to dial %s:%d: %v", sshClient.Host, sshClient.Port, err)
	}
	defer connection.Close()

	session, err := connection.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	buf := newSynchronizedBuffer()
	session.Stdout = buf
	session.Stderr = buf

	if err := session.Run(cmd); err != nil {
		return "", fmt.Errorf("failed to run cmd '%s': %v\n%s", cmd, err, buf.String())
	}
	return buf.String(), nil
}

// GetSSHPrivateKey returns the SSH private key path from the SSH_CLOUD_PRIV_KEY environment variable
func GetSSHPrivateKey() string {
	return os.Getenv("SSH_CLOUD_PRIV_KEY")
}
