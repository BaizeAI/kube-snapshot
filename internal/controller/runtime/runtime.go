package runtime

import "context"

type Auth struct {
	ConfigJSON string
}

type Runtime interface {
	Commit(ctx context.Context, containerID, image string, pause bool, message, author string, insecureRegistry bool) error
	ImageExists(ctx context.Context, image string) bool
	Pull(ctx context.Context, image string, auth *Auth, insecureRegistry bool, args ...string) error
	Push(ctx context.Context, image string, auth *Auth, insecureRegistry bool) error
}
