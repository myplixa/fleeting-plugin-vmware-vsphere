package vsphere

import (
	"context"
	"crypto"
	"fmt"

	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	"golang.org/x/crypto/ssh"
)

type PrivPub interface {
	crypto.PrivateKey
	Public() crypto.PublicKey
}

func (g *InstanceGroup) getSSHPubKey(ctx context.Context, info *provider.ConnectInfo) (ssh.PublicKey, error) {
	priv, err := ssh.ParseRawPrivateKey(info.Key)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}

	key, ok := priv.(PrivPub)
	if !ok {
		return nil, fmt.Errorf("key doesn't export PublicKey()")
	}

	sshPubKey, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return nil, fmt.Errorf("generating ssh public key: %w", err)
	}

	return sshPubKey, nil
}
