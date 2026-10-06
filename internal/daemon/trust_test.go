package daemon

import (
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestEventCredentialInjectionRequiresTrustedPolicyAllowlist(t *testing.T) {
	lookup := func(key string) (string, bool) { return "host-secret", key == "DEPLOY_TOKEN" }
	untrusted := contract.Exec{}
	if err := applyEventCredentials(&untrusted, state.EventContext{Kind: "pull_request"}, lookup); err != nil {
		t.Fatal(err)
	}
	if _, ok := untrusted.Env["DEPLOY_TOKEN"]; ok {
		t.Fatalf("untrusted PR received host credential: %#v", untrusted.Env)
	}
	untrustedWithNames := contract.Exec{}
	if err := applyEventCredentials(&untrustedWithNames, state.EventContext{Kind: "pull_request", CredentialNames: []string{"DEPLOY_TOKEN"}}, lookup); err != nil {
		t.Fatal(err)
	}
	if _, ok := untrustedWithNames.Env["DEPLOY_TOKEN"]; ok {
		t.Fatal("untrusted credential allowlist was honored")
	}
	trusted := contract.Exec{}
	if err := applyEventCredentials(&trusted, state.EventContext{Kind: "pull_request", Trusted: true, CredentialNames: []string{"DEPLOY_TOKEN"}}, lookup); err != nil {
		t.Fatal(err)
	}
	if trusted.Env["DEPLOY_TOKEN"] != "host-secret" {
		t.Fatalf("trusted credential env=%#v", trusted.Env)
	}
	if len(trusted.SensitiveEnv) != 1 || trusted.SensitiveEnv[0] != "DEPLOY_TOKEN" {
		t.Fatalf("trusted secret was not marked sensitive: %#v", trusted.SensitiveEnv)
	}
	if err := applyEventCredentials(&contract.Exec{}, state.EventContext{Kind: "pull_request", Trusted: true, CredentialNames: []string{"MISSING_SECRET"}}, lookup); err == nil {
		t.Fatal("missing allowlisted host secret should fail closed")
	}
}
