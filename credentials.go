package vsphere

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// credentialsFile mirrors the subset of InstanceGroup's vCenter-auth fields
// that vsphere_credentials_file can supply, so the same secrets don't have
// to be duplicated inline in every [[runners]] plugin_config block that
// targets the same vCenter.
type credentialsFile struct {
	Url                string `yaml:"url"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	InsecureConnection bool   `yaml:"insecure_connection"`
}

func loadCredentialsFile(path string) (*credentialsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading vsphere_credentials_file: %w", err)
	}

	var creds credentialsFile
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parsing vsphere_credentials_file: %w", err)
	}

	return &creds, nil
}
