package internal

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestParseRestartPolicy(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		detach  bool
		want    container.RestartPolicy
		wantErr string
	}{
		{name: "empty detached", policy: "", detach: true, want: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}},
		{name: "empty attached", policy: "", detach: false, want: container.RestartPolicy{Name: container.RestartPolicyDisabled}},
		{name: "no detached", policy: "no", detach: true, want: container.RestartPolicy{Name: container.RestartPolicyDisabled}},
		{name: "no attached", policy: "no", detach: false, want: container.RestartPolicy{Name: container.RestartPolicyDisabled}},
		{name: "always", policy: "always", detach: true, want: container.RestartPolicy{Name: container.RestartPolicyAlways}},
		{name: "unless-stopped", policy: "unless-stopped", detach: true, want: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}},
		{name: "on-failure", policy: "on-failure", detach: true, want: container.RestartPolicy{Name: container.RestartPolicyOnFailure}},
		{name: "on-failure with count", policy: "on-failure:5", detach: true, want: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 5}},
		{name: "negative count", policy: "on-failure:-1", detach: true, wantErr: "invalid restart policy: maximum retry count cannot be negative"},
		{name: "non-integer count", policy: "on-failure:x", detach: true, wantErr: "invalid restart policy format: maximum retry count must be an integer"},
		{name: "count on always", policy: "always:3", detach: true, wantErr: "invalid restart policy: maximum retry count can only be used with 'on-failure'"},
		{name: "missing name", policy: ":3", detach: true, wantErr: "invalid restart policy format: no policy provided before colon"},
		{name: "unknown", policy: "sometimes", detach: true, wantErr: "invalid restart policy: unknown policy 'sometimes'; use one of 'no', 'always', 'on-failure', or 'unless-stopped'"},
		{name: "always attached", policy: "always", detach: false, wantErr: "conflicting options: cannot specify both --restart and an attached (auto-removed) helper; use --detach"},
		{name: "on-failure attached", policy: "on-failure:2", detach: false, wantErr: "conflicting options: cannot specify both --restart and an attached (auto-removed) helper; use --detach"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRestartPolicy(tc.policy, tc.detach)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got policy %+v", tc.wantErr, got)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("got error %q, want %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
