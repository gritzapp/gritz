package lambdamicrovm

import (
	"context"

	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
)

// Cloud is the subset of the AWS Lambda MicroVMs control plane the backend
// uses. *awsmicrovm.Client implements it; keeping it an interface lets the
// backend's orchestration be unit-tested against a fake.
//
// All lifecycle verbs (suspend, resume, terminate) live here, on the trusted
// runner — never in the guest. CreateMicrovmAuthToken mints the short-lived
// proxy token the runner uses to reach the in-VM shim over the managed proxy.
type Cloud interface {
	RunMicrovm(ctx context.Context, in *awsmicrovm.RunMicrovmInput) (*awsmicrovm.RunMicrovmOutput, error)
	GetMicrovm(ctx context.Context, in *awsmicrovm.GetMicrovmInput) (*awsmicrovm.GetMicrovmOutput, error)
	ListMicrovms(ctx context.Context, in *awsmicrovm.ListMicrovmsInput) (*awsmicrovm.ListMicrovmsOutput, error)
	TerminateMicrovm(ctx context.Context, in *awsmicrovm.TerminateMicrovmInput) (*awsmicrovm.TerminateMicrovmOutput, error)
	SuspendMicrovm(ctx context.Context, in *awsmicrovm.SuspendMicrovmInput) (*awsmicrovm.SuspendMicrovmOutput, error)
	ResumeMicrovm(ctx context.Context, in *awsmicrovm.ResumeMicrovmInput) (*awsmicrovm.ResumeMicrovmOutput, error)
	CreateMicrovmAuthToken(ctx context.Context, in *awsmicrovm.CreateMicrovmAuthTokenInput) (*awsmicrovm.CreateMicrovmAuthTokenOutput, error)
}

// Stager stages a task's spec bundle somewhere the MicroVM can fetch it and
// returns a URL the in-VM shim can GET without AWS credentials (a presigned S3
// URL in the live implementation). The 16 KB run-hook payload is too small for
// an agent config, so the backend passes this URL instead of the bundle. S3
// staging is an gritz workaround for the payload limit — deliberately not part
// of the awsmicrovm service model.
type Stager interface {
	// Stage stores data under key in bucket and returns a URL valid for
	// ttlSeconds. The bucket is per-workspace, so it is passed per call.
	Stage(ctx context.Context, bucket, key string, data []byte, ttlSeconds int64) (string, error)
	// Remove deletes a previously staged object. Best-effort cleanup.
	Remove(ctx context.Context, bucket, key string) error
}
