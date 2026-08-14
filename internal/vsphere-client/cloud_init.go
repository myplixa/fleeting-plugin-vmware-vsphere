package vsphereclient

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"

	"github.com/vmware/govmomi/vim25/types"
	"gopkg.in/yaml.v3"
)

type user struct {
	Name         string   `yaml:"name,omitempty"`
	PrimaryGroup string   `yaml:"primary_group,omitempty"`
	Sudo         string   `yaml:"sudo,omitempty"`
	Groups       string   `yaml:"groups,omitempty"`
	LockPasswd   bool     `yaml:"lock_passwd,omitempty"`
	SshKeys      []string `yaml:"ssh_authorized_keys,omitempty"`
}

type cloudInitConfig struct {
	Hostname         string `yaml:"hostname,omitempty"`
	PreserveHostname bool   `yaml:"preserve_hostname"`
	ManageEtcHosts   bool   `yaml:"manage_etc_hosts"`
	Users            []user `yaml:"users"`
}

func (c *client) encodeUserData(username string, pubKey []byte, hostname string) ([]types.BaseOptionValue, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)

	if _, err := gw.Write(pubKey); err != nil {
		return nil, fmt.Errorf("compressing cloud-init user data: %w", err)
	}

	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("compressing cloud-init user data: %w", err)
	}

	data := cloudInitConfig{
		Hostname:         hostname,
		PreserveHostname: false,
		ManageEtcHosts:   true,
		Users: []user{
			{
				Name:         username,
				PrimaryGroup: username,
				Sudo:         "ALL=(ALL) NOPASSWD:ALL",
				Groups:       "sudo, wheel",
				LockPasswd:   false,
				SshKeys: []string{
					string(pubKey),
				},
			},
		},
	}

	marshalled, err := yaml.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("configuring cloud-init user data: %w", err)
	}

	config := fmt.Sprintf("#cloud-config\n\n%s", marshalled)

	encoded := base64.StdEncoding.EncodeToString([]byte(config))

	options := []types.BaseOptionValue{
		&types.OptionValue{
			Key:   "guestinfo.userdata",
			Value: encoded,
		},
		&types.OptionValue{
			Key:   "guestinfo.userdata.encoding",
			Value: "base64",
		},
	}

	return options, nil
}
