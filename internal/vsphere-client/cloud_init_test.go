package vsphereclient

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/types"
)

type fakeClient struct{}

func TestEncodeUserData(t *testing.T) {
	c := &client{}
	username := "testuser"
	pubKey := []byte("ssh-rsa AAAATESTKEY test@example.com")

	options, err := c.encodeUserData(username, pubKey)
	require.NoError(t, err, "encodeUserData returned error")

	var userData, encoding string
	for _, opt := range options {
		ov := opt.(*types.OptionValue)
		switch ov.Key {
		case "guestinfo.userdata":
			userData = ov.Value.(string)
		case "guestinfo.userdata.encoding":
			encoding = ov.Value.(string)
		}
	}

	require.Equal(t, "base64", encoding, "expected encoding 'base64'")

	decoded, err := base64.StdEncoding.DecodeString(userData)
	require.NoError(t, err, "failed to decode base64")

	str := string(decoded)
	require.Contains(t, str, "#cloud-config", "cloud-config header missing")
	require.Contains(t, str, username, "username missing in cloud-init YAML")
	require.Contains(t, str, string(pubKey), "ssh key missing in cloud-init YAML")
	require.Contains(t, str, "users:", "users key missing in cloud-init YAML")
}
