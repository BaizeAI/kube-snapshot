package runtime

import "context"

type Auth struct {
	Registry   string
	ConfigJSON string
}

type Runtime interface {
	Commit(ctx context.Context, containerID string, image string, pause bool, message string, author string) error
	ImageExists(ctx context.Context, image string) bool
	Pull(ctx context.Context, image string, auth *Auth, args ...string) error
	Push(ctx context.Context, image string, auth *Auth) error
}
