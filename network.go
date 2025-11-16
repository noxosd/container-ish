package main

import (
	"fmt"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func deleteNftableTable() error {
	c := &nftables.Conn{}

	table := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   "container-nat",
	}
	c.DelTable(table)

	err := c.Flush()
	if err != nil {
		return fmt.Errorf("failed to flush nftables rules: %w", err)
	}
	return nil
}

func addNetworkInterfaces(pid int) error {
	// variable names are incorrect
	containerVeth := "container-host"
	hostVeth := "container-veth"

	vethLinkAttrs := netlink.NewLinkAttrs()
	vethLinkAttrs.Name = hostVeth

	veth := &netlink.Veth{
		LinkAttrs: vethLinkAttrs,
		PeerName:  containerVeth,
	}

	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("failed to create veth pair: %w", err)
	}

	if err := netlink.LinkSetNsPid(veth, pid); err != nil {
		return fmt.Errorf("failed to set veth namespace: %w", err)
	}

	ns, err := netns.GetFromPid(pid)
	if err != nil {
		return fmt.Errorf("failed to get netns handle: %w", err)
	}
	defer ns.Close()

	hostAddr, _ := netlink.ParseAddr("10.0.0.1/24")
	containerAddr, _ := netlink.ParseAddr("10.0.0.2/24")

	l, _ := netlink.LinkByName(containerVeth)
	netlink.AddrAdd(l, hostAddr)
	netlink.LinkSetUp(l)

	// Setting up container side of the veth
	handle, err := netlink.NewHandleAt(ns)
	if err != nil {
		return fmt.Errorf("failed to get netlink handle in container netns: %w", err)
	}
	defer handle.Close()

	link, err := handle.LinkByName(hostVeth)
	if err != nil {
		return fmt.Errorf("failed to get container veth link: %w", err)
	}
	if err := handle.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set container veth up: %w", err)
	}

	handle.AddrAdd(link, containerAddr)

	_, zeroSubnet, _ := net.ParseCIDR("0.0.0.0/0")
	ip := net.ParseIP("10.0.0.1")
	route := &netlink.Route{
		Dst: zeroSubnet,
		Gw:  ip,
	}

	if err := handle.RouteAdd(route); err != nil {
		return fmt.Errorf("failed to add default route to the container: %w", err)
	}

	c := &nftables.Conn{}

	table := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   "container-nat",
	}

	table = c.AddTable(table)

	c.AddTable(table)
	postrouteChain := c.AddChain(&nftables.Chain{
		Name:     "postrouting",
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPostrouting, // postrouting hook
		Priority: nftables.ChainPriorityNATSource,
	})

	c.AddRule(&nftables.Rule{
		Table: table,
		Chain: postrouteChain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     []byte("wlp4s0\x00"),
			},
			&expr.Payload{
				DestRegister: 2,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       12,
				Len:          4,
			},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 2,
				Data:     net.ParseIP("10.0.0.2").To4(),
			},
			// masq
			&expr.Masq{},
		},
	},
	)

	err = c.Flush()
	if err != nil {
		return fmt.Errorf("failed to flush nftables rules: %w", err)
	}
	return nil
}
