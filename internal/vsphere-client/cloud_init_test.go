package vsphereclient

import (
	"encoding/base64"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/types"
)

type fakeClient struct{}

func TestEncodeUserData(t *testing.T) {
	c := &client{}
	username := "testuser"
	pubKey := []byte("ssh-rsa AAAATESTKEY test@example.com")
	hostname := "esx-01-ci-vm-small-a1b2c3d4"

	options, err := c.encodeUserData(username, pubKey, hostname)
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
	require.Contains(t, str, "sudo, wheel", "required groups missing in cloud-init YAML")
	require.Contains(t, str, "hostname: "+hostname, "hostname missing in cloud-init YAML")
	require.Contains(t, str, "preserve_hostname: false", "preserve_hostname missing in cloud-init YAML")
	require.Contains(t, str, "package_update: false", "package_update missing in cloud-init YAML")
	require.Contains(t, str, "package_upgrade: false", "package_upgrade missing in cloud-init YAML")
	require.Contains(t, str, "package_reboot_if_required: false", "package_reboot_if_required missing in cloud-init YAML")
	require.Contains(t, str, "final_message: Cloud-init finished successfully at $TIMESTAMP", "final_message missing in cloud-init YAML")
}

func TestEncodeUserDataWithCloudInitExtra(t *testing.T) {
	tmpl, err := template.New("extra").Parse("write_files:\n  - path: /etc/example.conf\n    content: {{ .RunnerName }}-{{ .Hostname }}-{{ .Vars.foo }}\n")
	require.NoError(t, err, "failed to parse test template")

	c := &client{
		namePrefix:             "ci-vm-small",
		cloudInitExtraTemplate: tmpl,
		cloudInitVars:          map[string]string{"foo": "bar"},
	}
	username := "testuser"
	pubKey := []byte("ssh-rsa AAAATESTKEY test@example.com")
	hostname := "esx-01-ci-vm-small-a1b2c3d4"

	options, err := c.encodeUserData(username, pubKey, hostname)
	require.NoError(t, err, "encodeUserData returned error")

	var userData string
	for _, opt := range options {
		ov := opt.(*types.OptionValue)
		if ov.Key == "guestinfo.userdata" {
			userData = ov.Value.(string)
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(userData)
	require.NoError(t, err, "failed to decode base64")

	str := string(decoded)
	require.Contains(t, str, "Content-Type: multipart/mixed", "expected a multipart message when cloud_init_extra_file is set")
	require.Contains(t, str, "#cloud-config", "base cloud-config part missing")
	require.Contains(t, str, "hostname: "+hostname, "base cloud-config part missing hostname")
	require.Contains(t, str, "ci-vm-small-"+hostname+"-bar", "extra part not rendered with expected template data")
}
