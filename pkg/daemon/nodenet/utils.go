package nodenet

import (
	"net"

	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// AddressFilter is a function type to filter addresses
type AddressFilter func(netlink.Addr) bool

// RouteFilter is a function type to filter routes
type RouteFilter func(netlink.Route) bool

func getAddrs() (addrMap map[netlink.Link][]netlink.Addr, err error) {
	nlHandle, err := netlink.NewHandle()
	defer nlHandle.Delete()
	if err != nil {
		return nil, err
	}

	links, err := nlHandle.LinkList()
	if err != nil {
		return nil, err
	}

	addrMap = make(map[netlink.Link][]netlink.Addr)
	for _, link := range links {
		addresses, err := nlHandle.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			if _, ok := addrMap[link]; ok {
				addrMap[link] = append(addrMap[link], address)
			} else {
				addrMap[link] = []netlink.Addr{address}
			}
		}
	}
	glog.V(2).Infof("retrieved Address map %+v", addrMap)
	return addrMap, nil
}

func getRouteMap() (routeMap map[int][]netlink.Route, err error) {
	nlHandle, err := netlink.NewHandle()
	defer nlHandle.Delete()
	if err != nil {
		return nil, err
	}

	routes, err := nlHandle.RouteList(nil, netlink.FAMILY_V6)
	if err != nil {
		return nil, err
	}

	routeMap = make(map[int][]netlink.Route)
	for _, route := range routes {
		if route.Protocol != unix.RTPROT_RA {
			glog.V(4).Infof("Ignoring route non Router advertisement route %+v", route)
			continue
		}
		if _, ok := routeMap[route.LinkIndex]; ok {
			routeMap[route.LinkIndex] = append(routeMap[route.LinkIndex], route)
		} else {
			routeMap[route.LinkIndex] = []netlink.Route{route}
		}
	}

	glog.V(2).Infof("Retrieved IPv6 route map %+v", routeMap)

	return routeMap, nil
}

// NonDeprecatedAddress returns true if the address is IPv6 and has a preferred lifetime of 0
func NonDeprecatedAddress(address netlink.Addr) bool {
	return !(net.IPv6len == len(address.IP) && address.PreferedLft == 0)
}

// NonDefaultRoute returns whether the passed Route is the default
func NonDefaultRoute(route netlink.Route) bool {
	return route.Dst != nil
}

// AddressesRouting takes a slice of Virtual IPs and returns a slice of configured addresses in the current network namespace that directly route to those vips. You can optionally pass an AddressFilter and/or RouteFilter to further filter down which addresses are considered
func AddressesRouting(vips []net.IP, af AddressFilter, rf RouteFilter) ([]net.IP, error) {
	addrMap, err := getAddrs()
	if err != nil {
		return nil, err
	}

	var routeMap map[int][]netlink.Route
	matches := make([]net.IP, 0)
	for link, addresses := range addrMap {
		for _, address := range addresses {
			maskPrefix, maskBits := address.Mask.Size()
			if !af(address) {
				continue
			}
			if net.IPv6len == len(address.IP) && maskPrefix == maskBits {
				if routeMap == nil {
					routeMap, err = getRouteMap()
					if err != nil {
						panic(err)
					}
				}
				if routes, ok := routeMap[link.Attrs().Index]; ok {
					for _, route := range routes {
						if !rf(route) {
							continue
						}
						routePrefix, _ := route.Dst.Mask.Size()
						glog.V(4).Infof("Checking route %+v (mask %s) for address %+v", route, route.Dst.Mask, address)
						if routePrefix != 0 {
							containmentNet := net.IPNet{IP: address.IP, Mask: route.Dst.Mask}
							for _, vip := range vips {
								glog.V(3).Infof("Checking whether address %s with route %s contains VIP %s", address, route, vip)
								if containmentNet.Contains(vip) {
									glog.V(2).Infof("Address %s with route %s contains VIP %s", address, route, vip)
									matches = append(matches, address.IP)
								}
							}
						}
					}
				}
			} else {
				for _, vip := range vips {
					glog.V(3).Infof("Checking whether address %s contains VIP %s", address, vip)
					if address.Contains(vip) {
						glog.V(2).Infof("Address %s contains VIP %s", address, vip)
						matches = append(matches, address.IP)
					}
				}
			}
		}

	}
	return matches, nil
}
