package daemon

// This file contains code to talk to the local etcd instance
// on control plane nodes, used as part of coordinating
// updates:
// https://github.com/openshift/machine-config-operator/issues/1897

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
	"go.etcd.io/etcd/clientv3"
	"go.etcd.io/etcd/pkg/transport"
	"google.golang.org/grpc"
)

const (
	// pollEtcdSecs is how often we ask etcd who the leader is
	pollEtcdSecs = 10
	endpoint     = "https://localhost:2379"
	podDir       = "/etc/kubernetes/static-pod-resources"
)

// Yes this is a brutal hack that should be replaced by something
// discussed in https://github.com/openshift/api/pull/694
func hackilyFindAPIServerCerts() (string, error) {
	ents, err := ioutil.ReadDir(podDir)
	if err != nil {
		return "", err
	}
	for _, ent := range ents {
		if !strings.HasPrefix(ent.Name(), "kube-apiserver-pod") {
			continue
		}
		path := filepath.Join(podDir, ent.Name())
		keyPath := filepath.Join(path, "secrets/etcd-client/tls.key")
		_, err := os.Stat(keyPath)
		if err != nil {
			continue
		}
		return path, nil
	}
	return "", nil
}

func initEtcdClient() (*clientv3.Client, error) {
	path, err := hackilyFindAPIServerCerts()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("Couldn't find kube-apiserver secrets yet")
	}

	// TODO - this is obviously an awful hack, but I don't think we want to bind in the etcd certs for
	// all MCDs.  We could do another daemonset that only runs on the controlplane...
	tlsInfo := transport.TLSInfo{
		CertFile:      filepath.Join(path, "secrets/etcd-client/tls.crt"),
		KeyFile:       filepath.Join(path, "secrets/etcd-client/tls.key"),
		TrustedCAFile: filepath.Join(path, "configmaps/etcd-serving-ca/ca-bundle.crt"),
	}
	tlsConfig, err := tlsInfo.ClientConfig()
	if err != nil {
		return nil, err
	}
	dialOptions := []grpc.DialOption{
		grpc.WithBlock(), // block until the underlying connection is up
	}
	cfg := clientv3.Config{
		DialOptions: dialOptions,
		Endpoints:   []string{endpoint},
		DialTimeout: 15 * time.Second,
		TLS:         tlsConfig,
	}

	return clientv3.New(cfg)
}

// watchCurrentEtcdLeader creates a channel that signals changes in the etcd leader
func watchCurrentEtcdLeader(stopCh <-chan struct{}) <-chan bool {
	context, cancel := context.WithCancel(clientv3.WithRequireLeader(context.Background()))

	r := make(chan bool)
	go func() {
		var lastLeader *bool
		var c *clientv3.Client
		for {
			var err error
			c, err = initEtcdClient()
			if err != nil {
				glog.Warningf("Failed to init etcd client: %v", err)
				time.Sleep(pollEtcdSecs * time.Second)
			} else {
				break
			}
		}
		for {
			resp, err := c.Status(context, endpoint)
			if err != nil {
				glog.Errorf("Failed to get etcd Status: %v", err)
				if lastLeader != nil {
					lastLeader = nil
					r <- false
				}
			} else {
				leader := resp.Header.MemberId == resp.Leader
				if lastLeader == nil || *lastLeader != leader {
					lastLeader = &leader
					r <- leader
				}
			}
			select {
			case <-stopCh:
				cancel()
				return
			case <-time.After(pollEtcdSecs * time.Second):
			}
		}
	}()
	return r
}

// This will be replaced by https://github.com/openshift/cluster-etcd-operator/pull/418
func ioniceEtcd(stopCh <-chan struct{}) {
	go func() {
		for {
			pids, err := exec.Command("pgrep", "etcd").CombinedOutput()
			if err == nil && len(pids) > 0 {
				pid, err := strconv.ParseInt(string(pids), 10, 64)
				if err != nil {
					glog.Warningf("Failed to parse pgrep etcd: %v", err)
				} else {
					err = exec.Command("ionice", "-c2", "-n0", "-p", fmt.Sprintf("%d", pid)).Run()
					if err != nil {
						glog.Warningf("Failed to parse ionice etcd: %v", err)
					}
					glog.Info("Reniced etcd")
					return
				}
			}

			select {
			case <-stopCh:
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
}
