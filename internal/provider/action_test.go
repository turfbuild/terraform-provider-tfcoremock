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
