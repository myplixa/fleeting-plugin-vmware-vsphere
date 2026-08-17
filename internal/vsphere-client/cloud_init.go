package vsphereclient

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/textproto"

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
	Hostname                string `yaml:"hostname,omitempty"`
	PreserveHostname        bool   `yaml:"preserve_hostname"`
	ManageEtcHosts          bool   `yaml:"manage_etc_hosts"`
	PackageUpdate           bool   `yaml:"package_update"`
	PackageUpgrade          bool   `yaml:"package_upgrade"`
	PackageRebootIfRequired bool   `yaml:"package_reboot_if_required"`
	Users                   []user `yaml:"users"`
	FinalMessage            string `yaml:"final_message"`
}

// cloudInitExtraData is the data made available to cloud_init_extra_file templates.
type cloudInitExtraData struct {
	Hostname   string
	RunnerName string
	Vars       map[string]string
}

func (c *client) encodeUserData(username string, pubKey []byte, hostname string) ([]types.BaseOptionValue, error) {
	data := cloudInitConfig{
		Hostname:                hostname,
		PreserveHostname:        false,
		ManageEtcHosts:          true,
		PackageUpdate:           false,
		PackageUpgrade:          false,
		PackageRebootIfRequired: false,
		FinalMessage:            "Cloud-init finished successfully at $TIMESTAMP",
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

	baseConfig := fmt.Sprintf("#cloud-config\n\n%s", marshalled)

	config := baseConfig
	if c.cloudInitExtraTemplate != nil {
		extra, err := c.renderCloudInitExtra(hostname)
		if err != nil {
			return nil, err
		}

		config, err = buildMultipartUserData(baseConfig, extra)
		if err != nil {
			return nil, fmt.Errorf("assembling multipart cloud-init user data: %w", err)
		}
	}

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

func (c *client) renderCloudInitExtra(hostname string) (string, error) {
	data := cloudInitExtraData{
		Hostname:   hostname,
		RunnerName: c.namePrefix,
		Vars:       c.cloudInitVars,
	}

	var buf bytes.Buffer
	if err := c.cloudInitExtraTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering cloud_init_extra_file: %w", err)
	}

	return buf.String(), nil
}

// buildMultipartUserData assembles the given #cloud-config documents into the
// MIME multi-part format cloud-init expects for combining several user-data
// documents into one. cloud-init merges write_files/runcmd/packages across
// parts (list values append), so the plugin's own base config (hostname,
// SSH user, package_* settings) and the operator-supplied extra config are
// both applied rather than one overwriting the other.
func buildMultipartUserData(parts ...string) (string, error) {
	var buf bytes.Buffer
	mpw := multipart.NewWriter(&buf)

	for i, content := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", `text/cloud-config; charset="us-ascii"`)
		header.Set("MIME-Version", "1.0")
		header.Set("Content-Transfer-Encoding", "7bit")
		header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cloud-config-%d.yaml"`, i))

		pw, err := mpw.CreatePart(header)
		if err != nil {
			return "", err
		}

		if _, err := pw.Write([]byte(content)); err != nil {
			return "", err
		}
	}

	if err := mpw.Close(); err != nil {
		return "", err
	}

	return fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\nMIME-Version: 1.0\n\n%s", mpw.Boundary(), buf.String()), nil
}
