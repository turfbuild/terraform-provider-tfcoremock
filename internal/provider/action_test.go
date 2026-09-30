package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

func TestAccSimpleAction(t *testing.T) {
	t.Cleanup(CleanupTestingDirectories(t))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProviderFactories(""),
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
		},
		Steps: []resource.TestStep{
			{
				Config: LoadFile(t, "testdata/actions/simple.tf"),
			},
		},
	})
}

func TestAccDynamicAction(t *testing.T) {
	t.Cleanup(CleanupTestingDirectories(t))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProviderFactories(LoadFile(t, "testdata/actions/dynamic_resources.json")),
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
		},
		Steps: []resource.TestStep{
			{
				Config: LoadFile(t, "testdata/actions/dynamic.tf"),
			},
		},
	})
}

// TestAccSimpleActionWarns proves warn_on_invoke is accepted and that a warned
// invocation still succeeds: a warning diagnostic must never fail the apply.
func TestAccSimpleActionWarns(t *testing.T) {
	t.Cleanup(CleanupTestingDirectories(t))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProviderFactories(""),
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
		},
		Steps: []resource.TestStep{
			{
				Config: LoadFile(t, "testdata/actions/warn.tf"),
			},
		},
	})
}

// TestAccSimpleActionFailsOnInvoke proves fail_on_invoke fails the invocation,
// and that a value in warn_on_invoke as well still fails: the warning rides
// beside the error, it does not replace it.
func TestAccSimpleActionFailsOnInvoke(t *testing.T) {
	t.Cleanup(CleanupTestingDirectories(t))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProviderFactories(""),
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
		},
		Steps: []resource.TestStep{
			{
				Config:      LoadFile(t, "testdata/actions/fail.tf"),
				ExpectError: regexp.MustCompile("action invocation failed"),
			},
		},
	})
}

// TestAccSimpleActionFailsOnceOnInvoke proves fail_once reaches actions: the
// before_create gate fails the first apply (so nothing is created), and the
// same configuration applied again invokes the gate past its strike and
// creates the resource. The resource directory is a temporary one because the
// strike file outlives the destroy.
func TestAccSimpleActionFailsOnceOnInvoke(t *testing.T) {
	config := fmt.Sprintf(LoadFile(t, "testdata/actions/fail_once.tf"), t.TempDir())
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProviderFactories(""),
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
		},
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile("action invocation failed"),
			},
			{
				Config: config,
			},
		},
	})
}

// TestAccSimpleActionDeferRefused proves defer_on_action reaches PlanAction: a
// stable Terraform allows no deferrals, so the listed action fails to plan
// rather than being planned as if nothing were marked.
func TestAccSimpleActionDeferRefused(t *testing.T) {
	t.Cleanup(CleanupTestingDirectories(t))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProviderFactories(""),
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
		},
		Steps: []resource.TestStep{
			{
				Config:      LoadFile(t, "testdata/actions/defer.tf"),
				ExpectError: regexp.MustCompile("Invalid action deferral"),
			},
		},
	})
}

// TestAccActionsProtocol5 runs the action configurations above with the
// provider served over plugin protocol 5: the same outcomes, since nothing an
// action does is protocol-specific once the schema can be expressed.
func TestAccActionsProtocol5(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    string
		resources string
		errs      []*regexp.Regexp // one per step; nil = the step succeeds
	}{
		{name: "simple", config: LoadFile(t, "testdata/actions/simple.tf"), errs: []*regexp.Regexp{nil}},
		{name: "dynamic", config: LoadFile(t, "testdata/actions/dynamic.tf"), resources: LoadFile(t, "testdata/actions/dynamic_resources.json"), errs: []*regexp.Regexp{nil}},
		{name: "warn", config: LoadFile(t, "testdata/actions/warn.tf"), errs: []*regexp.Regexp{nil}},
		{name: "fail", config: LoadFile(t, "testdata/actions/fail.tf"), errs: []*regexp.Regexp{regexp.MustCompile("action invocation failed")}},
		{name: "defer", config: LoadFile(t, "testdata/actions/defer.tf"), errs: []*regexp.Regexp{regexp.MustCompile("Invalid action deferral")}},
		{name: "fail_once", config: fmt.Sprintf(LoadFile(t, "testdata/actions/fail_once.tf"), t.TempDir()), errs: []*regexp.Regexp{regexp.MustCompile("action invocation failed"), nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := make([]resource.TestStep, len(tc.errs))
			for i, want := range tc.errs {
				steps[i] = resource.TestStep{Config: tc.config, ExpectError: want}
			}
			resource.Test(t, resource.TestCase{
				ProtoV5ProviderFactories: ProviderFactories5(tc.resources),
				TerraformVersionChecks: []tfversion.TerraformVersionCheck{
					tfversion.SkipBelow(version.Must(version.NewVersion("1.14.0-beta1"))),
				},
				Steps: steps,
			})
		})
	}
}
