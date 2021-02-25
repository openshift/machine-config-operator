package main

import (
	"fmt"
	"net"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
)

// Prevent hairpin traffic when the apiserver is up

// As per the Azure documentation (https://docs.microsoft.com/en-us/azure/load-balancer/concepts//limitations),
// if a backend is load-balanced to itself, then the traffic will be dropped.
//
// This is because the L3LB does DNAT, so while the outgoing packet has a destination
// IP of the VIP, the incoming load-balanced packet has a destination IP of the
// host. That means that it "sees" a syn with the source and destination
// IPs of itself, and duly replies wit a syn-ack back to itself. However, the client
// socket expects a syn-ack with a source IP of the VIP, so it drops the packet.
//
// The solution is to redirect traffic destined to the lb vip back to ourselves.
//
// Example rules using vip 10.0.0.1:
//
// -A PREROUTING -m comment --comment "azure LB vip overriding for pods" -j azure-vips
// -A OUTPUT -m comment --comment "azure LB vip overriding for local clients" -j azure-vips
// -A azure-vips --dst 10.0.0.1 -j REDIRECT

type azureHandler struct {
	vip    string
	ipt    *iptables.IPTables
	stopCh chan struct{}
}

var _ handler = &azureHandler{}

func newAzureHandler(vip string, isIPv6 bool) (handler, error) {
	proto := iptables.ProtocolIPv4
	if isIPv6 {
		proto = iptables.ProtocolIPv6
	}

	ipt, err := iptables.New(
		iptables.IPFamily(proto),
		iptables.Timeout(iptablesTimeout),
	)
	if err != nil {
		return nil, err
	}

	return &azureHandler{
		vip:    vip,
		ipt:    ipt,
		stopCh: make(chan struct{}),
	}, nil
}

// onFailure: either stop the routes service, or write downfile
func (az *azureHandler) onFailure() error {
	select {
	case az.stopCh <- struct{}{}:
		glog.V(4).Info("stopping iptables sync loop")
	default:
		glog.V(4).Info("no sync iptables running")
	}
	// wait for iptables sync loop to finish
	time.Sleep(iptablesTimeout * time.Second)
	return az.deleteRules()
}

// onSuccess: install iptables rules to REDIRECT LB traffic to the apiserver
func (az *azureHandler) onSuccess() error {
	if err := az.syncRulesOnce(); err != nil {
		return err
	}
	// bz1930457 delete stale conntrack entries to the apiserver
	ip := net.ParseIP(az.vip)
	var family netlink.InetFamily
	family = netlink.FAMILY_V4
	if az.ipt.Proto() == iptables.ProtocolIPv6 {
		family = netlink.FAMILY_V6
	}
	filter := &netlink.ConntrackFilter{}
	filter.AddIP(netlink.ConntrackNatSrcIP, ip)
	if _, err := netlink.ConntrackDeleteFilter(netlink.ConntrackTable, family, filter); err != nil {
		glog.V(4).Infof("Error deleting conntrack entries for %s", ip.String())
	}

	go az.syncRulesUntil()
	return nil

}

// https://github.com/openshift/installer/blob/master/data/data/azure/vnet/internal-lb.tf
// interval_in_seconds = 5
// number_of_probes    = 2
func (az *azureHandler) successThreshold() int {
	return 1
}

func (az *azureHandler) failureThreshold() int {
	// LB = 6 seconds, plus 10 seconds for propagation
	return 8
}

// name of nat chain for iptables masquerade rules
const azureMasqChainName = "azure-vips"

// deleteRules deletes the iptables rules added for the VIPs
func (az *azureHandler) deleteRules() error {
	glog.Infof("Deleting iptables VIP rules for: %v", az.vip)
	return az.ipt.ClearChain("nat", azureMasqChainName)
}

// syncRulesOnce syncs iptables rules
// https://bugzilla.redhat.com/show_bug.cgi?id=1936979
func (az *azureHandler) syncRulesOnce() error {
	glog.Infof("Adding iptables VIP rules for: %v", az.vip)
	// make sure our custom chain for non-masquerade exists
	exists, err := az.ipt.ChainExists("nat", azureMasqChainName)
	if err != nil {
		return fmt.Errorf("failed to list chains: %v", err)
	}
	if !exists {
		if err := az.ipt.NewChain("nat", azureMasqChainName); err != nil {
			return err
		}
	}

	// Add VIPs rules
	if err := az.ipt.AppendUnique("nat", "PREROUTING", "-m", "comment", "--comment", "azure LB vip overriding for pods", "-j", azureMasqChainName); err != nil {
		return err
	}
	if err := az.ipt.AppendUnique("nat", "OUTPUT", "-m", "comment", "--comment", "azure LB vip overriding for local clients", "-j", azureMasqChainName); err != nil {
		return err
	}
	// Need this so that existing flows (with an entry in conntrack) continue,
	// even if the iptables rule is removed
	if err := az.ipt.AppendUnique("filter", "FORWARD", "-m", "comment", "--comment", "azure LB vip existing",
		"-m", "addrtype", "!", "--dst-type", "LOCAL", "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"); err != nil {
		return err
	}

	if err := az.ipt.AppendUnique("filter", "OUTPUT", "-m", "comment", "--comment", "azure LB vip existing",
		"-m", "addrtype", "!", "--dst-type", "LOCAL", "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"); err != nil {
		return err
	}

	// Add VIP rules
	return az.ipt.AppendUnique("nat", azureMasqChainName, "--dst", az.vip, "-j", "REDIRECT")
}

// syncRulesUntil syncs the iptables rules until it receives the stop signal
func (az *azureHandler) syncRulesUntil() {
	ticker := time.NewTicker(30 * time.Second)
	for {
		select {
		case <-ticker.C:
			az.syncRulesOnce()
		case <-az.stopCh:
			return
		}
	}
}
