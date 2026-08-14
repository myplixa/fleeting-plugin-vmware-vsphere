package realvcenter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gitlab.com/gitlab-org/fleeting/fleeting"
	"gitlab.com/gitlab-org/fleeting/fleeting/connector"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	vsphere "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere"
)

var (
	pluginBinaryPath = flag.String("plugin-binary-path", "", "Path to the plugin binary")
	configFilePath   = flag.String("config-path", "", "Path to the configuration file")
)

type Config struct {
	PluginConfig    vsphere.InstanceGroup    `json:"plugin_config"`
	ConnectorConfig provider.ConnectorConfig `json:"connector_config"`
	// DockerTestImage is pulled and run to prove Docker is callable, not just
	// installed. Defaults to "hello-world" (Docker Hub) if unset — override
	// with an image reachable from your network if the clone has no direct
	// internet access.
	DockerTestImage string `json:"docker_test_image"`
}

func TestRealVCenterProvisioning(t *testing.T) {
	if *pluginBinaryPath == "" {
		t.Skip("plugin binary path is missing, skipping")
	}
	if *configFilePath == "" {
		t.Skip("config file path is missing, skipping")
	}
	if _, err := os.Stat(*configFilePath); os.IsNotExist(err) {
		t.Skipf("config file %q does not exist, skipping (copy config.example.json and fill in real vCenter details to run this test)", *configFilePath)
	}

	configFile, err := os.Open(*configFilePath)
	require.NoError(t, err)
	defer configFile.Close()

	var cfg Config
	require.NoError(t, json.NewDecoder(configFile).Decode(&cfg))

	dockerTestImage := cfg.DockerTestImage
	if dockerTestImage == "" {
		dockerTestImage = "hello-world"
	}

	configJSON, err := json.Marshal(cfg.PluginConfig)
	require.NoError(t, err)

	runner, err := fleeting.RunPlugin(*pluginBinaryPath, configJSON)
	require.NoError(t, err)
	t.Cleanup(runner.Kill)

	subCh := make(chan fleeting.Instance, 10)

	provisioner, err := fleeting.Init(context.Background(), nil, runner.InstanceGroup(),
		fleeting.WithMaxSize(1),
		fleeting.WithInstanceGroupSettings(provider.Settings{
			ConnectorConfig: cfg.ConnectorConfig,
		}),
		fleeting.WithSubscriber(func(instances []fleeting.Instance) {
			for _, inst := range instances {
				subCh <- inst
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { provisioner.Shutdown(context.Background()) })

	t.Log("requesting 1 instance")
	provisioner.Request(1)

	var inst fleeting.Instance
	for i := range subCh {
		if i.State() == provider.StateRunning {
			inst = i
			break
		}
	}
	require.NotNil(t, inst, "instance never reached running state")
	require.Equal(t, fleeting.CauseRequested, inst.Cause())
	t.Logf("instance running: %s", inst.ID())

	// Registered last so it runs first (t.Cleanup is LIFO): delete the
	// instance while the plugin connection is still alive, before the
	// provisioner/plugin-process cleanups above tear it down.
	t.Cleanup(func() {
		t.Logf("deleting instance: %s", inst.ID())
		inst.Delete()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for {
			select {
			case i := <-subCh:
				if i.ID() == inst.ID() && i.State() == provider.StateDeleted {
					return
				}
			case <-ctx.Done():
				t.Logf("timed out waiting for instance %s to report deleted; provisioner shutdown will retry", inst.ID())
				return
			}
		}
	})

	info, err := inst.ConnectInfo(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, info.Username, "username unexpectedly empty from fleeting plugin")

	runCommand := func(t *testing.T, command string) string {
		t.Helper()

		var stdout, stderr bytes.Buffer
		err := connector.Run(context.Background(), info, connector.ConnectorOptions{
			RunOptions: connector.RunOptions{
				Command: command,
				Stdout:  &stdout,
				Stderr:  &stderr,
			},
		})
		require.NoErrorf(t, err, "command %q failed, stderr: %s", command, stderr.String())

		return stdout.String()
	}

	t.Run("ssh access", func(t *testing.T) {
		out := runCommand(t, "echo fleeting-realtest-ok")
		require.Contains(t, out, "fleeting-realtest-ok")
	})

	t.Run("docker daemon reachable", func(t *testing.T) {
		out := runCommand(t, "docker version")
		require.Contains(t, out, "Server:")
	})

	t.Run("docker is callable", func(t *testing.T) {
		out := runCommand(t, "docker run --rm "+dockerTestImage)
		require.NotEmpty(t, out)
	})

	t.Run("guest hostname matches the clone name", func(t *testing.T) {
		out := runCommand(t, "hostname")
		require.Contains(t, out, inst.ID())
	})
}
