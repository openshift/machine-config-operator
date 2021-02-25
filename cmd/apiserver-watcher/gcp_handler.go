package main

import (
	"fmt"
	"net"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
)

// Update iptables rules based on google cloud load balancer VIPS
//
// This is needed because the GCP L3 load balancer doesn't actually do DNAT;
// the destination IP address is still the VIP. Normally, there is an agent that
// adds the vip to the local routing table, tricking the kernel in to thinking
// it's a local IP and allowing processes doing an accept(0.0.0.0) to receive
// the packets. Clever.
//
// We don't do that. Instead, we DNAT with conntrack. This is so we don't break
// existing connections when the vip is removed. This is useful for draining
// connections - take ourselves out of the vip, but service existing conns.
//
//  The apiserver watcher respect current forwarded IPs to the host and ensures
//  that the traffic is correctly redirected, but only takes responsibility of the
//  apiserver vip passed as a parameter, deleting the local traffic rules on failure.
//  The host process checks /run/cloud-routes/ for files $VIP.up and $VIP.down, the
//  apiserver-watcher sets the apiserver VIP.down file on failure to avoid receiving
//  traffic directed to the apiserver when it is not ready.
//
// Example rules using vip 10.0.0.2 for the apiserver. Note that the host in this case
// has additional forwarded IPs 35.184.123.49 and 35.188.173.243
//
// -A PREROUTING -m comment --comment "gcp LB vip DNAT" -j gcp-vips
// -A OUTPUT -m comment --comment "gcp LB vip DNAT for local clients" -j gcp-vips
// -A gcp-vips -d 10.0.0.2/32 -j REDIRECT
// -A gcp-vips -d 35.184.123.49/32 -j REDIRECT
// -A gcp-vips -d 35.188.173.243/32 -j REDIRECT
// -A gcp-vips-local -d 35.184.123.49/32 -j REDIRECT
// -A gcp-vips-local -d 35.188.173.243/32 -j REDIRECT
// -A gcp-vips-local -d 10.0.0.2/32 -j REDIRECT

const (
	// name of nat chain for iptables masquerade rules
	gcpMasqChainName      = "gcp-vips"
	gcpLocalMasqChainName = "gcp-vips-local"
)

type gcpHandler struct {
	vip    string
	ipt    *iptables.IPTables
	stopCh chan struct{}
}

var _ handler = &gcpHandler{}

func newGCPHandler(vip string) (handler, error) {
	// FIXME add IPv6 once GCP supports it
	ipt, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(iptablesTimeout),
	)
	if err != nil {
		return nil, err
	}

	return &gcpHandler{
		vip:    vip,
		ipt:    ipt,
		stopCh: make(chan struct{}),
	}, nil
}

// onFailure: stop syncing iptables rules but don't flush all of them
// keep the ones that are still being forwarded to this host.
func (gcp *gcpHandler) onFailure() error {
	// stop syncing iptables rules
	select {
	case gcp.stopCh <- struct{}{}:
		glog.V(4).Info("stopping iptables sync loop")
	default:
		glog.V(4).Info("no sync iptables running")
	}
	// wait for iptables sync loop to finish
	time.Sleep(iptablesTimeout * time.Second)

	// indicate the host script that handles GCP LB vips
	// to not forward traffic to the apiserver VIP
	glog.Infof("apiserver down: create downfile: %s.down", gcp.vip)
	if err := writeVipStateFile(gcp.vip, "down"); err != nil {
		return fmt.Errorf("error writing state file for vip DOWN: %v", err)
	}

	glog.Info("apiserver down: deleting iptables apiserver rules for local traffic")
	return gcp.ipt.DeleteIfExists("nat", gcpLocalMasqChainName, "--dst", gcp.vip, "-j", "REDIRECT")
}

