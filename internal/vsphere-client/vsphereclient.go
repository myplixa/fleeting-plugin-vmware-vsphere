package vsphereclient

import "context"

type Client interface {
	CloneVM(ctx context.Context)
	DeleteVM(ctx context.Context)
}
