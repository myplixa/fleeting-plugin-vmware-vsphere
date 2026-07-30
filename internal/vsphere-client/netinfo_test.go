package vsphereclient

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/types"
)

// nic builds a GuestNicInfo that pickGuestIP considers, i.e. one that reports a
// MAC address and an IpConfig. Both are required: VMware Tools also reports
// entries for devices with no guest-side configuration.
func nic(mac string, ips ...string) types.GuestNicInfo {
	return types.GuestNicInfo{
		MacAddress: mac,
		IpConfig:   &types.NetIpConfigInfo{},
		IpAddress:  ips,
	}
}

func Test_pickGuestIP(t *testing.T) {
	tests := []struct {
		name string
		nics []types.GuestNicInfo
		want string
	}{
		{
			name: "no nics",
			nics: nil,
			want: "",
		},
		{
			name: "single ipv4",
			nics: []types.GuestNicInfo{nic("00:50:56:00:00:01", "192.168.1.10")},
			want: "192.168.1.10",
		},
		{
			// A Windows guest with IPv6 enabled reports its link-local address
			// first. Returning it leaves the runner dialing an unreachable
			// address, and the job hangs on "Dialing instance..." with no error.
			name: "link-local ipv6 first, ipv4 second",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "fe80::250:56ff:fe00:1", "192.168.1.10"),
			},
			want: "192.168.1.10",
		},
		{
			name: "ipv4 on a later nic wins over link-local on the first",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "fe80::250:56ff:fe00:1"),
				nic("00:50:56:00:00:02", "10.0.0.5"),
			},
			want: "10.0.0.5",
		},
		{
			// The previous implementation broke out of the address loop only, so
			// the NIC loop kept going and the last NIC overwrote a good address.
			name: "first usable address wins over a later nic",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "192.168.1.10"),
				nic("00:50:56:00:00:02", "10.0.0.5"),
			},
			want: "192.168.1.10",
		},
		{
			name: "ipv4 preferred over routable ipv6 on the same nic",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "2001:db8::1", "192.168.1.10"),
			},
			want: "192.168.1.10",
		},
		{
			name: "routable ipv6 used when there is no ipv4",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "fe80::250:56ff:fe00:1", "2001:db8::1"),
			},
			want: "2001:db8::1",
		},
		{
			name: "loopback skipped",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "127.0.0.1", "::1", "192.168.1.10"),
			},
			want: "192.168.1.10",
		},
		{
			// net.ParseIP returns nil here. The previous check compared its
			// String() against "nil", but the zero value prints "<nil>", so
			// malformed addresses were never skipped.
			name: "malformed address skipped",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "not-an-ip", "192.168.1.10"),
			},
			want: "192.168.1.10",
		},
		{
			name: "nic without mac address ignored",
			nics: []types.GuestNicInfo{
				nic("", "10.0.0.5"),
				nic("00:50:56:00:00:02", "192.168.1.10"),
			},
			want: "192.168.1.10",
		},
		{
			name: "nic without ip config ignored",
			nics: []types.GuestNicInfo{
				{MacAddress: "00:50:56:00:00:01", IpAddress: []string{"10.0.0.5"}},
				nic("00:50:56:00:00:02", "192.168.1.10"),
			},
			want: "192.168.1.10",
		},
		{
			name: "only link-local addresses yields nothing",
			nics: []types.GuestNicInfo{
				nic("00:50:56:00:00:01", "fe80::250:56ff:fe00:1", "169.254.1.5"),
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pickGuestIP(tt.nics))
		})
	}
}