// onSuccess: either start routes service, or remove down file
func (gcp *gcpHandler) onSuccess() error {
	// indicate the host script that handles GCP LB vips that
	// it should not delete the apiserver VIP
	glog.Infof("apiserver down: deleting downfile: %s.down", gcp.vip)
	if err := removeVipStateFile(gcp.vip, "down"); err != nil {
		return fmt.Errorf("error deleting state file for vip UP: %v", err)
	}
	// try to install the iptables rules for the VIPs
	if err := gcp.syncRulesOnce(); err != nil {
		return fmt.Errorf("error syncing iptables rules: %v", err)
	}

	ip := net.ParseIP(gcp.vip)
	// bz1930457 delete stale conntrack entries to the apiserver
	filter := &netlink.ConntrackFilter{}
	filter.AddIP(netlink.ConntrackNatSrcIP, ip)
	if _, err := netlink.ConntrackDeleteFilter(netlink.ConntrackTable, netlink.FAMILY_V4, filter); err != nil {
		glog.V(4).Infof("Error deleting conntrack entries for %s", gcp.vip)
	}

	go gcp.syncRulesUntil()
	return nil

}

// https://github.com/openshift/installer/blob/master/data/data/gcp/network/lb-private.tf
// healthy_threshold   = 3
// unhealthy_threshold = 3
// check_interval_sec  = 2
// timeout_sec         = 2

func (gcp *gcpHandler) successThreshold() int {
	return 1
}

func (gcp *gcpHandler) failureThreshold() int {
	// LB = 6 seconds, plus 10 seconds for propagation
	return 8
}

// syncRulesOnce syncs ip masquerade rules
func (gcp *gcpHandler) syncRulesOnce() error {
	gcpChains := []string{gcpMasqChainName, gcpLocalMasqChainName}
	for _, gcpChan := range gcpChains {
		exists, err := gcp.ipt.ChainExists("nat", gcpChan)
		if err != nil {
			return err
		}
		if !exists {
			if err := gcp.ipt.NewChain("nat", gcpChan); err != nil {
				return err
			}
		}
	}

	// traffic coming from outside is redirected to the local apiserver
	if err := gcp.ipt.AppendUnique("nat", "PREROUTING", "-m", "comment", "--comment", "gcp LB vip DNAT", "-j", gcpMasqChainName); err != nil {
		return err
	}
	// traffic coming from inside is redirected to the local apiserver
	if err := gcp.ipt.AppendUnique("nat", "OUTPUT", "-m", "comment", "--comment", "gcp LB vip DNAT for local clients", "-j", gcpLocalMasqChainName); err != nil {
		return err
	}
	// Need this so that existing flows (with an entry in conntrack) continue,
	// even if the iptables rule is removed
	if err := gcp.ipt.AppendUnique("filter", "INPUT", "-m", "comment", "--comment", "gcp LB vip existing",
		"-m", "addrtype", "!", "--dst-type", "LOCAL", "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := gcp.ipt.AppendUnique("filter", "OUTPUT", "-m", "comment", "--comment", "gcp LB vip existing",
		"-m", "addrtype", "!", "--dst-type", "LOCAL", "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"); err != nil {
		return err
	}

	// Add LoadBalancer VIPs rule
	glog.Infof("Adding iptables VIP rules for: %v", gcp.vip)
	if err := gcp.ipt.AppendUnique("nat", gcpMasqChainName, "--dst", gcp.vip, "-j", "REDIRECT"); err != nil {
		return err
	}
	if err := gcp.ipt.AppendUnique("nat", gcpLocalMasqChainName, "--dst", gcp.vip, "-j", "REDIRECT"); err != nil {
		return err
	}

	return nil
}

// syncRulesUntil syncs the iptables rules until it receives the stop signal
func (gcp *gcpHandler) syncRulesUntil() {
	ticker := time.NewTicker(30 * time.Second)
	for {
		select {
		case <-ticker.C:
			gcp.syncRulesOnce()
		case <-gcp.stopCh:
			return
		}
	}
}
