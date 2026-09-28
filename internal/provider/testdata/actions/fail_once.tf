provider "tfcoremock" {
  resource_directory = "%s"
  fail_once          = true
  fail_on_invoke     = ["doomed"]
}

resource "tfcoremock_simple_resource" "resource" {
  lifecycle {
    action_trigger {
      events  = [before_create]
      actions = [action.tfcoremock_simple_resource.action]
    }
  }
}

action "tfcoremock_simple_resource" "action" {
  config {
    string = "doomed"
  }
}
